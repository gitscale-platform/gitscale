package runner

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

// ActivityNameBootColdVM is the registered activity name. Stable so workflow
// tests can dispatch by string without holding the activity reference.
const ActivityNameBootColdVM = "runner.BootColdVM"

// BootColdVMActivity allocates a fresh microVM from the cold pool. The
// activity calls GetQuotaAccount, validates the requested shape against
// the per-job ceiling, then asks the provisioner to boot. Quota-failure
// and shape-failure are non-retryable — the workflow surfaces them.
type BootColdVMActivity struct {
	provisioner MicroVMProvisioner
	billing     appclient.BillingClient
}

// NewBootColdVMActivity wraps the deps. Returns an error if either dep is
// nil so the worker boot path fails fast.
func NewBootColdVMActivity(p MicroVMProvisioner, b appclient.BillingClient) (*BootColdVMActivity, error) {
	if p == nil {
		return nil, errors.New("runner.NewBootColdVMActivity: provisioner is nil")
	}
	if b == nil {
		return nil, errors.New("runner.NewBootColdVMActivity: billing client is nil")
	}
	return &BootColdVMActivity{provisioner: p, billing: b}, nil
}

// Execute is the activity entry point. Quota check first (non-retryable
// failure), then provisioner boot. Errors propagate as-is so the workflow
// can classify them via errors.Is.
func (a *BootColdVMActivity) Execute(ctx context.Context, in BootInput) (MicroVMHandle, error) {
	q, err := a.billing.GetQuotaAccount(ctx, in.OrgID)
	if err != nil {
		if errors.Is(err, appclient.ErrQuotaAccountNotFound) {
			// No quota row → no entitlement → non-retryable.
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.BootColdVM: quota account not found", "QuotaInsufficient", ErrQuotaInsufficient)
		}
		return MicroVMHandle{}, fmt.Errorf("runner.BootColdVM: GetQuotaAccount: %w", err)
	}
	if err := validateAgainstQuota(in.Resource, q.ComputeMinutesPerMonthCap); err != nil {
		if errors.Is(err, ErrQuotaInsufficient) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.BootColdVM: quota insufficient", "QuotaInsufficient", err)
		}
		return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
			"runner.BootColdVM: invalid resource shape", "InvalidShape", err)
	}
	h, err := a.provisioner.BootCold(ctx, in)
	if err != nil {
		if errors.Is(err, ErrQuotaInsufficient) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.BootColdVM: provisioner: quota insufficient", "QuotaInsufficient", err)
		}
		if errors.Is(err, ErrInvalidShape) {
			return MicroVMHandle{}, temporal.NewNonRetryableApplicationError(
				"runner.BootColdVM: provisioner: invalid shape", "InvalidShape", err)
		}
		return MicroVMHandle{}, fmt.Errorf("runner.BootColdVM: provisioner.BootCold: %w", err)
	}
	return h, nil
}
