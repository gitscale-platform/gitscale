package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

// ActivityNameCreatePartition is the registered name for the
// CreatePartitionActivity.Execute method. The workflow dispatches by string
// so tests can substitute a fake activity without holding a reference.
const ActivityNameCreatePartition = "billing.CreatePartition"

// CreatePartitionInput is passed from PartitionRolloverWorkflow to
// CreatePartitionActivity.
type CreatePartitionInput struct {
	Year  int
	Month int
}

// CreatePartitionResult records the activity outcome for the workflow's
// return value (and for tests asserting on the activity's behaviour).
type CreatePartitionResult struct {
	PartitionName string
	Created       bool // false if the partition already existed
}

// CreatePartitionActivity wraps a billing.Partitioner so it can be registered
// with the worker and invoked from a workflow. Activity methods are I/O
// boundaries (ADR-003); errors are returned to the workflow which applies
// the retry policy.
type CreatePartitionActivity struct {
	partitioner billing.Partitioner
}

// NewCreatePartitionActivity returns a CreatePartitionActivity backed by p.
// Returns an error if p is nil so the worker boot path fails fast.
func NewCreatePartitionActivity(p billing.Partitioner) (*CreatePartitionActivity, error) {
	if p == nil {
		return nil, errors.New("billing.NewCreatePartitionActivity: partitioner is nil")
	}
	return &CreatePartitionActivity{partitioner: p}, nil
}

// Execute creates the (year, month) partition on billing.usage_events. The
// underlying Partitioner is idempotent — concurrent retries are safe.
func (a *CreatePartitionActivity) Execute(ctx context.Context, in CreatePartitionInput) (CreatePartitionResult, error) {
	created, err := a.partitioner.CreateUsageEventsPartition(ctx, in.Year, in.Month)
	if err != nil {
		return CreatePartitionResult{}, err
	}
	return CreatePartitionResult{
		PartitionName: fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month),
		Created:       created,
	}, nil
}
