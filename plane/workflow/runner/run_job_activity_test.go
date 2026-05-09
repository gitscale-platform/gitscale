package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
	"github.com/gitscale-platform/gitscale/plane/workflow/runner/runnertest"
)

func TestRunJobActivity_Happy(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	a, err := runner.NewRunJobActivity(fake)
	if err != nil {
		t.Fatalf("NewRunJobActivity: %v", err)
	}
	r, err := a.Execute(context.Background(), runner.RunInput{
		VMID: "vm-1", Command: []string{"/bin/true"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.ExitCode != 0 {
		t.Errorf("expected exit 0, got %d", r.ExitCode)
	}
	if r.LogsObjectURI == "" {
		t.Error("expected non-empty LogsObjectURI")
	}
}

func TestRunJobActivity_VMLost_Surfaces(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	fake.SetRun(func(_ runner.RunInput) (runner.JobResult, error) {
		return runner.JobResult{}, runner.ErrVMLost
	})
	a, err := runner.NewRunJobActivity(fake)
	if err != nil {
		t.Fatalf("NewRunJobActivity: %v", err)
	}
	_, err = a.Execute(context.Background(), runner.RunInput{VMID: "vm-1"})
	if !errors.Is(err, runner.ErrVMLost) {
		t.Fatalf("expected ErrVMLost wrapped, got: %v", err)
	}
}

func TestRunJobActivity_NilProvisioner(t *testing.T) {
	t.Parallel()
	if _, err := runner.NewRunJobActivity(nil); err == nil {
		t.Error("expected error for nil provisioner")
	}
}
