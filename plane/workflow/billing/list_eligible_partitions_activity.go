package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
)

// ActivityNameListEligiblePartitions is the registered name for the
// ListEligiblePartitionsActivity.Execute method. The DEK destruction
// workflow dispatches by string so tests can substitute a fake.
const ActivityNameListEligiblePartitions = "billing.ListEligiblePartitionsForDEKDestroy"

// ListEligibleInput is the input to ListEligiblePartitionsActivity.Execute.
// Cutoff is the upper bound: rows with archived_at < cutoff are returned.
type ListEligibleInput struct {
	Cutoff time.Time
}

// EligiblePartition is a per-partition record returned to the workflow.
// KEKHint is the manifest hint embedded at archive time and is required for
// DestroyDEKActivity. KEKHint may be empty for legacy archives written
// before the DEK destruction workflow shipped — those rows are surfaced
// here so the workflow can record a "missing_kek_hint" skip and operators
// can backfill via the runbook.
type EligiblePartition struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	ArchivedAt    time.Time
	KEKHint       string
}

// ListEligibleResult is the activity's return type — a deterministic
// slice (sorted by year, month, partition id) is propagated to the workflow.
type ListEligibleResult struct {
	Partitions []EligiblePartition
}

// KEKHintResolver looks up the kek_hint for a given partition archive.
// For #80 we read it out of the manifest.json sitting in the object store
// next to the encrypted parquet (issue #74's archiveManifest, see
// export_activity.go). The interface keeps the activity testable without
// a real object store.
type KEKHintResolver interface {
	ResolveKEKHint(ctx context.Context, lakeURI string) (string, error)
}

// ListEligiblePartitionsActivity scans billing.partition_archives for rows
// older than the cutoff and resolves each row's kek_hint via the supplied
// resolver. Errors resolving an individual hint do not abort the activity:
// the row is returned with KEKHint == "" and the workflow records a skip.
type ListEligiblePartitionsActivity struct {
	store    store.MetadataStore
	resolver KEKHintResolver
}

// NewListEligiblePartitionsActivity returns a ListEligiblePartitionsActivity.
// Both deps must be non-nil so the worker boot path fails fast.
func NewListEligiblePartitionsActivity(s store.MetadataStore, r KEKHintResolver) (*ListEligiblePartitionsActivity, error) {
	if s == nil {
		return nil, errors.New("billing.NewListEligiblePartitionsActivity: store is nil")
	}
	if r == nil {
		return nil, errors.New("billing.NewListEligiblePartitionsActivity: resolver is nil")
	}
	return &ListEligiblePartitionsActivity{store: s, resolver: r}, nil
}

// Execute returns the deterministic list of eligible partitions older than
// in.Cutoff. Errors resolving any individual kek_hint are swallowed and the
// row is returned with KEKHint=="" so the workflow can record a skip rather
// than aborting the run for the entire batch.
func (a *ListEligiblePartitionsActivity) Execute(ctx context.Context, in ListEligibleInput) (ListEligibleResult, error) {
	if in.Cutoff.IsZero() {
		return ListEligibleResult{}, errors.New("billing.ListEligiblePartitions: cutoff is zero")
	}
	rows, err := a.store.Billing().ListPartitionArchivesArchivedBefore(ctx, in.Cutoff)
	if err != nil {
		return ListEligibleResult{}, fmt.Errorf("billing.ListEligiblePartitions: %w", err)
	}
	out := make([]EligiblePartition, 0, len(rows))
	for _, pa := range rows {
		hint, _ := a.resolver.ResolveKEKHint(ctx, pa.LakeURI) // best-effort
		out = append(out, EligiblePartition{
			Year:          pa.Year,
			Month:         pa.Month,
			PartitionName: pa.PartitionName,
			LakeURI:       pa.LakeURI,
			ArchivedAt:    pa.ArchivedAt,
			KEKHint:       hint,
		})
	}
	return ListEligibleResult{Partitions: out}, nil
}
