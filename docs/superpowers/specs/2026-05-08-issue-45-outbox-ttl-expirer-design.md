# Spec — Issue #45 Outbox row TTL expirer per ADR-008

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/45
Plane: workflow
Priority: p2 (Wave 0)
ADR-impact: conforming (ADR-008 already prescribes 24h post-high-water expiry)

## Problem

ADR-008 specifies "outbox rows TTL-expire 24h after the consumer
high-water mark advances past them." The polling consumer marks rows
`processed_at = now()` after Kafka publish, but no process deletes the
expired rows. Five outbox tables (identity, repositories, collaboration, ci,
billing) grow unbounded. The lag dashboards already track unprocessed rows
but not table bloat — so this turns into hidden tech debt that lands as a
PG vacuum/index-bloat incident.

## Goals

1. A scheduled Temporal workflow that, once per hour, runs five activities
   (one per domain) that DELETE expired outbox rows.
2. Idempotent: re-running the activity is a no-op when nothing is expired.
3. Bounded per-call work: each DELETE caps at `expirer.batch_size` rows
   (default 10k) and re-runs until zero, so a backlogged outbox does not
   produce a single 100M-row DELETE.
4. Observability: per-domain `gitscale_outbox_rows_deleted_total` counter,
   `gitscale_outbox_expirer_duration_seconds` histogram.

## Non-goals

- Modifying the outbox consumer (the consumer is the high-water mark
  source-of-truth; the expirer is downstream of it).
- Reclaiming disk via VACUUM FULL (autovacuum + the partial index does this
  passively).
- Variable TTL per domain (24h is the ADR; configurable but per-cluster, not
  per-domain).

## Architecture

### Workflow

`plane/workflow/outboxttl/` package:

```
PartitionExpiryWorkflow → (parallel) 5x ExpireDomainOutboxActivity
                                 │
                                 ├── identity
                                 ├── repositories
                                 ├── collaboration
                                 ├── ci
                                 └── billing
```

Each activity calls a single domain's `Expirer.Expire(ctx)`. Activities run
in parallel because they touch disjoint tables. Activity timeout: 5 minutes
(generous; a healthy cluster expires <100ms). Retry policy: default
exponential, max 3 attempts.

Workflow runs on a Temporal Schedule with cron `0 * * * *` (top of every
hour). The schedule is defined declaratively next to the existing
`PartitionRolloverWorkflow` schedule.

### Expirer (data plane)

`plane/data/outbox/expirer.go`:

```go
// Expirer deletes processed outbox rows older than the TTL window.
// One Expirer per domain.
type Expirer struct {
    pool      *pgxpool.Pool
    domain    store.Domain
    ttl       time.Duration // default 24h
    batchSize int           // default 10000
    metrics   *ExpirerMetrics
}

// NewExpirer wires an Expirer for d against pool. reg may be nil.
func NewExpirer(pool *pgxpool.Pool, d store.Domain, opts ExpirerOptions) *Expirer

type ExpirerOptions struct {
    TTL       time.Duration   // 0 → 24h
    BatchSize int             // 0 → 10000
    Registry  prometheus.Registerer
}

// Expire deletes processed rows older than TTL in batches of BatchSize
// until zero rows are deleted in a cycle. Returns total rows deleted.
func (e *Expirer) Expire(ctx context.Context) (totalDeleted int64, err error)
```

Implementation per cycle:

```sql
WITH victims AS (
    SELECT id FROM <domain>.<domain>_outbox
    WHERE processed_at IS NOT NULL
      AND processed_at < now() - $1::interval
    ORDER BY id
    LIMIT $2
)
DELETE FROM <domain>.<domain>_outbox
WHERE id IN (SELECT id FROM victims)
RETURNING 1;
```

CTE pattern keeps row order, gets a row count from `RowsAffected`, and stays
non-blocking (no advisory lock; consumer's `SELECT ... FOR UPDATE SKIP
LOCKED` is unaffected because the expirer never targets `processed_at IS
NULL` rows).

### Activity

`plane/workflow/outboxttl/activity.go`:

```go
const ActivityNameExpireDomainOutbox = "outbox.ExpireDomain"

type ExpireDomainOutboxActivity struct {
    expirers map[store.Domain]*outbox.Expirer
}

type ExpireDomainInput struct {
    Domain string // store.Domain string form
}

type ExpireDomainResult struct {
    Domain        string
    RowsDeleted   int64
    DurationMS    int64
}

func NewExpireDomainOutboxActivity(expirers map[store.Domain]*outbox.Expirer) *ExpireDomainOutboxActivity

func (a *ExpireDomainOutboxActivity) Execute(ctx context.Context, in ExpireDomainInput) (ExpireDomainResult, error)
```

Activity is registered alongside CreatePartitionActivity in
`cmd/workflow-worker/main.go` once dependencies are wired (additive; no
existing activity changes).

### Workflow function

`plane/workflow/outboxttl/workflow.go`:

```go
func ExpireOutboxesWorkflow(ctx workflow.Context) (ExpireOutboxesResult, error) {
    domains := []store.Domain{
        store.DomainIdentity, store.DomainRepositories,
        store.DomainCollaboration, store.DomainCI, store.DomainBilling,
    }
    futures := make([]workflow.Future, len(domains))
    for i, d := range domains {
        ao := workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Minute}
        actx := workflow.WithActivityOptions(ctx, ao)
        futures[i] = workflow.ExecuteActivity(actx, ActivityNameExpireDomainOutbox,
            ExpireDomainInput{Domain: string(d)})
    }
    var totals ExpireOutboxesResult
    for i, f := range futures {
        var r ExpireDomainResult
        if err := f.Get(ctx, &r); err != nil {
            // Surface but continue collecting other domains' results.
            totals.Errors = append(totals.Errors, fmt.Sprintf("%s: %v", domains[i], err))
            continue
        }
        totals.PerDomain = append(totals.PerDomain, r)
    }
    if len(totals.Errors) > 0 {
        return totals, fmt.Errorf("expirer: %d domain(s) failed", len(totals.Errors))
    }
    return totals, nil
}
```

Determinism check: only `workflow.ExecuteActivity` and slice iteration of a
fixed `domains` slice — no `time.Now`, no `range map`, no goroutines, no
randomness. Replay-safe.

### Metrics

`plane/data/outbox/expirer_metrics.go`:

```go
type ExpirerMetrics struct {
    rowsDeleted *prometheus.CounterVec   // labels: domain
    duration    *prometheus.HistogramVec // labels: domain
}

func NewExpirerMetrics(reg prometheus.Registerer) *ExpirerMetrics
```

Metric names:

- `gitscale_outbox_rows_deleted_total{domain="…"}`
- `gitscale_outbox_expirer_duration_seconds{domain="…"}`

### Worker wiring

`cmd/workflow-worker/main.go`: build a `map[store.Domain]*outbox.Expirer`
keyed by all five domains (the worker already holds the pgxpool), construct
the activity, register it, and register
`ExpireOutboxesWorkflow`. No schedule registration in this PR — schedule
registration lives in #76's worker bootstrap or a small follow-up. Without
schedule, the workflow can be triggered manually
(`temporal workflow execute -t outbox -w ExpireOutboxesWorkflow`).

### Test plan

| Layer | Test |
|---|---|
| Unit (Expirer) | testcontainer PG; insert rows with mixed `processed_at`; verify only old `processed_at IS NOT NULL` rows deleted |
| Unit (Expirer) | Batch behaviour: insert 25k rows, batch_size=10k; verify 3 cycles, all deleted |
| Unit (Activity) | Activity returns RowsDeleted from underlying Expirer |
| Workflow (testsuite) | Workflow schedules 5 activities; collects results; one failing activity yields workflow error but other results retained |
| Integration | Boot CanaryWorkflow + ExpireOutboxesWorkflow side-by-side via worker; verify no interference |
| Determinism | gitscale-temporal-determinism skill clean |

## Acceptance checklist

- [ ] `outbox.Expirer` + tests
- [ ] `outboxttl.ExpireDomainOutboxActivity` + tests
- [ ] `outboxttl.ExpireOutboxesWorkflow` + tests
- [ ] `gitscale_outbox_rows_deleted_total` and `…_duration_seconds` metrics
- [ ] Worker wires activity + workflow registration
- [ ] PR description references ADR-008

## Open questions

None — defaults baked.

## References

- ADR-008 line 531 in `docs/architecture.md`
- Existing consumer: `plane/data/outbox/consumer.go`
- Existing metrics scaffolding: `plane/data/outbox/metrics.go`
- Domain enum: `plane/data/store/domain.go`
