// Package approval is the workflow-plane plan-approval activity. It wraps
// appclient.PolicyClient as a Temporal activity so workflows can gate
// state-mutating actions behind the application-plane plan-approval engine
// (ADR-015, ADR-019).
//
// The activity is the only workflow-plane code allowed to interact with
// plan approvals. It MUST NOT import plane/data/store directly: the static
// architecture lint enforces that no Transact*/WriteOutbox*/Insert*/
// Update*/Delete* call is reachable from this package. Every state change
// goes through PolicyClient, which fans out to the application plane and
// writes the source row + outbox row in a single SQL transaction.
//
// Determinism: this package contains an activity, not a workflow, so the
// determinism lint (plane/workflow/lint) does not apply. The activity is
// free to use time.Now, sleep, and context cancellation; workflow callers
// invoke it via workflow.ExecuteActivity which preserves replay
// determinism.
package approval
