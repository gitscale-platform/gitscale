package runner

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

// ActivityNameEmitUsageEvent is the registered activity name.
const ActivityNameEmitUsageEvent = "runner.EmitUsageEvent"

// EmitUsageEventActivity calls appclient.BillingClient.RecordCIJobCompleted,
// which routes to cmd/billing-service and writes the source row + outbox
// row in a single Tx (ADR-008/019). Idempotent on JobID — retries are safe
// per the policy in plane/workflow/ci.emitOptions.
type EmitUsageEventActivity struct {
	client appclient.BillingClient
}

// NewEmitUsageEventActivity wraps the client.
func NewEmitUsageEventActivity(c appclient.BillingClient) (*EmitUsageEventActivity, error) {
	if c == nil {
		return nil, errors.New("runner.NewEmitUsageEventActivity: client is nil")
	}
	return &EmitUsageEventActivity{client: c}, nil
}

// Execute translates the workflow-level UsageInput into the appclient
// projection and calls the billing service. Memory-second + vcpu-second
// metrics are derived from JobResult.DurationMS so the workflow code
// remains a pure orchestrator with no math.
func (a *EmitUsageEventActivity) Execute(ctx context.Context, in UsageInput) error {
	durationSec := float64(in.Result.DurationMS) / 1000.0
	// Note: ResourceShape's VCPU / MemoryMB are not on UsageInput because
	// the workflow already passed them into Boot/Lease and the actual
	// consumed metrics come from the host. The conversion below assumes
	// the result reflects scheduled-VM behaviour; if the runner adapter
	// reports finer-grained metrics in JobResult, plumb them through.
	return a.client.RecordCIJobCompleted(ctx, appclient.CIJobCompletedInput{
		JobID:           in.JobID,
		PrincipalID:     in.PrincipalID,
		PrincipalKind:   in.PrincipalKind,
		OrgID:           in.OrgID,
		RepoID:          in.RepoID,
		Tier:            in.Tier,
		VCPUSeconds:     durationSec, // single-job vCPU-seconds approximation
		MemoryMBSeconds: float64(in.Result.PeakMemoryKB) / 1024.0 * durationSec,
		EgressKB:        in.Result.BytesEgressed / 1024,
		ExitCode:        in.Result.ExitCode,
	})
}
