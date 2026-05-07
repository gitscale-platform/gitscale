package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// registerArchiveActivityStubs registers no-op activity stubs under the
// archive workflow's activity names so OnActivity(...) mocks can attach.
// The mocks always intercept; the function bodies never run.
func registerArchiveActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(
		func(context.Context, DetachInput) error { return nil },
		activity.RegisterOptions{Name: ActivityNameDetachPartition},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, ExportInput) (ExportResult, error) { return ExportResult{}, nil },
		activity.RegisterOptions{Name: ActivityNameExport},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, EmitInput) error { return nil },
		activity.RegisterOptions{Name: ActivityNameEmitArchiveEvent},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, DropInput) error { return nil },
		activity.RegisterOptions{Name: ActivityNameDropPartition},
	)
}

func TestPartitionArchiveWorkflow_happyPath(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)
	registerArchiveActivityStubs(env)

	env.OnActivity(ActivityNameDetachPartition, mock.Anything, DetachInput{Year: 2026, Month: 5}).
		Return(nil)
	env.OnActivity(ActivityNameExport, mock.Anything, ExportInput{Year: 2026, Month: 5}).
		Return(ExportResult{
			LakeURI:      "s3://test-bucket/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
			RowCount:     100,
			BytesWritten: 4096,
			SHA256Hex:    "abc123",
		}, nil)
	env.OnActivity(ActivityNameEmitArchiveEvent, mock.Anything, EmitInput{
		Year:          2026,
		Month:         5,
		PartitionName: "billing.usage_events_2026_05",
		LakeURI:       "s3://test-bucket/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		RowCount:      100,
		BytesWritten:  4096,
	}).Return(nil)
	env.OnActivity(ActivityNameDropPartition, mock.Anything, DropInput{Year: 2026, Month: 5}).
		Return(nil)

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result ArchiveResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if result.RowCount != 100 {
		t.Errorf("RowCount=%d want 100", result.RowCount)
	}
	if result.LakeURI == "" {
		t.Error("LakeURI empty")
	}
}

func TestPartitionArchiveWorkflow_exportRetryOnFirstFailure(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)
	registerArchiveActivityStubs(env)

	exportResult := ExportResult{
		LakeURI:      "s3://test-bucket/billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet",
		RowCount:     50,
		BytesWritten: 2048,
		SHA256Hex:    "def456",
	}
	env.OnActivity(ActivityNameDetachPartition, mock.Anything, DetachInput{Year: 2026, Month: 5}).
		Return(nil)
	env.OnActivity(ActivityNameExport, mock.Anything, ExportInput{Year: 2026, Month: 5}).
		Return(ExportResult{}, errors.New("s3: connection reset")).Once()
	env.OnActivity(ActivityNameExport, mock.Anything, ExportInput{Year: 2026, Month: 5}).
		Return(exportResult, nil)
	env.OnActivity(ActivityNameEmitArchiveEvent, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(ActivityNameDropPartition, mock.Anything, DropInput{Year: 2026, Month: 5}).
		Return(nil)

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error after retry: %v", err)
	}
	var result ArchiveResult
	_ = env.GetWorkflowResult(&result)
	if result.RowCount != 50 {
		t.Errorf("RowCount=%d want 50", result.RowCount)
	}
}

func TestPartitionArchiveWorkflow_dropFailureSurfacesError(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PartitionArchiveWorkflow)
	registerArchiveActivityStubs(env)

	env.OnActivity(ActivityNameDetachPartition, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(ActivityNameExport, mock.Anything, mock.Anything).
		Return(ExportResult{LakeURI: "s3://b/k", RowCount: 1, BytesWritten: 100, SHA256Hex: "x"}, nil)
	env.OnActivity(ActivityNameEmitArchiveEvent, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(ActivityNameDropPartition, mock.Anything, mock.Anything).
		Return(errors.New("pg: table does not exist"))

	env.ExecuteWorkflow(PartitionArchiveWorkflow, ArchiveInput{
		RunTime: time.Date(2027, 11, 24, 14, 0, 0, 0, time.UTC),
		Year:    2026,
		Month:   5,
	})

	if env.GetWorkflowError() == nil {
		t.Error("expected workflow error when drop fails, got nil")
	}
}
