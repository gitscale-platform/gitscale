# Runbook: billing.usage_events partition gap

## What this alert means

`billing.usage_events` is range-partitioned by month. INSERTs require a
partition that covers the row's `ts`. If the next-month partition does not
exist, INSERTs fail with `no partition of relation "usage_events" found
for row`.

## Hard date

The original seed in `005_billing.sql` covers 2026-05 through 2027-04. The
first failure date — absent the rollover workflow — is **2027-05-01**.
Subsequent failure dates roll forward monthly.

## Resolution

1. Identify the missing month. The page links to the gauge
   `gitscale_billing_partition_days_until_gap`. Subtract the gauge value
   from today to find the first uncovered date; the missing month is the
   month of that date.
2. Run:

   ```bash
   cmd/partition-rollover-trigger --year=YYYY --month=MM \
       --addr=$TEMPORAL_ADDR --namespace=$TEMPORAL_NAMESPACE
   ```

   The CreatePartition activity is idempotent — a duplicate run is harmless
   if the workflow self-resolves between page and execution.
3. Confirm `gitscale_billing_partition_days_until_gap` jumps by ~30.

## Why the workflow may have stalled

- Temporal worker pod is down → `kubectl get pods -n workflow`.
- Temporal namespace unreachable → `tctl --address $TEMPORAL_ADDR namespace list`.
- Partitioner advisory lock held by orphan session →
  `SELECT * FROM pg_locks WHERE locktype='advisory' AND granted='t';`

## Verification

- Re-scrape `partition-gap-monitor`: `curl :9100/metrics | grep partition_days`.
- New partition exists in Postgres:

  ```sql
  SELECT relname FROM pg_class
  WHERE relname LIKE 'usage_events_%' ORDER BY relname DESC LIMIT 3;
  ```

## Escalation

If `cmd/partition-rollover-trigger` fails repeatedly, page the data plane
on-call. Manual fallback: writing a row into `billing.partition_archives`
is **not** a substitute — only `CREATE TABLE billing.usage_events_YYYY_MM
PARTITION OF ...` (which the activity issues) resolves the gap.

## References

- Spec: `docs/superpowers/specs/2026-05-08-issue-48-partition-gap-monitoring-design.md`
- Issue: gitscale-platform/gitscale#48
- Workflow: `plane/workflow/billing/workflow.go` (`PartitionRolloverWorkflow`, #18-rollover)
- Activity: `plane/data/store/billing/postgres.go` (`CreateUsageEventsPartition`, idempotent)
- Alert rule: `deploy/alerts/billing_partition_gap.yaml`
