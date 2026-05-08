package billing

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

// ActivityNameEmitDEKDestroyed is the registered name for the
// EmitDEKDestroyedActivity.Execute method.
const ActivityNameEmitDEKDestroyed = "billing.EmitDEKDestroyedEvent"

// EmitDEKDestroyedInput is the input to EmitDEKDestroyedActivity.Execute.
type EmitDEKDestroyedInput struct {
	Year            int
	Month           int
	PartitionName   string
	KEKHint         string
	VaultKeyVersion int
}

// EmitDEKDestroyedActivity calls appclient.BillingClient.RecordDEKDestroyed,
// which routes to the billing app-plane service (ADR-019). The service
// writes billing.partition_dek_destroyed to billing_outbox in a single
// transaction (ADR-008). Idempotent on (year, month, partition_name,
// kek_hint).
type EmitDEKDestroyedActivity struct {
	client appclient.BillingClient
}

// NewEmitDEKDestroyedActivity returns an EmitDEKDestroyedActivity. Returns
// an error if client is nil so the worker boot path fails fast.
func NewEmitDEKDestroyedActivity(client appclient.BillingClient) (*EmitDEKDestroyedActivity, error) {
	if client == nil {
		return nil, errors.New("billing.NewEmitDEKDestroyedActivity: client is nil")
	}
	return &EmitDEKDestroyedActivity{client: client}, nil
}

// Execute calls RecordDEKDestroyed on the billing app-plane service.
func (a *EmitDEKDestroyedActivity) Execute(ctx context.Context, in EmitDEKDestroyedInput) error {
	return a.client.RecordDEKDestroyed(ctx, appclient.DEKDestroyedInput{
		Year:            in.Year,
		Month:           in.Month,
		PartitionName:   in.PartitionName,
		KEKHint:         in.KEKHint,
		VaultKeyVersion: in.VaultKeyVersion,
	})
}
