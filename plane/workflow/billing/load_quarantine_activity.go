package billing

import (
	"context"
	"errors"
	"fmt"
	"os"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

// ActivityNameLoadQuarantine is the registered name for LoadIntoQuarantineActivity.
const ActivityNameLoadQuarantine = "billing.LoadIntoQuarantine"

// LoadQuarantineInput is the input to LoadIntoQuarantineActivity.Execute.
type LoadQuarantineInput struct {
	Year          int
	Month         int
	PlaintextPath string
}

// LoadQuarantineResult records the table name and row count loaded.
type LoadQuarantineResult struct {
	QuarantineTable string
	RowsImported    int64
}

// LoadIntoQuarantineActivity creates the quarantine table, COPYs plaintext
// rows in, then revokes write privileges. The activity also removes the
// scratch file on success — on failure the workflow's drop-quarantine
// compensation handles cleanup.
type LoadIntoQuarantineActivity struct {
	restorer billingstore.Restorer
}

// NewLoadIntoQuarantineActivity returns a LoadIntoQuarantineActivity.
func NewLoadIntoQuarantineActivity(restorer billingstore.Restorer) (*LoadIntoQuarantineActivity, error) {
	if restorer == nil {
		return nil, errors.New("billing.NewLoadIntoQuarantineActivity: restorer is nil")
	}
	return &LoadIntoQuarantineActivity{restorer: restorer}, nil
}

// Execute runs Ensure → Load → Seal in sequence. Any error before Seal leaves
// the workflow to invoke a drop-quarantine compensation activity.
func (a *LoadIntoQuarantineActivity) Execute(ctx context.Context, in LoadQuarantineInput) (LoadQuarantineResult, error) {
	table, err := a.restorer.EnsureQuarantineTable(ctx, in.Year, in.Month)
	if err != nil {
		return LoadQuarantineResult{}, fmt.Errorf("load quarantine: ensure: %w", err)
	}

	f, err := os.Open(in.PlaintextPath)
	if err != nil {
		return LoadQuarantineResult{}, fmt.Errorf("load quarantine: open scratch %s: %w", in.PlaintextPath, err)
	}
	rows, copyErr := a.restorer.LoadParquetIntoQuarantine(ctx, in.Year, in.Month, f)
	_ = f.Close()
	if copyErr != nil {
		return LoadQuarantineResult{}, fmt.Errorf("load quarantine: copy: %w", copyErr)
	}

	if err := a.restorer.SealQuarantineTable(ctx, in.Year, in.Month); err != nil {
		return LoadQuarantineResult{}, fmt.Errorf("load quarantine: seal: %w", err)
	}

	// Best-effort scratch cleanup. A leftover file is harmless for correctness.
	_ = os.Remove(in.PlaintextPath)

	return LoadQuarantineResult{QuarantineTable: table, RowsImported: rows}, nil
}

// ActivityNameDropQuarantine is the registered name for the compensation
// activity invoked by the workflow on failure.
const ActivityNameDropQuarantine = "billing.DropQuarantine"

// DropQuarantineInput identifies the quarantine table to drop.
type DropQuarantineInput struct {
	Year  int
	Month int
}

// DropQuarantineActivity removes the quarantine table on workflow failure.
// Idempotent (DROP TABLE IF EXISTS).
type DropQuarantineActivity struct {
	restorer billingstore.Restorer
}

// NewDropQuarantineActivity returns a DropQuarantineActivity.
func NewDropQuarantineActivity(restorer billingstore.Restorer) (*DropQuarantineActivity, error) {
	if restorer == nil {
		return nil, errors.New("billing.NewDropQuarantineActivity: restorer is nil")
	}
	return &DropQuarantineActivity{restorer: restorer}, nil
}

// Execute drops the quarantine table.
func (a *DropQuarantineActivity) Execute(ctx context.Context, in DropQuarantineInput) error {
	return a.restorer.DropQuarantineTable(ctx, in.Year, in.Month)
}
