// Package issuehold hosts the IssueHoldExpiryWorkflow — a Temporal
// workflow that owns the TTL on a held issue. Started post-commit by
// plane/application/issuenoise.Router on a held verdict; on expiry it
// invokes the AutoCloseIfStillHeld activity which calls back into the
// application plane via gRPC (ADR-019: workflow plane never touches
// the metadata DB directly).
//
// Determinism (ADR-003):
//
//   - The workflow function uses workflow.NewTimer + workflow.GetSignalChannel
//     for the wait. No time.Sleep, no time.Now, no goroutines.
//   - The workflow ID is "issue-hold-{issue_id}" so duplicate starts
//     are idempotent (Temporal rejects the duplicate at submission).
//   - Activities are registered under stable string names so workflow
//     replays do not bind to a specific Go closure.
//
// Reconciler (out of scope for this package): a workflow-plane
// scheduled task lists held issues without a running workflow every
// 5 minutes and starts one. That sweep is owned by the wider
// observability stack; this package exposes only the workflow itself.
//
// References:
//   - Issue: https://github.com/gitscale-platform/gitscale/issues/117
//   - ADR-003 (workflow determinism), ADR-008 (outbox), ADR-019 (plane boundary).
package issuehold
