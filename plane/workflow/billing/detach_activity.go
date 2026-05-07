package billing

import (
	"context"
	"errors"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

const ActivityNameDetachPartition = "billing.DetachPartition"

// DetachInput is the input to DetachPartitionActivity.Execute.
type DetachInput struct {
	Year  int
	Month int
}

// DetachPartitionActivity issues DETACH PARTITION CONCURRENTLY via the Archiver.
type DetachPartitionActivity struct {
	archiver billingstore.Archiver
}

// NewDetachPartitionActivity returns a DetachPartitionActivity. Returns an
// error if archiver is nil so the worker boot path fails fast.
func NewDetachPartitionActivity(archiver billingstore.Archiver) (*DetachPartitionActivity, error) {
	if archiver == nil {
		return nil, errors.New("billing.NewDetachPartitionActivity: archiver is nil")
	}
	return &DetachPartitionActivity{archiver: archiver}, nil
}

// Execute detaches billing.usage_events_YYYY_MM from the parent table.
// Idempotent — the Archiver checks pg_inherits and skips if already detached.
func (a *DetachPartitionActivity) Execute(ctx context.Context, in DetachInput) error {
	return a.archiver.DetachUsageEventsPartition(ctx, in.Year, in.Month)
}
