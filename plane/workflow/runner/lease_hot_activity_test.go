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

func TestLeaseHotVMActivity_Happy(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	stub := appclient.NewStubBillingClient()
	orgID := uuid.New()
	stub.SetQuotaFor(orgID, appclient.QuotaAccountView{
		AccountID: uuid.New(), OrgID: orgID, PlanTier: "pro",
		ComputeMinutesPerMonthCap: 5000,
	})
	a, err := runner.NewLeaseHotVMActivity(fake, stub)
	if err != nil {
		t.Fatalf("NewLeaseHotVMActivity: %v", err)
	}
	h, err := a.Execute(context.Background(), runner.LeaseInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: orgID,
		Resource: runner.DefaultResourceShape,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if h.ID == "" {
		t.Fatal("expected non-empty handle ID")
	}
	if got := len(fake.LeaseCalls()); got != 1 {
		t.Fatalf("expected 1 LeaseHot call, got %d", got)
	}
}

func TestLeaseHotVMActivity_QuotaInsufficient_NonRetryable(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFake()
	stub := appclient.NewStubBillingClient()
	orgID := uuid.New()
	stub.SetQuotaFor(orgID, appclient.QuotaAccountView{
		AccountID: uuid.New(), OrgID: orgID, PlanTier: "free",
		ComputeMinutesPerMonthCap: 1,
	})
	a, err := runner.NewLeaseHotVMActivity(fake, stub)
	if err != nil {
		t.Fatalf("NewLeaseHotVMActivity: %v", err)
	}
	_, err = a.Execute(context.Background(), runner.LeaseInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: orgID,
		Resource: runner.DefaultResourceShape,
	})
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected temporal.ApplicationError, got %T: %v", err, err)
	}
	if !appErr.NonRetryable() {
		t.Fatal("expected NonRetryable error")
	}
}

func TestLeaseHotVMActivity_NilDeps(t *testing.T) {
	t.Parallel()
	if _, err := runner.NewLeaseHotVMActivity(nil, appclient.NewStubBillingClient()); err == nil {
		t.Error("expected error for nil provisioner")
	}
	if _, err := runner.NewLeaseHotVMActivity(runnertest.NewFake(), nil); err == nil {
		t.Error("expected error for nil billing client")
	}
}
