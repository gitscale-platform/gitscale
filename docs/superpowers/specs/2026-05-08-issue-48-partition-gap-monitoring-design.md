# Spec — Issue #48 usage_events partition-gap monitoring + runbook

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/48
Plane: data
Priority: p1 (calendar-gated; partition gap occurs 2027-05-01)
ADR-impact: none (operational gating; no ADR amendment)

## Problem

`005_billing.sql` seeds 12 monthly partitions of `billing.usage_events`,
ending at `usage_events_2027_04`. From 2027-05-01, INSERTs fall off the end
unless the next partition exists. The CreatePartitionActivity (#18-rollover)
already exists, and the rollover workflow already has determinism + retry
tests. The remaining gap is **operational**:

- No metric tells us whether the next-month partition is present.
- No alert pages oncall if the workflow's cron silently misses a cycle.
- No runbook captures "trigger CreatePartition by hand" for the on-call.

## Goals

1. Add a Prometheus gauge that reports the number of days remaining until the
   first INSERT will fail, surfaced from the data plane.
2. Provide an alerting rule (Prometheus / generic) that pages when the
   next-month partition does not exist by the 20th of the current month.
3. Provide a manual-trigger path for CreatePartition that an oncall engineer
   can invoke without writing Go code (a small CLI under `cmd/`).
4. Land a runbook section documenting the gap date and the recovery steps.

## Non-goals

- Building a generic Postgres health-check exporter (out of scope for #48;
  the metric is purpose-built for this specific gap).
- Modifying the CreatePartitionActivity itself (already in `plane/workflow/billing/`
  and shipped via #18-rollover).
- Building a UI / dashboard. A YAML alert rule + the metric is enough.

## Architecture

### Metric

New file: `plane/data/store/billing/partition_gap_metric.go`.

```go
type PartitionGapMetric struct {
    daysUntilGap *prometheus.GaugeVec // labels: schema, table
    reg          prometheus.Registerer
    pool         *pgxpool.Pool
    clock        func() time.Time
}

func NewPartitionGapMetric(pool *pgxpool.Pool, reg prometheus.Registerer) *PartitionGapMetric

// Refresh recomputes days_until_gap = days_between(today, last_partition_end_date)
// against billing.usage_events. Safe to call on a 60s ticker.
func (m *PartitionGapMetric) Refresh(ctx context.Context) error
```

Implementation: query `pg_inherits` joined against `pg_class` filtered to
`billing.usage_events`'s child partitions; parse the bounds via
`pg_get_expr(c.relpartbound, c.oid)` to extract the upper bound; subtract
from `now()`.

Daemon entrypoint: `cmd/partition-gap-monitor/main.go` — reads
`POSTGRES_DSN`, runs `Refresh` on a ticker, exposes `:9100/metrics`. Graceful
shutdown on SIGTERM. The same pattern as existing observability daemons (mirror
`cmd/outbox-consumer` boot). Optional in production (the workflow is the
primary mitigation); the daemon is the safety-net telemetry.

Metric schema:

```
gitscale_billing_partition_days_until_gap{schema="billing",table="usage_events"}
gitscale_billing_partition_count{schema="billing",table="usage_events"}
```

### Alert rule

New file: `deploy/alerts/billing_partition_gap.yaml`.

```yaml
groups:
- name: billing-partition-gap
  rules:
  - alert: BillingUsageEventsNextPartitionMissing
    expr: gitscale_billing_partition_days_until_gap{schema="billing",table="usage_events"} <= 11
    for: 15m
    labels:
      severity: page
    annotations:
      summary: "billing.usage_events next-month partition missing"
      runbook: "docs/runbooks/billing-partition-gap.md"
      description: |
        Less than 11 days remain before billing.usage_events INSERTs will
        fail. The PartitionRolloverWorkflow runs 7 days before EoM, so this
        alert firing at <=11d means the rollover has missed at least one
        scheduled cycle.

  - alert: BillingUsageEventsPartitionsLow
    expr: gitscale_billing_partition_count{schema="billing",table="usage_events"} < 3
    for: 1h
    labels:
      severity: warn
    annotations:
      summary: "billing.usage_events partition count below 3"
      runbook: "docs/runbooks/billing-partition-gap.md"
```

Threshold rationale: rollover runs 7 days before EoM. Allowing for one
missed cycle (next month's run is implicit), an alert at ≤11 days fires only
if the workflow is not running. The first cycle-miss after the 20th of any
month surfaces as days-remaining ≈ 10 (typical), which trips the alert.

### Manual trigger CLI

New file: `cmd/partition-rollover-trigger/main.go`.

A small CLI that POSTs a one-shot start-workflow request to the workflow
namespace (Temporal client) for `PartitionRolloverWorkflow` with the desired
`(year, month)`. Flags: `--year`, `--month`, `--namespace`, `--addr`. Uses
the existing `temporal.NewClient` configured by `WORKFLOW_*` env vars (mirror
the existing worker). Idempotent because the activity is idempotent.

The runbook documents this CLI as the page-resolution action.

### Runbook

New file: `docs/runbooks/billing-partition-gap.md`.

Sections:

- **What this alert means** — INSERTs into `billing.usage_events` will start
  failing within N days.
- **Hard date** — first failure occurs at 2027-05-01 unless `usage_events_2027_05`
  has been created. Subsequent failures roll forward monthly.
- **Resolution** — run `cmd/partition-rollover-trigger --year=YYYY --month=MM`
  for next month. The activity is idempotent on `(year, month)` so a duplicate
  call after the workflow self-resolves is harmless.
- **Why the workflow may have stalled** — Temporal worker down; namespace
  unreachable; Postgres advisory lock held by orphan session. Diagnostic
  commands inline.
- **Verification** — re-query the gauge: `gitscale_billing_partition_days_until_gap`
  jumps by ~30 once the next partition exists.

### Integration test

New file: `plane/data/store/billing/partition_gap_metric_test.go`
(`//go:build integration`).

Boots a Postgres testcontainer with the migrations through `005_billing.sql`,
calls `Refresh`, asserts the gauge value matches the calendar distance to
`usage_events_2027_04`'s upper bound (≈ days from `now()` to 2027-05-01).

## Test plan

| Layer | Test |
|---|---|
| Unit | Gauge `Refresh` against a fake `pgx.Conn` (mocked rows) |
| Integration | testcontainer PG with the real `005_billing.sql`; gauge reflects calendar |
| Integration | After CREATE TABLE PARTITION attaches a 2027-05 partition, gauge jumps by 30 |
| CI lint | YAML alert rule parses with `promtool check rules` |

## Acceptance checklist

- [ ] `gitscale_billing_partition_days_until_gap` gauge implemented + integration-tested
- [ ] `cmd/partition-gap-monitor` binary builds, exposes `/metrics`
- [ ] `cmd/partition-rollover-trigger` CLI builds, idempotent against the existing workflow
- [ ] `deploy/alerts/billing_partition_gap.yaml` lint-passes `promtool check rules`
- [ ] `docs/runbooks/billing-partition-gap.md` written and cross-linked from the alert rule
- [ ] PR description references the gap date 2027-05-01 and #18-rollover

## Open questions

None.

## References

- `005_billing.sql` partition seed (lines 93–115)
- `plane/workflow/billing/workflow.go` PartitionRolloverWorkflow (#18-rollover)
- `plane/data/store/billing/postgres.go` CreateUsageEventsPartition (idempotent)
- Issue body acceptance criteria
