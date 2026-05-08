package billing

import (
	"context"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/client"
)

// ArchiveScheduleID is the stable Temporal schedule ID for the monthly archive.
const ArchiveScheduleID = "billing-partition-archive"

// ArchiveCronExpression fires at 14:00 UTC on the 24th — 2 hours after the
// rollover schedule (12:00 UTC same day), giving rollover time to complete.
const ArchiveCronExpression = "0 14 24 * *"

// EnsureArchiveSchedule registers (or converges) the monthly archive schedule.
// Called by cmd/workflow-worker at boot after the worker is running.
//
// ATTENTION: This helper currently registers the schedule without computing
// (year, month) for the workflow input — the action below has no Args, so the
// workflow would receive ArchiveInput{Year:0, Month:0} and fail validation.
// The cmd-level wiring that supplies real archive deps must also compute
// (year, month) from time.Now() − 18 months and set Args accordingly. The
// workflow itself rejects invalid inputs, so a schedule fire with no args
// surfaces as a workflow error rather than a silent data corruption.
//
// See follow-up issue for full schedule wiring.
func EnsureArchiveSchedule(ctx context.Context, sc gswf.ScheduleClient) (client.ScheduleHandle, error) {
	return gswf.EnsureSchedule(ctx, sc, client.ScheduleOptions{
		ID: ArchiveScheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{ArchiveCronExpression},
			TimeZoneName:    "UTC",
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "billing-partition-archive",
			Workflow:  "PartitionArchiveWorkflow",
			TaskQueue: gswf.QueueBillingMaintenance,
		},
	})
}
