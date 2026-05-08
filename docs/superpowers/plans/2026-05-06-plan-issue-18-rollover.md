# Plan: #18-rollover — Billing partition rollover (rollover arm only)

**Date:** 2026-05-06
**Issue:** #18 (split — rollover arm; archive arm tracked under #18-archive)
**Spec:** `2026-05-06-issue-34-billing-archival-tier-spike.md` (archive context only); `2026-05-06-issue-33-workflow-bootstrap-design.md` D6 (Schedule API)
**Branch:** `feat/workflow-billing-partition-rollover`
**Pre-merge of:** #33
**Blocks:** none directly; addresses calendar bomb at 2027-05 partition gap

## Pre-flight

- Confirm #33 merged on main.
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/workflow-billing-partition-rollover`
- Verify `005_billing.sql` exists and `usage_events` is `PARTITION BY RANGE(ts)` with 12 monthly partitions.

## Step sequence

### Step 1 — Workflow definition

File: `plane/workflow/billing/partition_rollover_workflow.go`

```go
func PartitionRolloverWorkflow(ctx workflow.Context, input PartitionRolloverInput) error {
    ao := workflow.ActivityOptions{
        StartToCloseTimeout: 5 * time.Minute,
        RetryPolicy:         workflow.DefaultRetryPolicy(),
    }
    ctx = workflow.WithActivityOptions(ctx, ao)

    target := nextMonthFrom(input.RunTime) // computed deterministically
    return workflow.ExecuteActivity(ctx, CreatePartitionActivity, CreatePartitionInput{
        Year:  target.Year,
        Month: target.Month,
    }).Get(ctx, nil)
}
```

`nextMonthFrom` is a pure function; deterministic. `input.RunTime` comes from the schedule firing.

### Step 2 — `CreatePartition` activity

File: `plane/workflow/billing/create_partition_activity.go`

```go
type CreatePartitionInput struct {
    Year  int
    Month int
}

type CreatePartitionActivity struct {
    store store.MetadataStore // direct import permitted (read-only/DDL exception per ADR-019)
}

func (a *CreatePartitionActivity) Execute(ctx context.Context, in CreatePartitionInput) error {
    return a.store.Transact(ctx, func(tx store.Tx) error {
        // Advisory lock keyed on (table, year, month) to serialize concurrent retries.
        if err := tx.AdvisoryLock(ctx, partitionLockKey(in.Year, in.Month)); err != nil {
            return err
        }

        startDate := fmt.Sprintf("%04d-%02d-01", in.Year, in.Month)
        endDate := nextMonth(startDate)
        tableName := fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month)

        ddl := fmt.Sprintf(`
            CREATE TABLE IF NOT EXISTS %s
            PARTITION OF billing.usage_events
            FOR VALUES FROM ('%s') TO ('%s')
        `, tableName, startDate, endDate)

        return tx.Exec(ctx, ddl)
    })
}
```

`store.Tx.AdvisoryLock` and `store.Tx.Exec` need to be added to the `Tx` interface in #14 — file as a small follow-up issue if not already present.

### Step 3 — Bundle

File: `plane/workflow/billing/bundle.go`

```go
func Bundle(activity *CreatePartitionActivity) workflow.Bundle {
    return workflow.Bundle{
        TaskQueue: workflow.QueueBillingMaintenance,
        Workflows: []any{PartitionRolloverWorkflow},
        Activities: []any{activity.Execute},
    }
}
```

### Step 4 — Wire into worker

File: `cmd/workflow-worker/main.go`

- Construct `CreatePartitionActivity` with the `MetadataStore`.
- Apply `billing.Bundle(...)` on the billing-maintenance worker.

### Step 5 — Temporal Schedule

File: `cmd/workflow-worker/schedules.go` (new)

On worker start (or in a separate `cmd/workflow-scheduler` if reviewer prefers), call:

```go
workflow.EnsureSchedule(ctx, client, workflow.ScheduleSpec{
    ID:           "billing-partition-rollover",
    CronSchedule: "0 12 24 * *", // 12:00 UTC on the 24th of each month — 7 days before EoM
    WorkflowID:   "billing-partition-rollover-{year}-{month}",
    Workflow:     PartitionRolloverWorkflow,
})
```

`EnsureSchedule` is idempotent; safe across worker restarts.

### Step 6 — Tests

File: `plane/workflow/billing/partition_rollover_workflow_test.go`

Uses `testsuite.WorkflowTestSuite` (time-skipping). Verifies:

- Workflow calls `CreatePartitionActivity` with correct year/month.
- `nextMonthFrom` boundary correctness (December → next January, year boundary).

File: `plane/workflow/billing/create_partition_activity_test.go`

Unit test against stub `MetadataStore` — asserts DDL string.

File: `plane/workflow/billing/integration_test.go`

```go
//go:build integration

func TestCreatePartition_idempotent(t *testing.T) {
    // run activity twice with same input → no error, single partition exists
}

func TestCreatePartition_advisory_lock_serializes_retries(t *testing.T) {
    // two goroutines, same year/month → one creates, one is no-op; no error
}

func TestRollover_endToEnd(t *testing.T) {
    // execute workflow with input.RunTime = 2027-04-24
    // assert partition usage_events_2027_05 exists after run
}
```

### Step 7 — Determinism lint check

Verify the workflow file passes `make lint-determinism`. `nextMonthFrom` uses no `time.Now()`; takes `RunTime` from input (deterministic).

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Determinism | `make lint-determinism` | clean |
| Unit | `go test -race ./plane/workflow/billing/...` | pass |
| Integration | `go test -tags integration -race ./plane/workflow/billing/...` | all 3 cases pass |
| Schedule register | manual: start worker, run `tctl schedule list -n gitscale-dev` | shows `billing-partition-rollover` |
| Workflow execution | manual: trigger via tctl with `RunTime=2027-04-24` | partition `usage_events_2027_05` created |

## Acceptance checklist

- [ ] `PartitionRolloverWorkflow` deterministic; passes lint.
- [ ] `CreatePartitionActivity` idempotent under retry (advisory lock + IF NOT EXISTS).
- [ ] Bundle registered on `billing-maintenance` queue.
- [ ] Schedule registered with monthly cadence (24th 12:00 UTC).
- [ ] All test cases green.
- [ ] PR description cross-links spec + ADR-019 (DDL exception clause) + execution plan.
- [ ] PR closes "rollover arm" of #18; #18-archive remains open against #34 ADR.
- [ ] 2027-05 partition gap closed before May 2027.

## Risks

| Risk | Mitigation |
|---|---|
| Schedule fires twice (Temporal restart bug) | activity is idempotent (advisory lock + IF NOT EXISTS); retries are no-op |
| `nextMonthFrom` off-by-one at year boundary | unit test covers Dec→Jan, leap-year Feb |
| `tx.Exec` and `tx.AdvisoryLock` not in `Tx` interface | file follow-up issue against #14; if blocking, add minimal extension here |
| DDL inside Tx blocks concurrent OLTP | `CREATE TABLE … PARTITION OF` takes a brief lock on the parent; runs in seconds for an empty partition; schedule fires at low-traffic 12:00 UTC on the 24th |
| Schedule API not stable in chosen SDK version | tested in #33; version pinned |
| Operator forgets to set `TEMPORAL_NAMESPACE=gitscale-prod` in prod | worker fails fast per #33 D1 |

## Rollback

DDL is purely additive (new partition tables). Drop the partition manually if rolled back; no data loss because workflow runs ahead-of-time, not against existing data.
