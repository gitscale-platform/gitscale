package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
	"github.com/gitscale-platform/gitscale/plane/workflow/runner/runnertest"
)

func TestTeardownVMActivity_Idempotent_DoubleTeardown(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	a, err := runner.NewTeardownVMActivity(fake)
	if err != nil {
		t.Fatalf("NewTeardownVMActivity: %v", err)
	}
	// Pre-allocate a VM through the fake's BootCold so liveVMs registers it.
	h, _ := fake.BootCold(context.Background(), runner.BootInput{})
	if err := a.Execute(context.Background(), h.ID); err != nil {
		t.Fatalf("first teardown: %v", err)
	}
	// Second call must also succeed (ErrAlreadyTorndown swallowed).
	if err := a.Execute(context.Background(), h.ID); err != nil {
		t.Fatalf("second teardown should be idempotent success, got: %v", err)
	}
	// Third call too.
	if err := a.Execute(context.Background(), h.ID); err != nil {
		t.Fatalf("third teardown should remain idempotent: %v", err)
	}
}

func TestTeardownVMActivity_UnknownVMID(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	a, err := runner.NewTeardownVMActivity(fake)
	if err != nil {
		t.Fatalf("NewTeardownVMActivity: %v", err)
	}
	if err := a.Execute(context.Background(), "vm-never-existed"); err != nil {
		t.Fatalf("teardown of unknown vm should return nil (ErrNotFound swallowed); got: %v", err)
	}
}

func TestTeardownVMActivity_EmptyID_NoOp(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	a, err := runner.NewTeardownVMActivity(fake)
	if err != nil {
		t.Fatalf("NewTeardownVMActivity: %v", err)
	}
	if err := a.Execute(context.Background(), ""); err != nil {
		t.Fatalf("empty vmID should be a no-op success, got: %v", err)
	}
	if got := len(fake.TeardownCalls()); got != 0 {
		t.Fatalf("provisioner.Teardown must not be called for empty vmID; got %d", got)
	}
}

func TestTeardownVMActivity_TransportError_Surfaces(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	fake.SetTeardown(func(_ string) error { return errors.New("connection reset") })
	a, err := runner.NewTeardownVMActivity(fake)
	if err != nil {
		t.Fatalf("NewTeardownVMActivity: %v", err)
	}
	if err := a.Execute(context.Background(), "some-vm"); err == nil {
		t.Fatal("expected error for transport failure")
	}
}

func TestTeardownVMActivity_NilProvisioner(t *testing.T) {
	t.Parallel()
	if _, err := runner.NewTeardownVMActivity(nil); err == nil {
		t.Error("expected error for nil provisioner")
	}
}
