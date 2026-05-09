package issuehold_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/workflow/issuehold"
	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// fakeCloser captures activity invocations.
type fakeCloser struct {
	calls  int
	result issuehold.AutoCloseResult
	err    error
}

func (f *fakeCloser) AutoCloseIfStillHeld(_ context.Context, _ issuehold.AutoCloseInput) (issuehold.AutoCloseResult, error) {
	f.calls++
	return f.result, f.err
}

func TestIssueHoldExpiryWorkflow_AutoClosesOnTTL(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	closer := &fakeCloser{result: issuehold.AutoCloseResult{AutoClosed: true}}
	act := issuehold.NewAutoCloseActivity(closer)
	env.RegisterActivityWithOptions(act.Execute, activity.RegisterOptions{Name: issuehold.ActivityNameAutoCloseIfStillHeld})
	env.RegisterWorkflow(issuehold.IssueHoldExpiryWorkflow)

	env.ExecuteWorkflow(issuehold.IssueHoldExpiryWorkflow, issuehold.Params{
		IssueID: uuid.New(),
		RepoID:  uuid.New(),
		HoldTTL: 14 * 24 * time.Hour,
	})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var res issuehold.Result
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !res.AutoClosed {
		t.Errorf("AutoClosed=false, want true")
	}
	if closer.calls != 1 {
		t.Errorf("closer.calls=%d want 1", closer.calls)
	}
}

func TestIssueHoldExpiryWorkflow_ReleaseSignalSkipsClose(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	closer := &fakeCloser{}
	act := issuehold.NewAutoCloseActivity(closer)
	env.RegisterActivityWithOptions(act.Execute, activity.RegisterOptions{Name: issuehold.ActivityNameAutoCloseIfStillHeld})
	env.RegisterWorkflow(issuehold.IssueHoldExpiryWorkflow)

	// Send the release signal at virtual t=1s, well before the 14d TTL.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(issuehold.SignalNameRelease, "released")
	}, time.Second)

	env.ExecuteWorkflow(issuehold.IssueHoldExpiryWorkflow, issuehold.Params{
		IssueID: uuid.New(),
		RepoID:  uuid.New(),
		HoldTTL: 14 * 24 * time.Hour,
	})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var res issuehold.Result
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if res.AutoClosed {
		t.Errorf("AutoClosed=true, want false on release path")
	}
	if closer.calls != 0 {
		t.Errorf("closer must not be called on release path; got %d calls", closer.calls)
	}
}

func TestWorkflowID_Stable(t *testing.T) {
	id := uuid.New()
	got := issuehold.WorkflowID(id)
	want := "issue-hold-" + id.String()
	if got != want {
		t.Fatalf("WorkflowID=%q want %q", got, want)
	}
}

func TestNewAutoCloseActivity_NilCloserErrors(t *testing.T) {
	act := issuehold.NewAutoCloseActivity(nil)
	if _, err := act.Execute(context.Background(), issuehold.AutoCloseInput{}); err == nil {
		t.Fatal("expected ErrNoCloser")
	}
}
