package issuehold

import (
	"context"
	"errors"
)

// ErrNoCloser indicates the activity was constructed without a back-
// channel into the application plane.
var ErrNoCloser = errors.New("issuehold: nil HeldIssueCloser")

// HeldIssueCloser is the back-channel into the application plane.
// A real impl wraps a gRPC client into the application plane's
// issuenoise service (ADR-019: workflow plane never touches the
// metadata DB directly). Tests inject a fake.
//
// AutoCloseIfStillHeld atomically reads the current issue state and,
// if still held, transitions it to closed (status='closed') with
// a "expired_in_review_queue" reason — writing the source row +
// outbox row in one Tx. Returns AlreadyClosed=true if the issue is
// already closed/open (a benign race with manual release).
type HeldIssueCloser interface {
	AutoCloseIfStillHeld(ctx context.Context, in AutoCloseInput) (AutoCloseResult, error)
}

// AutoCloseActivity wraps a HeldIssueCloser. One instance is
// registered with the worker under ActivityNameAutoCloseIfStillHeld.
type AutoCloseActivity struct {
	closer HeldIssueCloser
}

// NewAutoCloseActivity constructs an activity over closer.
func NewAutoCloseActivity(closer HeldIssueCloser) *AutoCloseActivity {
	return &AutoCloseActivity{closer: closer}
}

// Execute is the activity entrypoint. Errors surface to Temporal and
// trigger the configured retry policy.
func (a *AutoCloseActivity) Execute(ctx context.Context, in AutoCloseInput) (AutoCloseResult, error) {
	if a.closer == nil {
		return AutoCloseResult{}, ErrNoCloser
	}
	return a.closer.AutoCloseIfStillHeld(ctx, in)
}
