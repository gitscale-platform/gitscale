package billing

import (
	"time"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// PartitionRolloverInput is the input to PartitionRolloverWorkflow.
// RunTime is the deterministic anchor — the schedule passes its scheduled
// time as the anchor, not workflow.Now (which is also deterministic but
// would couple the next-month math to schedule-fire latency). Spec D11.
type PartitionRolloverInput struct {
	// RunTime is the time the rollover is computed against. Production
	// callers pass the schedule's scheduled-time. Test callers pass a
	// fixed time so assertions are stable.
	RunTime time.Time
}

// PartitionRolloverWorkflow ensures the next calendar month's partition on
// billing.usage_events exists. Idempotent — re-runs are safe because the
// activity's CREATE TABLE IF NOT EXISTS is itself idempotent.
//
// Execution model: a single activity call with the spec D9 default retry
// policy (5 attempts, exp backoff). The workflow body is purely
// deterministic — nextMonthFrom is a pure function of input.
func PartitionRolloverWorkflow(ctx workflow.Context, input PartitionRolloverInput) (CreatePartitionResult, error) {
	year, month := nextMonthFrom(input.RunTime)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var result CreatePartitionResult
	err := workflow.ExecuteActivity(ctx, ActivityNameCreatePartition, CreatePartitionInput{
		Year:  year,
		Month: month,
	}).Get(ctx, &result)
	return result, err
}

// nextMonthFrom returns the (year, month) of the calendar month immediately
// after the calendar month containing t. Pure; year-boundary safe.
func nextMonthFrom(t time.Time) (int, int) {
	t = t.UTC()
	year, month := t.Year(), int(t.Month())
	month++
	if month > 12 {
		month = 1
		year++
	}
	return year, month
}
