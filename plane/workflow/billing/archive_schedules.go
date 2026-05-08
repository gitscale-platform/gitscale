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
// The schedule targets ArchiveRouterWorkflow rather than
// PartitionArchiveWorkflow directly: ScheduleWorkflowAction.Args is statically
// bound at schedule-create time, so the (year, month) := fireTime − 18 months
// computation must happen at fire time. ArchiveRouterWorkflow performs that
// computation deterministically via workflow.Now and starts
// PartitionArchiveWorkflow as a child. See archive_router_workflow.go.
func EnsureArchiveSchedule(ctx context.Context, sc gswf.ScheduleClient) (client.ScheduleHandle, error) {
	return gswf.EnsureSchedule(ctx, sc, client.ScheduleOptions{
		ID: ArchiveScheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{ArchiveCronExpression},
			TimeZoneName:    "UTC",
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "billing-partition-archive",
			Workflow:  "ArchiveRouterWorkflow",
			TaskQueue: gswf.QueueBillingMaintenance,
		},
	})
}
