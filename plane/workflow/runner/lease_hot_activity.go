package runner

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

// ActivityNameLeaseHotVM is the registered activity name.
const ActivityNameLeaseHotVM = "runner.LeaseHotVM"

// LeaseHotVMActivity leases a pre-warmed microVM from the hot fleet.
// Mirror of BootColdVMActivity but with a tighter timeout and retry
// policy (see plane/workflow/ci.bootOptionsFor): the hot pool exists
// precisely to give bursty agent traffic sub-second admission.
type LeaseHotVMActivity struct {
	provisioner MicroVMProvisioner
	billing     appclient.BillingClient
}

// NewLeaseHotVMActivity wraps the deps.
func NewLeaseHotVMActivity(p MicroVMProvisioner, b appclient.BillingClient) (*LeaseHotVMActivity, error) {
	if p == nil {
		return nil, errors.New("runner.NewLeaseHotVMActivity: provisioner is nil")
	}
	if b == nil {
		return nil, errors.New("runner.NewLeaseHotVMActivity: billing client is nil")
	}
	return &LeaseHotVMActivity{provisioner: p, billing: b}, nil
}

// Execute is the activity entry point. Same admission rule as the cold
// path — agents may opt into the hot pool via annotation but the quota
// envelope is enforced regardless of pool tier.
func (a *LeaseHotVMActivity) Execute(ctx context.Context, in LeaseInput) (MicroVMHandle, error) {
	q, err := a.billing.GetQuotaAccount(ctx, in.OrgID)
	if err != nil {
		if errors.Is(err, appclient.ErrQuotaAccountNotFound) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.LeaseHotVM: quota account not found", "QuotaInsufficient", ErrQuotaInsufficient)
		}
		return MicroVMHandle{}, fmt.Errorf("runner.LeaseHotVM: GetQuotaAccount: %w", err)
	}
	if err := validateAgainstQuota(in.Resource, q.ComputeMinutesPerMonthCap); err != nil {
		if errors.Is(err, ErrQuotaInsufficient) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.LeaseHotVM: quota insufficient", "QuotaInsufficient", err)
		}
		return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
			"runner.LeaseHotVM: invalid resource shape", "InvalidShape", err)
	}
	h, err := a.provisioner.LeaseHot(ctx, in)
	if err != nil {
		if errors.Is(err, ErrQuotaInsufficient) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.LeaseHotVM: provisioner: quota insufficient", "QuotaInsufficient", err)
		}
		if errors.Is(err, ErrInvalidShape) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.LeaseHotVM: provisioner: invalid shape", "InvalidShape", err)
		}
		return MicroVMHandle{}, fmt.Errorf("runner.LeaseHotVM: provisioner.LeaseHot: %w", err)
	}
	return h, nil
}
