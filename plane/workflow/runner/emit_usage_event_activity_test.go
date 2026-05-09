package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
)

func TestEmitUsageEventActivity_RecordsCallShape(t *testing.T) {
	t.Parallel()
	stub := appclient.NewStubBillingClient()
	a, err := runner.NewEmitUsageEventActivity(stub)
	if err != nil {
		t.Fatalf("NewEmitUsageEventActivity: %v", err)
	}
	jobID := uuid.New()
	in := runner.UsageInput{
		JobID:         jobID,
		PrincipalID:   uuid.New(),
		PrincipalKind: "agent",
		OrgID:         uuid.New(),
		RepoID:        uuid.New(),
		Tier:          "cold",
		Result: runner.JobResult{
			ExitCode:      0,
			DurationMS:    2000,
			BytesEgressed: 4 * 1024,
			PeakMemoryKB:  2048,
		},
	}
	if err := a.Execute(context.Background(), in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	calls := stub.CICalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 CI call, got %d", len(calls))
	}
	c := calls[0]
	if c.JobID != jobID {
		t.Errorf("JobID mismatch")
	}
	if c.Tier != "cold" {
		t.Errorf("Tier mismatch: %s", c.Tier)
	}
	if c.PrincipalKind != "agent" {
		t.Errorf("PrincipalKind mismatch: %s", c.PrincipalKind)
	}
	if c.EgressKB != 4 {
		t.Errorf("EgressKB expected 4, got %d", c.EgressKB)
	}
	if c.VCPUSeconds != 2.0 {
		t.Errorf("VCPUSeconds expected 2.0, got %f", c.VCPUSeconds)
	}
}

func TestEmitUsageEventActivity_PropagatesError(t *testing.T) {
	t.Parallel()
	stub := appclient.NewStubBillingClient()
	want := errors.New("billing service down")
	stub.SetCIFn(func(_ appclient.CIJobCompletedInput) error { return want })
	a, err := runner.NewEmitUsageEventActivity(stub)
	if err != nil {
		t.Fatalf("NewEmitUsageEventActivity: %v", err)
	}
	if err := a.Execute(context.Background(), runner.UsageInput{
		JobID: uuid.New(), PrincipalID: uuid.New(), OrgID: uuid.New(), RepoID: uuid.New(),
		PrincipalKind: "agent", Tier: "cold",
	}); !errors.Is(err, want) {
		t.Fatalf("expected wrapped err %v, got %v", want, err)
	}
}

func TestEmitUsageEventActivity_NilClient(t *testing.T) {
	t.Parallel()
	if _, err := runner.NewEmitUsageEventActivity(nil); err == nil {
		t.Error("expected error for nil client")
	}
}
