package billing

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"
)

// DEKDestructionRouterWorkflowName is the registered name for the router.
//
// The schedule (EnsureDEKDestructionSchedule) targets this router; the
// router computes Cutoff at fire time via workflow.Now and starts
// DEKDestructionWorkflow as a child. This pattern works around
// ScheduleWorkflowAction's static-args limitation while preserving the
// inner workflow's determinism. Mirrors ArchiveRouterWorkflow.
const DEKDestructionRouterWorkflowName = "billing.DEKDestructionRouter"

// DEKDestructionRouterWorkflow computes Cutoff := workflow.Now − 7y30d
// and starts DEKDestructionWorkflow as a child workflow with the bound
// Cutoff and RunTime. Determinism: workflow.Now(ctx) is the canonical
// replay-safe clock; AddDate is pure; fmt.Sprintf is pure. No goroutines,
// network calls, or non-deterministic map iteration.
func DEKDestructionRouterWorkflow(ctx workflow.Context) (DEKDestructionResult, error) {
	now := workflow.Now(ctx)
	cutoff := now.AddDate(0, 0, -DEKDestructionRetentionDays)

	cwo := workflow.ChildWorkflowOptions{
		WorkflowID: fmt.Sprintf("billing-dek-destruction-%s", now.UTC().Format(time.RFC3339)),
	}
	cctx := workflow.WithChildOptions(ctx, cwo)

	var result DEKDestructionResult
	if err := workflow.ExecuteChildWorkflow(cctx, DEKDestructionWorkflow, DEKDestructionInput{
		RunTime: now,
		Cutoff:  cutoff,
	}).Get(ctx, &result); err != nil {
		return DEKDestructionResult{}, err
	}
	return result, nil
}
