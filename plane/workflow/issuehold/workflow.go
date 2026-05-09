package issuehold

import (
	"time"

	"github.com/google/uuid"
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// SignalNameRelease is the name of the signal that maintainers send
// (via the application plane) to abort the hold cleanly. Stable
// string — changing it is breaking for in-flight workflows.
const SignalNameRelease = "issue_release"

// ActivityNameAutoCloseIfStillHeld is the registered activity name
// the workflow dispatches by string. The activity impl lives in
// activities.go and calls back into the application plane via gRPC.
const ActivityNameAutoCloseIfStillHeld = "issuehold.AutoCloseIfStillHeld"

// Params is the workflow input. HoldTTL must be > 0.
type Params struct {
	IssueID uuid.UUID
	RepoID  uuid.UUID
	HoldTTL time.Duration
}

// Result reports the terminal state. AutoClosed is true when the
// activity ran (TTL expired with the issue still held); false when
// the workflow exited via the release signal.
type Result struct {
	AutoClosed bool
}

// AutoCloseInput is the activity payload — kept tiny and JSON-stable
// so workflow replay survives any future activity-impl refactor.
type AutoCloseInput struct {
	IssueID uuid.UUID
	RepoID  uuid.UUID
}

// AutoCloseResult reports the activity outcome. AlreadyClosed is true
// when the issue was no longer in held state at activity-execution
// time — a benign race with manual release.
type AutoCloseResult struct {
	AutoClosed    bool
	AlreadyClosed bool
}

// IssueHoldExpiryWorkflow waits for either p.HoldTTL to elapse or a
// release signal to arrive. On TTL expiry it dispatches the activity
// to auto-close the issue (which writes the source row + outbox row
// in one Tx, ADR-008). On release it returns cleanly without
// invoking the activity.
//
// Determinism: only workflow.NewTimer + workflow.GetSignalChannel
// + workflow.ExecuteActivity. No time.Now, no time.Sleep.
func IssueHoldExpiryWorkflow(ctx workflow.Context, p Params) (Result, error) {
	timer := workflow.NewTimer(ctx, p.HoldTTL)
	releaseCh := workflow.GetSignalChannel(ctx, SignalNameRelease)

	sel := workflow.NewSelector(ctx)
	var released bool
	var timerErr error

	sel.AddFuture(timer, func(f workflow.Future) {
		// Timer.Get is required for cancellation propagation.
		timerErr = f.Get(ctx, nil)
	})
	sel.AddReceive(releaseCh, func(c workflow.ReceiveChannel, _ bool) {
		// Drain the signal payload; we don't need it. The signal's
		// arrival is the action.
		var ignored any
		c.Receive(ctx, &ignored)
		released = true
	})

	sel.Select(ctx)

	if released {
		return Result{AutoClosed: false}, nil
	}
	if timerErr != nil {
		// Cancellation or other timer error — surface it so the
		// scheduler can decide whether to retry.
		return Result{}, timerErr
	}

	// TTL expired without release: dispatch the activity.
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 1 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	actx := workflow.WithActivityOptions(ctx, ao)
	var ar AutoCloseResult
	if err := workflow.ExecuteActivity(actx, ActivityNameAutoCloseIfStillHeld,
		AutoCloseInput{IssueID: p.IssueID, RepoID: p.RepoID}).Get(ctx, &ar); err != nil {
		return Result{}, err
	}
	return Result{AutoClosed: ar.AutoClosed}, nil
}

// WorkflowID returns the deterministic workflow id "issue-hold-{uuid}"
// the application plane uses when starting an instance. Idempotency
// is anchored on this id — Temporal rejects duplicate starts.
func WorkflowID(issueID uuid.UUID) string {
	return "issue-hold-" + issueID.String()
}
