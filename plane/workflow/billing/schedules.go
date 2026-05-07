package billing

import (
	"context"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/client"
)

// ScheduleID is the stable identifier used for the Temporal schedule
// registered by EnsureRolloverSchedule. Stable across worker restarts so
// EnsureSchedule can converge on update.
const ScheduleID = "billing-partition-rollover"

// CronExpression fires the rollover workflow at 12:00 UTC on the 24th of
// every month — 7 days before the earliest possible end-of-month, leaving
// slack for retries before the calendar bomb at 2027-05.
const CronExpression = "0 12 24 * *"

// EnsureRolloverSchedule registers (or converges) the monthly partition
// rollover schedule. Called by cmd/workflow-worker at boot once the worker
// is running.
//
// The Schedule passes its own scheduled-time as RunTime to the workflow so
// determinism holds across replays.
func EnsureRolloverSchedule(ctx context.Context, sc gswf.ScheduleClient) (client.ScheduleHandle, error) {
	return gswf.EnsureSchedule(ctx, sc, client.ScheduleOptions{
		ID: ScheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{CronExpression},
			TimeZoneName:    "UTC",
		},
		Action: &client.ScheduleWorkflowAction{
			// Temporal appends the scheduled-time to this ID prefix so the
			// per-fire workflow ID is unique without app-level templating.
			ID:        "billing-partition-rollover",
			Workflow:  "PartitionRolloverWorkflow",
			TaskQueue: gswf.QueueBillingMaintenance,
		},
	})
}
