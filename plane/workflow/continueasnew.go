package workflow

import "go.temporal.io/sdk/workflow"

// continueAsNewHistoryThreshold caps a single workflow run's history at
// 50000 events. Beyond this, replay cost balloons and the Temporal server's
// per-run history-size limit (default 50MB) becomes a real constraint.
// Workflows that legitimately produce many events should call
// workflow.NewContinueAsNewError before crossing this threshold.
const continueAsNewHistoryThreshold = 50_000

// ShouldContinueAsNew reports whether the current workflow run should
// terminate with workflow.NewContinueAsNewError to start a fresh history.
// Long-lived workflows (agent sessions, partition-rollover schedulers)
// MUST call this in their main loop and respect the result.
//
// Callers should pair the check with workflow.IsReplaying(ctx) only if
// they have a reason to suppress the call during replay; the helper itself
// is replay-safe because workflow.GetInfo is deterministic.
func ShouldContinueAsNew(ctx workflow.Context) bool {
	info := workflow.GetInfo(ctx)
	return info.GetCurrentHistoryLength() >= continueAsNewHistoryThreshold
}
