package billing

import (
	"context"
	"errors"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

const ActivityNameDropPartition = "billing.DropPartition"

// DropInput is the input to DropPartitionActivity.Execute.
type DropInput struct {
	Year  int
	Month int
}

// DropPartitionActivity drops the detached billing.usage_events_YYYY_MM table.
// Must only be called after the object store upload and outbox emit succeed.
type DropPartitionActivity struct {
	archiver billingstore.Archiver
}

// NewDropPartitionActivity returns a DropPartitionActivity backed by archiver.
func NewDropPartitionActivity(archiver billingstore.Archiver) (*DropPartitionActivity, error) {
	if archiver == nil {
		return nil, errors.New("billing.NewDropPartitionActivity: archiver is nil")
	}
	return &DropPartitionActivity{archiver: archiver}, nil
}

// Execute drops billing.usage_events_YYYY_MM. Uses DROP TABLE IF EXISTS — idempotent.
func (a *DropPartitionActivity) Execute(ctx context.Context, in DropInput) error {
	return a.archiver.DropUsageEventsPartition(ctx, in.Year, in.Month)
}
