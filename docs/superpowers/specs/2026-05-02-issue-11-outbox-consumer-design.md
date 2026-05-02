# Design: Polling outbox consumer (Issue #11)

**Status:** Draft for review
**Date:** 2026-05-02
**Owner:** Data plane
**ADRs:** ADR-004 (Kafka), ADR-008 (outbox + polling consumer), ADR-017 (interface seams)
**GitHub issue:** [#11](https://github.com/gitscale-platform/gitscale/issues/11)

## 1. Summary

A Go service in `plane/data/outbox/` that drains every domain's `*_outbox` table and publishes each row as an event to the corresponding Kafka topic. One consumer instance per domain. Polling-based (no logical replication, no CDC) per ADR-008.

The hard correctness goal: **no event is silently lost between the source-of-truth transaction's COMMIT and the appearance of that event on the Kafka topic.** Duplicates are acceptable; they are deduped at the consumer side on `event_id`. Loss is not.

## 2. Scope

In:

- Per-domain consumer that drains `<schema>.<schema>_outbox`, publishes to its topic, marks rows processed.
- Concurrency control: many replicas safe; per-poll, one wins exclusivity for a given domain.
- Producer wrapper interface around `confluent-kafka-go`.
- Test harness with both mock producer (unit) and `testcontainers` PostgreSQL + Kafka (integration).
- Metrics: lag, batch size, publish latency, lock-miss count, publish-error count.
- Configuration via env vars.

Out:

- The TTL/vacuum job that deletes rows where `processed_at < now() - 24h` — separate issue.
- The downstream consumers (search indexer, webhook fanout, billing aggregator, audit, cold-storage migrator) — separate issues.
- Schema registry / payload contract — open question, will be tracked alongside #12.
- Multi-region replication of Kafka — out of scope until DR phase.

## 3. Authoritative outbox row schema

Each domain's outbox table (already merged from #6–#10) is structurally identical:

```sql
CREATE TABLE <schema>.<schema>_outbox (
  id              BIGSERIAL    PRIMARY KEY,
  event_id        UUID         NOT NULL UNIQUE,
  aggregate_type  TEXT         NOT NULL,
  aggregate_id    UUID         NOT NULL,
  event_type      TEXT         NOT NULL,
  payload         JSONB        NOT NULL,
  processed_at    TIMESTAMPTZ  NULL,
  created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_<schema>_outbox_unprocessed
  ON <schema>.<schema>_outbox (created_at)
  WHERE processed_at IS NULL;
```

> **Correction vs issue #11 text:** The issue's pseudo-SQL uses `processed = false / processed = true`. The merged migration uses `processed_at IS NULL` / `processed_at = now()`. This spec uses the migration as ground truth; the issue's pseudo-SQL is a typo.

## 4. Package layout

```
plane/data/outbox/
  consumer.go           # OutboxConsumer interface + Run loop
  config.go             # Config struct, env loader
  drain.go              # drainBatch — single-cycle txn body
  producer.go           # KafkaProducer interface
  producer_kafka.go     # confluent-kafka-go impl
  producer_mock.go      # in-memory impl for unit tests
  metrics.go            # Prometheus registrations
  consumer_test.go      # unit tests (mock DB + mock producer)
  integration_test.go   # testcontainers PG + Kafka

plane/data/outbox/wiring/
  wiring.go             # builds 5 consumer instances from config
```

## 5. Interfaces

```go
// plane/data/outbox/consumer.go

type OutboxConsumer interface {
    // Run blocks until ctx is canceled. Returns ctx.Err() on clean shutdown.
    Run(ctx context.Context) error
}

// plane/data/outbox/producer.go

type KafkaProducer interface {
    // PublishBatch produces every event in the batch to the given topic, then
    // blocks until either every event has a successful delivery report or
    // ctx is canceled. Partial success returns an error; the caller MUST treat
    // the entire batch as not-published (will be retried via polling).
    PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error
    Close() error
}

type OutboxRow struct {
    ID            int64
    EventID       uuid.UUID
    AggregateType string
    AggregateID   uuid.UUID
    EventType     string
    Payload       json.RawMessage
    CreatedAt     time.Time
}

// plane/data/outbox/config.go

type Config struct {
    Domain         string         // "identity", "repositories", …
    Table          string         // schema-qualified: "identity.identity_outbox"
    Topic          string         // "gitscale.identity.events"
    DB             *pgxpool.Pool
    Producer       KafkaProducer
    PollInterval   time.Duration  // OUTBOX_POLL_INTERVAL_MS, default 1000
    PublishTimeout time.Duration  // OUTBOX_PUBLISH_TIMEOUT_MS, default 5000
    BatchSize      int            // OUTBOX_BATCH_SIZE, default 100
    Metrics        *Metrics       // nil-safe
}
```

## 6. Concurrency model

**Decision:** N replicas may run; per poll cycle, exactly one wins a per-domain advisory lock. Losers exit the cycle immediately and retry on the next tick.

- Lock function: `pg_try_advisory_xact_lock(hashtext('<schema>.<schema>_outbox')::bigint)`. The explicit `::bigint` cast disambiguates the `(bigint)` overload from the `(int, int)` overload — both exist and would silently take an `int4` differently.
- Lock scope: **transaction**. Released automatically on COMMIT or ROLLBACK. No risk of orphan lock from a crashed worker.
- `FOR UPDATE SKIP LOCKED` is retained as defense-in-depth, even though only one advisory-lock holder is active per cycle. Cheap and harmless.

**Why not let N workers drain the same domain in parallel?** Per-aggregate ordering. ADR-004 mandates per-partition ordering keyed by aggregate. With two parallel drainers, batches for the same `aggregate_id` could publish in non-deterministic order. Single-active-drainer per domain preserves it. Throughput is bounded by per-domain Kafka publish rate, which is sufficient for projected volumes.

## 7. Drain cycle — pinned ordering

Inside one txn, in this order:

1. `SELECT pg_try_advisory_xact_lock(...)` → if false, return without doing work.
2. `SELECT id, event_id, aggregate_type, aggregate_id, event_type, payload, created_at FROM <table> WHERE processed_at IS NULL ORDER BY created_at, id LIMIT $batch FOR UPDATE SKIP LOCKED` → batch.
3. If batch empty: return (commits empty txn, releases lock).
4. `producer.PublishBatch(pubCtx, topic, batch)` with `pubCtx` deadline = `PublishTimeout`. Synchronous: returns only after every record's delivery report or after ctx timeout.
5. If publish errored: return error → `ROLLBACK` → no UPDATE applied → next poll re-selects same rows.
6. `UPDATE <table> SET processed_at = now() WHERE id = ANY($ids)`.
7. `COMMIT`.

**Tiebreaker on ORDER BY:** `created_at, id`. `id` is `BIGSERIAL` (verified) — gives deterministic order across microsecond collisions.

**Why publish before UPDATE inside the same txn:** the only ordering that has no data-loss window. Crash anywhere → ROLLBACK → rows retried → consumer dedupes on `event_id`. The other two orderings (UPDATE-then-publish-after-commit; intermediate-state two-txn) lose events on crash or add a recovery state machine.

## 8. Delivery semantics

**At-least-once at the broker, effectively-once at the consumer.**

- The Kafka producer is idempotent within a session (`enable.idempotence=true`, `acks=all`). Within a session, a successful publish never produces a duplicate at the broker.
- Across producer restarts, idempotence does not carry over: a republished row from a new session lands as a new broker message.
- Therefore every consumer **must** dedupe on `event_id` (already mandated by `CLAUDE.md`). This is the durable defense.

This is recorded as a doc in the consumer SDK (out of scope here) and a hard expectation in ADR-008.

## 9. Producer configuration

```
enable.idempotence=true
acks=all
max.in.flight.requests.per.connection=5    # safe with idempotence enabled
delivery.timeout.ms=5000                    # ≤ Config.PublishTimeout
compression.type=zstd
linger.ms=5
batch.size=65536
```

Partition key: TBD pending #12 reconciliation with ADR-004 — see §13.

## 10. Failure modes

| Scenario | Outcome | Recovery |
|---|---|---|
| Crash after SELECT, before publish | Txn rollback. Rows untouched. | Next poll re-selects + republishes. |
| Crash after publish, before UPDATE | Txn rollback. Events on broker, rows still `processed_at IS NULL`. | Next poll republishes. Consumer dedupes on `event_id`. |
| Crash after UPDATE, before COMMIT | Txn rollback (UPDATE never durable). Events on broker, rows still unprocessed. | Same as above — republish + dedupe. |
| Crash after COMMIT | Clean. | None needed. |
| `PublishBatch` returns partial error | Txn rollback. **Caller treats whole batch as unpublished.** | Next poll retries all rows. Some events are now duplicated on the broker; consumer dedupes. |
| `PublishBatch` exceeds `PublishTimeout` | Txn rollback. Lock released. | Next poll retries. Lag metric grows. Alert fires when oldest-unprocessed > threshold. |
| Kafka cluster unavailable | Same as timeout — repeats every poll cycle. | Backoff (see §12). Lag alert. |
| One row in batch is broker-rejected (e.g., size, schema) | **Whole batch rolled back, error logged, lag stalls.** Domain is jammed until offending row is removed. | v1: manual op intervention — operator inspects, deletes/quarantines the row. v2: bisect on failure (tracked as future work). |
| DB connection lost mid-txn | Txn aborted by driver. Lock released on conn close. | Next poll. |
| Two replicas poll simultaneously | One wins the advisory lock; other returns immediately. | None. |

## 11. Backpressure & graceful shutdown

- `Run` ticks via `time.NewTicker(PollInterval)` plus a `select { case <-ctx.Done(): return }` arm.
- Mid-batch ctx cancel: the txn body's queries observe ctx and return early; pgx aborts the txn → ROLLBACK → state preserved.
- After ctx cancel, `Run` calls `producer.Close()` (drains the producer's internal queue with a 5s deadline) before returning.
- Sustained Kafka outage: each poll cycle does a fast advisory-lock acquire + SELECT + failed publish + ROLLBACK. No work, no progress, no resource leak. Lag metric grows monotonically; ops alert is the escalation path.
- No exponential backoff on the poll loop in v1 — interval is already 1s, and a hot-loop on a dead Kafka costs only one SELECT + one failed publish per second per domain. Cheap. Add backoff if measured cost becomes a problem.

## 12. Observability

Metrics (Prometheus, registered via `Metrics`):

| Name | Type | Labels | Purpose |
|---|---|---|---|
| `outbox_drain_cycles_total` | counter | `domain`, `result` (`ok`, `lock_missed`, `empty`, `publish_error`, `update_error`) | Loop health |
| `outbox_batch_size` | histogram | `domain` | Capacity headroom signal |
| `outbox_publish_duration_seconds` | histogram | `domain`, `result` | Kafka health |
| `outbox_oldest_unprocessed_seconds` | gauge | `domain` | **SLO signal.** Sampled every poll cycle by **every replica regardless of whether it won the advisory lock**, so the signal does not drop when leadership rotates. Alert > 60s sustained. |
| `outbox_processed_total` | counter | `domain` | Throughput |
| `outbox_advisory_lock_held` | gauge | `domain` | 0/1 — leadership tracking |

The `outbox_oldest_unprocessed_seconds` gauge is computed cheaply at the start of each cycle:

```sql
SELECT EXTRACT(EPOCH FROM (now() - MIN(created_at)))
FROM <table> WHERE processed_at IS NULL;
```

Tracing: each drain cycle gets a span; `PublishBatch` gets a child span with `messaging.kafka.batch.size`, `messaging.destination.name`, `outbox.domain` attributes.

Logs: structured JSON, level `info` for normal cycles only when batch > 0; level `warn` on publish errors; level `error` on update errors.

## 13. Open dependency: partition key (cross-issue with #12)

ADR-004 mandates per-partition ordering on `repo_id` / `org_id`. Issue #12 (Kafka topology) currently specifies `aggregate_id` as the partition key. These are not the same: a `pr.opened` event has `aggregate_id` = PR UUID, not repo UUID, so two PRs on the same repo could land on different partitions and lose repo-level ordering.

**This spec does not pick a side.** It treats the partition key as injected by the producer wrapper from the row, with the column choice deferred to issue #12's resolution. The consumer's interface (`PublishBatch(ctx, topic, batch)`) is unaffected by the eventual decision.

Three resolutions are on the table for #12 — captured here only so this spec's interface can support whichever is chosen:

- **R1:** Use `aggregate_id`. ADR-004 amended; per-aggregate ordering only.
- **R2:** Add `repo_id UUID NULL` column to every outbox row (filled by domain services where applicable; null for org-level events with separate routing).
- **R3:** Two-tier key: `org_id` for collaboration/repositories topics; `aggregate_id` for identity/billing.

The producer wrapper will read whichever column ends up canonical. **No code in this consumer hardcodes the column name** — the producer impl reads from a `PartitionKey(OutboxRow) []byte` strategy func passed via Config.

## 14. Domain wiring

```go
// plane/data/outbox/wiring/wiring.go

func StartAll(ctx context.Context, db *pgxpool.Pool, prod KafkaProducer) []*outbox.OutboxConsumer {
    domains := []outbox.Config{
        {Domain: "identity",      Table: "identity.identity_outbox",           Topic: "gitscale.identity.events"},
        {Domain: "repositories",  Table: "repositories.repositories_outbox",   Topic: "gitscale.repositories.events"},
        {Domain: "collaboration", Table: "collaboration.collaboration_outbox", Topic: "gitscale.collaboration.events"},
        {Domain: "ci",            Table: "ci.ci_outbox",                       Topic: "gitscale.ci.events"},
        {Domain: "billing",       Table: "billing.billing_outbox",             Topic: "gitscale.billing.events"},
    }
    // … apply env defaults to each, start one goroutine per domain
}
```

Each consumer is independent. A failure in one domain (e.g., billing's Kafka topic ACL is broken) does not affect the others.

## 15. Configuration

Env vars, all optional with documented defaults:

| Var | Default | Purpose |
|---|---|---|
| `OUTBOX_POLL_INTERVAL_MS` | 1000 | Poll interval |
| `OUTBOX_PUBLISH_TIMEOUT_MS` | 5000 | Per-batch publish deadline |
| `OUTBOX_BATCH_SIZE` | 100 | `LIMIT` on SELECT |
| `KAFKA_BOOTSTRAP_SERVERS` | (required) | Producer config |
| `KAFKA_CLIENT_ID` | `gitscale-outbox-<hostname>` | Producer client id (kept stable per pod for log correlation; idempotence is per-session regardless) |

Defaults live in `config.go` and are overridden by env. Tests inject explicit values.

## 16. Testing strategy

**Unit (`consumer_test.go`):**

- `drainBatch` with mock pgx + mock producer. Cases:
  - Lock not acquired → no SELECT issued.
  - Empty batch → no UPDATE issued, lock released.
  - Successful publish + UPDATE.
  - Publish error → rollback path; no UPDATE issued.
  - Producer panics → recovered, txn rolled back.
- `Run` loop: ticker-driven, ctx cancel exits cleanly.

**Integration (`integration_test.go`, testcontainers):**

- Real PostgreSQL with all 5 schemas applied.
- Real Kafka (Redpanda is fine — wire-protocol compatible, smaller image).
- Test scenarios:
  - Insert 3 rows into `identity.identity_outbox`. Run consumer for 2 poll cycles. Assert: rows have `processed_at IS NOT NULL`; Kafka has 3 messages on `gitscale.identity.events` in `created_at` order.
  - **Crash mid-batch (the test #11 currently doesn't cover):** insert 5 rows; run drain with a producer wrapper that publishes the first 3 then injects ctx cancel. Restart consumer. Assert: Kafka eventually has at most 8 messages (≤ 5 + 3 republished); set of `event_id`s on the broker = original 5; PostgreSQL has all 5 rows with `processed_at IS NOT NULL`. This validates the at-least-once + dedupe story.
  - Two consumer replicas racing on the same domain: insert 100 rows; start two replicas. Assert: each row published exactly once (set of `event_id`s = 100 with no duplicates), no row stuck unprocessed, `outbox_advisory_lock_held` gauge alternates cleanly.
  - Kafka unavailable: stop the broker, insert rows, run consumer for 5s. Assert: `processed_at IS NULL` for all rows, `outbox_oldest_unprocessed_seconds` > 0, no panic. Restart broker. Assert: rows drain.

## 17. Plane boundaries

`plane/data/outbox/` is a data-plane package. Importers:

- `cmd/outbox-consumer/` (the binary) — allowed.
- Application plane services that need to **insert into** outbox tables import the migrations' SQL via `MetadataStore`, not this package. They never call the consumer code.
- Other planes import nothing from here.

The `KafkaProducer` interface is the only seam. The concrete `confluent-kafka-go` impl is wired in `cmd/outbox-consumer/main.go`. Tests use `producer_mock.go`.

## 18. Future work (not blocking #11)

- Outbox row TTL/vacuum job (separate issue — referenced in ADR-008).
- Bisect-on-poison-message (replaces v1's "halt the domain" behavior).
- Schema registry for event payloads (cross-issue with #12).
- Producer-side metrics on the broker side (lag from broker's perspective).
- Multi-region Kafka replication.

## 19. Acceptance criteria (refines issue #11's list)

The issue's acceptance criteria are kept; this spec adds:

- [ ] Spec uses `processed_at IS NULL` / `SET processed_at = now()` (not the issue's `processed = false/true`).
- [ ] `pg_try_advisory_xact_lock` (transaction-scoped), not session-scoped.
- [ ] `ORDER BY created_at, id` — `id` tiebreaker on the SELECT.
- [ ] Drain order: try-lock → SELECT FOR UPDATE SKIP LOCKED → publish → UPDATE → COMMIT.
- [ ] `OUTBOX_PUBLISH_TIMEOUT_MS` env var bounds the txn lifetime.
- [ ] `outbox_oldest_unprocessed_seconds` gauge published per domain.
- [ ] Integration test covers crash-mid-batch.
- [ ] Integration test covers two-replica race.
- [ ] Producer wrapper's partition-key strategy is injected (not hardcoded).
