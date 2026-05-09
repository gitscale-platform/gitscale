package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
	"github.com/gitscale-platform/gitscale/plane/workflow/runner/runnertest"
)

func newAct(t *testing.T) (*runner.BootColdVMActivity, *runnertest.Fake, *appclient.StubBillingClient, uuid.UUID) {
	t.Helper()
	fake := runnertest.NewFake()
	stub := appclient.NewStubBillingClient()
	orgID := uuid.New()
	stub.SetQuotaFor(orgID, appclient.QuotaAccountView{
		AccountID: uuid.New(), OrgID: orgID, PlanTier: "free",
		ComputeMinutesPerMonthCap: 500,
	})
	a, err := runner.NewBootColdVMActivity(fake, stub)
	if err != nil {
		t.Fatalf("NewBootColdVMActivity: %v", err)
	}
	return a, fake, stub, orgID
}

func TestBootColdVMActivity_Happy(t *testing.T) {
	t.Parallel()
	a, fake, _, orgID := newAct(t)
	in := runner.BootInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: orgID,
		Resource: runner.DefaultResourceShape,
	}
	h, err := a.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if h.ID == "" {
		t.Fatal("expected non-empty handle ID")
	}
	if got := len(fake.BootCalls()); got != 1 {
		t.Fatalf("expected 1 BootCold call, got %d", got)
	}
}

func TestBootColdVMActivity_QuotaAccountNotFound_NonRetryable(t *testing.T) {
	t.Parallel()
	a, _, _, _ := newAct(t)
	// Use an orgID that wasn't seeded.
	in := runner.BootInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: uuid.New(),
		Resource: runner.DefaultResourceShape,
	}
	_, err := a.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error")
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected temporal.ApplicationError, got %T: %v", err, err)
	}
	if !appErr.NonRetryable() {
		t.Fatal("expected NonRetryable error")
	}
	if appErr.Type() != "QuotaInsufficient" {
		t.Fatalf("expected error type QuotaInsufficient, got %q", appErr.Type())
	}
}

func TestBootColdVMActivity_QuotaInsufficient_NonRetryable(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	stub := appclient.NewStubBillingClient()
	orgID := uuid.New()
	// Tiny cap that any meaningful job exceeds.
	stub.SetQuotaFor(orgID, appclient.QuotaAccountView{
		AccountID: uuid.New(), OrgID: orgID, PlanTier: "free",
		ComputeMinutesPerMonthCap: 1,
	})
	a, err := runner.NewBootColdVMActivity(fake, stub)
	if err != nil {
		t.Fatalf("NewBootColdVMActivity: %v", err)
	}
	in := runner.BootInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: orgID,
		Resource: runner.DefaultResourceShape,
	}
	_, err = a.Execute(context.Background(), in)
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected temporal.ApplicationError, got %T: %v", err, err)
	}
	if !appErr.NonRetryable() {
		t.Fatal("expected NonRetryable error")
	}
	if appErr.Type() != "QuotaInsufficient" {
		t.Fatalf("expected QuotaInsufficient type, got %q", appErr.Type())
	}
	if got := len(fake.BootCalls()); got != 0 {
		t.Fatalf("provisioner must not be called when quota is insufficient; got %d calls", got)
	}
}

func TestBootColdVMActivity_ProvisionerTransportError_Retryable(t *testing.T) {
	t.Parallel()
	a, fake, _, orgID := newAct(t)
	fake.SetBootCold(func(_ runner.BootInput) (runner.MicroVMHandle, error) {
		return runner.MicroVMHandle{}, errors.New("connection refused")
	})
	in := runner.BootInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: orgID,
		Resource: runner.DefaultResourceShape,
	}
	_, err := a.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error")
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.NonRetryable() {
		t.Fatalf("transport errors must be retryable; got NonRetryable: %v", err)
	}
}

func TestBootColdVMActivity_NilDeps(t *testing.T) {
	t.Parallel()
	if _, err := runner.NewBootColdVMActivity(nil, appclient.NewStubBillingClient()); err == nil {
		t.Error("expected error for nil provisioner")
	}
	if _, err := runner.NewBootColdVMActivity(runnertest.NewFake(), nil); err == nil {
		t.Error("expected error for nil billing client")
	}
}
