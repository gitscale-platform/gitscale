package billing

import (
	"context"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/client"
)

// DEKDestructionScheduleID is the stable Temporal schedule ID for the
// monthly DEK destruction run.
const DEKDestructionScheduleID = "billing-dek-destruction"

// DEKDestructionCronExpression fires on the 1st of every month at 02:00
// UTC — well after the rollover (24th 12:00 UTC) and archive (24th 14:00
// UTC) schedules so any cleanup work has settled before crypto-shred runs.
const DEKDestructionCronExpression = "0 2 1 * *"

// EnsureDEKDestructionSchedule registers (or converges) the monthly DEK
// destruction schedule. Called by cmd/workflow-worker at boot once the
// worker is running.
//
// The schedule targets DEKDestructionRouterWorkflow rather than
// DEKDestructionWorkflow directly: ScheduleWorkflowAction.Args is statically
// bound at schedule-create time, so the Cutoff = fireTime − 7y30d
// computation must happen at fire time. The router performs that
// computation deterministically via workflow.Now and starts
// DEKDestructionWorkflow as a child. See dek_destruction_router_workflow.go.
func EnsureDEKDestructionSchedule(ctx context.Context, sc gswf.ScheduleClient) (client.ScheduleHandle, error) {
	return gswf.EnsureSchedule(ctx, sc, client.ScheduleOptions{
		ID: DEKDestructionScheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{DEKDestructionCronExpression},
			TimeZoneName:    "UTC",
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "billing-dek-destruction",
			Workflow:  "DEKDestructionRouterWorkflow",
			TaskQueue: gswf.QueueBillingMaintenance,
		},
	})
}
