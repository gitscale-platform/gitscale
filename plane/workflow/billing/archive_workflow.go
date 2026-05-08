package billing

import (
	"fmt"
	"time"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// ArchiveInput is the input to PartitionArchiveWorkflow.
// Year and Month identify the partition to archive.
// RunTime is the schedule's scheduled-time — passed through for determinism but
// not used to derive Year/Month here (caller sets them explicitly).
type ArchiveInput struct {
	RunTime time.Time
	Year    int
	Month   int
}

// ArchiveResult is the output of PartitionArchiveWorkflow.
type ArchiveResult struct {
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
}

// PartitionArchiveWorkflow archives billing.usage_events_YYYY_MM to the
// analytics-lake object store and drops the partition from PostgreSQL.
//
// Activity sequence: DetachPartition → Export → EmitOutbox → DropPartition.
// Drop failure after emit surfaces as a workflow error — data exists in both
// PG (detached) and object store; no data loss. Runbook: verify object store
// integrity then DROP TABLE manually or re-run the workflow.
func PartitionArchiveWorkflow(ctx workflow.Context, in ArchiveInput) (ArchiveResult, error) {
	if in.Year < 2026 || in.Year > 2099 {
		return ArchiveResult{}, fmt.Errorf("archive: year %d out of supported range [2026, 2099]", in.Year)
	}
	if in.Month < 1 || in.Month > 12 {
		return ArchiveResult{}, fmt.Errorf("archive: month %d out of range [1, 12]", in.Month)
	}
	partitionName := fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month)

	shortOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	longOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 4 * time.Hour,
		HeartbeatTimeout:    5 * time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	emitOpts := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}

	detachCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(detachCtx, ActivityNameDetachPartition,
		DetachInput{Year: in.Year, Month: in.Month},
	).Get(detachCtx, nil); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: detach: %w", err)
	}

	exportCtx := workflow.WithActivityOptions(ctx, longOpts)
	var exportResult ExportResult
	if err := workflow.ExecuteActivity(exportCtx, ActivityNameExport,
		ExportInput{Year: in.Year, Month: in.Month},
	).Get(exportCtx, &exportResult); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: export: %w", err)
	}

	emitCtx := workflow.WithActivityOptions(ctx, emitOpts)
	if err := workflow.ExecuteActivity(emitCtx, ActivityNameEmitArchiveEvent,
		EmitInput{
			Year:          in.Year,
			Month:         in.Month,
			PartitionName: partitionName,
			LakeURI:       exportResult.LakeURI,
			RowCount:      exportResult.RowCount,
			BytesWritten:  exportResult.BytesWritten,
		},
	).Get(emitCtx, nil); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: emit: %w", err)
	}

	dropCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(dropCtx, ActivityNameDropPartition,
		DropInput{Year: in.Year, Month: in.Month},
	).Get(dropCtx, nil); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive: drop (data safe in object store): %w", err)
	}

	return ArchiveResult{
		PartitionName: partitionName,
		LakeURI:       exportResult.LakeURI,
		RowCount:      exportResult.RowCount,
		BytesWritten:  exportResult.BytesWritten,
	}, nil
}
