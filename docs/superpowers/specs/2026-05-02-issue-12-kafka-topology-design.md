# Design: Kafka topic topology + partition strategy (Issue #12)

**Status:** Draft for review
**Date:** 2026-05-02
**Owner:** Data plane
**ADRs:** ADR-004 (Kafka event bus), ADR-008 (outbox publishes here)
**GitHub issue:** [#12](https://github.com/gitscale-platform/gitscale/issues/12)
**Related spec:** [Issue #11 — polling outbox consumer](./2026-05-02-issue-11-outbox-consumer-design.md)

## 1. Summary

Defines the Kafka topology consumed by the polling outbox consumer (#11):

- 5 main topics (one per domain) + 5 dead-letter topics
- Partition counts, replication factor, retention, cleanup policy
- The event envelope and per-event-type JSON Schema contract
- Consumer group name registry
- Apply tooling (Terraform for prod/staging; small Go CLI for local `make dev-up`)
- Topic versioning policy

## 2. Decisions locked

| ID | Decision | Rationale |
|---|---|---|
| D1 | **Partition key = `aggregate_id` (UUID)** for every topic | Avoids hot-partition risk under agent traffic; ADR-004 to be amended (see §11) |
| D2 | **JSON Schema in repo** (no schema registry); CI-lints fixtures; optional runtime validation | Keeps `JSONB` outbox column; cross-language consumers pick up schemas from repo |
| D3 | **Per-topic DLQ** at `gitscale.<domain>.events.dlq`, 1 partition, 30d retention | Cheap insurance for consumer-side poison-message handling |
| D4 | **In-place backwards-compatible evolution**; breaking changes get `…events.v2` + dual-publisher window | Standard CI lint enforces compat |
| D5 | `cleanup.policy=delete` on all topics | Append-only event streams; not latest-state-of-aggregate |
| D6 | Terraform Kafka provider for prod/staging; local Go CLI for `make dev-up`. Both consume `topics.yaml`. | Single source of truth |
| D7 | `auto.offset.reset=earliest` default for all consumer groups | Late-binding consumers backfill, not skip |
| D8 | **Multi-region/DR out of scope** | Tracked in future-work section |

## 3. Scope

In:
- Topic + DLQ definitions (`topics.yaml`)
- Go constants for topic + consumer-group names
- `EventEnvelope` Go struct + matching JSON Schema for the envelope itself
- Per-event-type JSON Schema directory layout + CI lint contract
- Apply tooling: Terraform module + `cmd/kafka-topology-apply` CLI
- Per-topic partition count rationale documented inline

Out:
- Concrete per-event-type schemas (filed alongside the producing domain service issues)
- Consumer-side configuration beyond the offset-reset default (per-consumer issues)
- Multi-region replication / MirrorMaker / Cluster Linking (future)
- Producer configuration (lives in #11)

## 4. Package layout

```
plane/data/kafka/
  topics.go              # Go constants for topic names
  topics.yaml            # Source of truth: partition counts, retention, configs
  consumer_groups.go     # Consumer group name constants + topic linkage comments
  envelope.go            # EventEnvelope struct + (de)serialization helpers
  envelope.schema.json   # JSON Schema for the envelope
  topology.go            # Reads topics.yaml; used by Terraform data source + local CLI
  topology_test.go       # Validates yaml structure + invariants

plane/data/events/
  identity/
    user.created.schema.json
    user.created.testdata/sample.json
    org.created.schema.json
    …
  repositories/
    repo.created.schema.json
    repo.archived.schema.json
    …
  collaboration/
    pr.opened.schema.json
    pr.reviewed.schema.json
    issue.commented.schema.json
    …
  ci/
    pipeline.started.schema.json
    job.transitioned.schema.json
    …
  billing/
    usage.recorded.schema.json
    …

deploy/terraform/kafka/
  main.tf                # for_each over topics.yaml
  variables.tf

cmd/kafka-topology-apply/
  main.go                # Local apply tool for make dev-up
```

> **#11 dependency:** The outbox consumer imports `topics.go` for topic-name constants and `envelope.go` for the envelope type. Nothing else.

## 5. Topic definitions

`plane/data/kafka/topics.yaml` is the single source of truth:

```yaml
defaults:
  replication_factor: 3
  configs:
    cleanup.policy: delete
    min.insync.replicas: 2
    compression.type: zstd

topics:
  - name: gitscale.identity.events
    partitions: 12
    retention_ms: 604800000          # 7 days
    rationale: "User/org/agent mutations. Low volume."

  - name: gitscale.identity.events.dlq
    partitions: 1
    retention_ms: 2592000000         # 30 days
    rationale: "Poison messages from identity consumers."

  - name: gitscale.repositories.events
    partitions: 24
    retention_ms: 604800000
    rationale: "Repo metadata mutations. Push events drive ~½ collaboration volume."

  - name: gitscale.repositories.events.dlq
    partitions: 1
    retention_ms: 2592000000

  - name: gitscale.collaboration.events
    partitions: 48
    retention_ms: 604800000
    rationale: "PR/issue/comment events. Highest write volume — agent-driven."

  - name: gitscale.collaboration.events.dlq
    partitions: 1
    retention_ms: 2592000000

  - name: gitscale.ci.events
    partitions: 24
    retention_ms: 604800000
    rationale: "Workflow + per-job state transitions. ~20 events per pipeline."

  - name: gitscale.ci.events.dlq
    partitions: 1
    retention_ms: 2592000000

  - name: gitscale.billing.events
    partitions: 12
    retention_ms: 2592000000         # 30 days — reconciliation window
    rationale: "Usage events trail every metered op. Longer retention for billing reconciliation."

  - name: gitscale.billing.events.dlq
    partitions: 1
    retention_ms: 2592000000
```

### Partition count rationale (back-of-envelope)

Numbers are estimates for May 2027 production scale; revise as we measure.

| Topic | Drivers | Peak est. (events/sec) | Partitions | Per-partition load @ avg 1KB |
|---|---|---|---|---|
| identity | User/org/agent mutations | ~100 | 12 | ~10/sec — generous headroom |
| repositories | Push events; outbox-emitted on metadata change | ~8,000 | 24 | ~330/sec ≈ 330 KB/s |
| collaboration | Agent PR/issue/comment storms; ~100 events/min/session × 1K active sessions × 10× burst | ~16,000 | 48 | ~330/sec |
| ci | Workflow + job transitions; ~20 events/pipeline × 1K pipelines/min × burst | ~5,000 | 24 | ~210/sec |
| billing | Usage events, well-bounded by metering rules | ~3,000 | 12 | ~250/sec |

All counts are powers-of-2 multiples of 12 to allow clean rebalancing if we ever raise partition counts.

> **Repartitioning is not free.** Increasing partition count later breaks `aggregate_id` → partition mapping for existing keys; events for an aggregate may split across old + new partitions, breaking per-aggregate ordering during the cutover. Plan to size right, not to grow elastically.

## 6. Event envelope

### Go type

```go
// plane/data/kafka/envelope.go

type EventEnvelope struct {
    EventID       string          `json:"event_id"`        // UUID — outbox.event_id
    AggregateType string          `json:"aggregate_type"`  // e.g. "pull_request"
    AggregateID   string          `json:"aggregate_id"`    // UUID
    EventType     string          `json:"event_type"`      // e.g. "pr.opened"
    SchemaVersion string          `json:"schema_version"`  // e.g. "v1" — payload schema version, NOT topic version
    Payload       json.RawMessage `json:"payload"`
    PublishedAt   time.Time       `json:"published_at"`    // RFC3339, UTC
}
```

### Envelope JSON Schema (`envelope.schema.json`)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://gitscale.dev/schemas/kafka/envelope.json",
  "type": "object",
  "required": ["event_id", "aggregate_type", "aggregate_id", "event_type", "schema_version", "payload", "published_at"],
  "properties": {
    "event_id": { "type": "string", "format": "uuid" },
    "aggregate_type": { "type": "string", "minLength": 1 },
    "aggregate_id": { "type": "string", "format": "uuid" },
    "event_type": { "type": "string", "pattern": "^[a-z_]+\\.[a-z_]+$" },
    "schema_version": { "type": "string", "pattern": "^v[0-9]+$" },
    "payload": { "type": "object" },
    "published_at": { "type": "string", "format": "date-time" }
  },
  "additionalProperties": false
}
```

`schema_version` is **per-event-type payload version**, independent of the topic name. Most events stay at `v1` forever (backwards-compatible additions don't bump it). It bumps to `v2` only when a payload field is removed or its type changes — at which point per the versioning policy (§9) the topic itself rolls to `…events.v2`.

## 7. Per-event-type JSON Schema contract

### Directory layout

`plane/data/events/<domain>/<event_type>.schema.json` — one file per `event_type` value emitted by the domain.

`plane/data/events/<domain>/<event_type>.testdata/` — directory of JSON sample files. Every committed schema must have at least one fixture.

### CI lint (`make lint-events`)

A new lint target that:

1. Validates each `*.schema.json` is itself valid JSON Schema (draft 2020-12).
2. Validates every fixture in `testdata/` against its sibling schema. Fail = bad sample or bad schema; commit blocks.
3. **Backwards-compat check** for changed schemas: if a schema file was modified vs `main`, it must satisfy "is-superset-of-old" — only `additionalProperties:false` relaxations, only added optional fields, no removed/renamed fields, no narrowed types. Implementation = `json-schema-diff-validator` or equivalent.
4. Validates that every `event_type` referenced in any Go code (string literals matching the regex pattern) has a corresponding schema file. Linter walks `plane/**/*.go` for `event_type:` and `EventType:` literal usage.

### Go codegen (optional, decoupled)

For producer ergonomics: `make gen-events` runs `go-jsonschema` to produce `plane/data/events/<domain>/types.gen.go` with strongly-typed structs. Producers can use the typed structs and marshal to JSON; the resulting bytes go into the outbox row's `payload` column.

Codegen is **not** required to use a schema. Schemas are consumer-readable on their own. Codegen is a Go-side convenience.

### Producer-side runtime validation

`Off in prod, on in tests.` Reasoning:

- In prod, the producer is the outbox consumer (#11) running on serialized rows that came from the application plane. The application plane is the entity that needs to validate before INSERT — a wrapper helper in `plane/data/outbox/insert.go` (separate from this issue) can do that.
- In tests, full validation gives us the fast-fail signal we want.
- Hot-path validation in #11 would add cost to every drained event for no marginal safety vs the application-plane gate.

## 8. Consumer group registry

```go
// plane/data/kafka/consumer_groups.go

const (
    // SearchIndexer consumes ALL 5 main topics. Indexes into Vespa (ADR-016).
    GroupSearchIndexer = "gitscale.search-indexer"

    // AuditLog consumes ALL 5 main topics. Writes immutable audit records to ClickHouse.
    GroupAuditLog = "gitscale.audit-log"

    // WebhookFanout consumes repositories.events + collaboration.events + ci.events.
    // Fans out to customer-configured webhook endpoints.
    GroupWebhookFanout = "gitscale.webhook-fanout"

    // BillingAggregator consumes billing.events.
    // Aggregates usage events into customer invoice line items.
    GroupBillingAggregator = "gitscale.billing-aggregator"

    // ColdStorageMigrator consumes repositories.events to learn which repos
    // have crossed the hot→cold boundary (last_active_at > 30d). Triggers
    // erasure-coding migration jobs in the workflow plane.
    GroupColdStorageMigrator = "gitscale.cold-storage-migrator"
)
```

Per-consumer Kafka client config (session timeout, max poll records, isolation level) lives with the consumer's own issue, not here. Default that does live here:

```go
// auto.offset.reset for all groups — late binding must backfill, not skip
const DefaultAutoOffsetReset = "earliest"
```

## 9. Topic versioning policy

**In-place backwards-compatible evolution.** A change to `<event_type>.schema.json` that is a superset (per §7's CI check) ships in-place. Consumers that haven't been updated keep working; new fields are unread.

**Breaking changes** (field removed, renamed, narrowed):

1. Bump the payload's `schema_version` field in the envelope (`v1` → `v2`).
2. Roll the topic: introduce `gitscale.<domain>.events.v2` with the same partition count, retention, configs.
3. **Dual-publish window:** the outbox consumer publishes the same logical event to both `…events` and `…events.v2`. Mechanism:
   - Outbox row carries the v2-shape payload only — domain services emit v2 directly.
   - A per-event-type **downgrade function** (`func(v2 json.RawMessage) (v1 json.RawMessage, err error)`) is registered in `plane/data/events/<domain>/downgrades.go` alongside the schema bump.
   - Outbox consumer, when publishing an event whose `event_type` has an active v1↔v2 downgrade registered, publishes the v2 payload to `…events.v2` and the downgraded v1 payload to `…events`, both inside the same drain-cycle txn (per #11 spec §7).
   - Both publishes must succeed for the row's `processed_at` to be set; failure on either rolls the txn back.
   - The downgrade function is removed once the v1 topic is decommissioned (§9 step 5).
   This keeps consumer sides simple — every consumer reads exactly one topic.
4. Consumer migration: each consumer group is repointed to the v2 topic at its own pace. Subscriptions on the v1 topic are observable via `consumer-groups.sh --describe` — when zero remain, the v1 topic is decommissioned.
5. Decommission: 30 days after the last consumer migrates, delete v1.

This policy is documented in `plane/data/kafka/topics.yaml`'s top-level comment.

## 10. Apply tooling

### Terraform module (`deploy/terraform/kafka/`)

```hcl
locals {
  topology = yamldecode(file("${path.module}/../../../plane/data/kafka/topics.yaml"))
}

resource "kafka_topic" "topics" {
  for_each           = { for t in local.topology.topics : t.name => t }
  name               = each.value.name
  partitions         = each.value.partitions
  replication_factor = local.topology.defaults.replication_factor
  config = merge(
    local.topology.defaults.configs,
    { "retention.ms" = tostring(each.value.retention_ms) }
  )
}
```

### Local apply tool (`cmd/kafka-topology-apply/`)

A small Go binary that:

1. Reads `plane/data/kafka/topics.yaml`.
2. Connects to a Kafka broker (env var `KAFKA_BOOTSTRAP_SERVERS`).
3. For each topic in the yaml: `CREATE` if missing, `ALTER` config + partitions if drift detected. Never deletes (safety).
4. Used by `make dev-up` (against local Redpanda) and as a sanity check in CI.

The CLI ensures parity between Terraform-applied prod and locally-applied dev environments — same yaml drives both.

## 11. ADR-004 amendment required (cross-cutting)

ADR-004 currently says: *"Per-partition ordering is preserved on `repo_id` / `org_id` partition keys."*

This contradicts D1. Per `CLAUDE.md`, the repo policy is to **flag the conflict and open a `type/adr` issue before merging code that contradicts**. This spec depends on the amendment landing.

**Action:** open a `type/adr` issue titled *"Amend ADR-004: partition key is `aggregate_id`, not `repo_id`/`org_id`"* with the amended decision text:

> *Decision (amended): Kafka is the canonical event bus. Per-partition ordering is preserved per aggregate, keyed on `aggregate_id` (UUID) for every topic. Repo-level or org-level ordering is not guaranteed; consumers requiring it must reorder by `(aggregate_id, published_at)` after consumption. The previous `repo_id`/`org_id` keying was rejected to avoid hot-partition risk under agent traffic.*

This issue is a hard prerequisite for #12 merging.

## 12. Plane boundaries

`plane/data/kafka/` is consumed by:
- The outbox producer (#11) — imports topic constants + envelope type.
- Consumer-side code in any plane that subscribes — imports consumer-group constants + envelope type.

`plane/data/events/<domain>/` — schemas are pure data, importable by any plane for validation or codegen. Generated `types.gen.go` files are imported by domain services for ergonomics.

No package under `plane/data/kafka/` reaches into other plane packages.

## 13. Failure modes (topology level)

| Scenario | Outcome | Mitigation |
|---|---|---|
| Topic missing on broker | Producer errors on first publish | `cmd/kafka-topology-apply` runs in CI-deploy step before any service starts |
| Wrong partition count after manual broker change | Per-aggregate ordering breaks for events whose `aggregate_id` hashes differently | Topology drift alert: a periodic CI job reads broker metadata, compares to `topics.yaml`, alerts on drift |
| Schema mismatch — consumer reads field that was removed | Deserialization error on consumer | Backwards-compat lint blocks the PR. Breaking change forces v2 topic + dual-publish per §9 |
| DLQ topic missing | Consumer DLQ-publish fails; events stuck in retry loop | Same as topic missing — caught by topology apply |
| Replication factor unmet (cluster has < 3 brokers) | `kafka-topic-apply` fails fast | Documented prerequisite: 3+ broker cluster |

## 14. Future work (not blocking #12)

- Multi-region replication (MirrorMaker 2 / Confluent Cluster Linking)
- Schema Registry (S2) migration when a non-Go consumer materializes
- Bisect-on-poison-message for #11 producer side, paired with the DLQ topics defined here
- Auto-validation of envelope on consumer side via shared SDK
- Capacity dashboard from `topics.yaml` partition counts vs measured per-partition throughput

## 15. Acceptance criteria (refines issue #12's list)

The issue's acceptance criteria are kept; this spec adds:

- [ ] `topics.yaml` is the single source of truth, consumed by both Terraform and `cmd/kafka-topology-apply`.
- [ ] DLQ topic for each main topic, 1 partition, 30d retention.
- [ ] `EventEnvelope` includes a `schema_version` field.
- [ ] `envelope.schema.json` committed alongside `envelope.go`.
- [ ] `plane/data/events/<domain>/` directory created (initially empty schema files filed under each domain's own service issue).
- [ ] `make lint-events` target validates: schema-self-validity, fixture-against-schema, backwards-compat-on-modify, schema-exists-for-every-string-literal-event-type.
- [ ] `lint-events` step added to CI; **its config (rules, allowlists, ignore patterns) committed in the same PR** — per the `CLAUDE.md` CI linter rule.
- [ ] Default `auto.offset.reset=earliest` constant.
- [ ] Comments in `consumer_groups.go` document each group's topic subscription.
- [ ] Versioning policy section in `topics.yaml` header comment.
- [ ] **Cross-cutting prerequisite:** `type/adr` issue opened to amend ADR-004's partition-key wording. Spec merge blocked until the amendment is accepted.
