package billing

import (
	"fmt"

	"go.temporal.io/sdk/workflow"
)

// ArchiveRouterWorkflowName is the registered name for ArchiveRouterWorkflow.
// The schedule (EnsureArchiveSchedule) targets this router; the router
// computes (year, month) at fire time and spawns PartitionArchiveWorkflow
// as a child. This pattern works around ScheduleWorkflowAction's static-args
// limitation while preserving PartitionArchiveWorkflow determinism.
const ArchiveRouterWorkflowName = "billing.ArchiveRouter"

// ArchiveRouterWorkflow computes (year, month) := workflow.Now − 18 months
// and starts PartitionArchiveWorkflow as a child.
//
// Determinism: workflow.Now(ctx) is the canonical replay-safe clock.
// AddDate, Year(), Month() are pure. fmt.Sprintf is pure. No goroutines,
// network calls, or non-deterministic map iteration.
func ArchiveRouterWorkflow(ctx workflow.Context) error {
	now := workflow.Now(ctx)
	target := now.AddDate(0, -18, 0)
	year, month := target.Year(), int(target.Month())

	cwo := workflow.ChildWorkflowOptions{
		WorkflowID: fmt.Sprintf("billing-archive-%04d-%02d", year, month),
	}
	cctx := workflow.WithChildOptions(ctx, cwo)

	var result ArchiveResult
	return workflow.ExecuteChildWorkflow(cctx, PartitionArchiveWorkflow, ArchiveInput{
		RunTime: now,
		Year:    year,
		Month:   month,
	}).Get(ctx, &result)
}
