package issuenoise

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// HoldExpiryParams is the input the issuenoise router hands to the
// workflow-plane wrapper. The wrapper translates this into a
// plane/workflow/issuehold.Params and starts the workflow with id
// "issue-hold-{issue_id}" (idempotent on collision).
type HoldExpiryParams struct {
	IssueID uuid.UUID
	RepoID  uuid.UUID
	HoldTTL time.Duration
}

// WorkflowStarter is the swap surface between the issuenoise router
// and the Temporal client. The router never imports the Temporal SDK
// directly (ADR-019); a real impl wraps a temporalclient.Client and
// is wired at the cmd/ boot layer.
//
// All methods are idempotent: calling twice with the same issue_id
// must not start two workflows. This contract is what lets the
// reconciler safely retry post-commit failures.
type WorkflowStarter interface {
	// StartHoldExpiry starts (or no-ops on) the IssueHoldExpiryWorkflow
	// for p.IssueID. Returns nil on idempotent-already-running.
	StartHoldExpiry(ctx context.Context, p HoldExpiryParams) error
	// SignalRelease tells the running IssueHoldExpiryWorkflow that
	// the issue has been manually released; the workflow exits cleanly
	// without auto-closing. Returns nil if no workflow is running
	// (the workflow may have already completed normally).
	SignalRelease(ctx context.Context, issueID uuid.UUID) error
}

// NoopWorkflowStarter is a WorkflowStarter that records nothing and
// returns nil. Used when the router is constructed without a Temporal
// client (e.g. unit tests that don't care about the post-commit
// side-effect).
type NoopWorkflowStarter struct{}

// StartHoldExpiry is a no-op.
func (NoopWorkflowStarter) StartHoldExpiry(_ context.Context, _ HoldExpiryParams) error {
	return nil
}

// SignalRelease is a no-op.
func (NoopWorkflowStarter) SignalRelease(_ context.Context, _ uuid.UUID) error {
	return nil
}

// RecordingWorkflowStarter is a WorkflowStarter that captures every
// call. Used by the router tests to assert idempotency and signaling
// behaviour without a real Temporal cluster.
type RecordingWorkflowStarter struct {
	Started  []HoldExpiryParams
	Released []uuid.UUID
	StartErr error
	SigErr   error
}

// StartHoldExpiry records p.
func (r *RecordingWorkflowStarter) StartHoldExpiry(_ context.Context, p HoldExpiryParams) error {
	if r.StartErr != nil {
		return r.StartErr
	}
	r.Started = append(r.Started, p)
	return nil
}

// SignalRelease records issueID.
func (r *RecordingWorkflowStarter) SignalRelease(_ context.Context, issueID uuid.UUID) error {
	if r.SigErr != nil {
		return r.SigErr
	}
	r.Released = append(r.Released, issueID)
	return nil
}
