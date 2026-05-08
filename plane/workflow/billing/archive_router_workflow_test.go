package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

// TestArchiveRouter_Computes18MonthLag fixes workflow.Now to a known
// timestamp and asserts the child workflow is started for (now − 18mo).
func TestArchiveRouter_Computes18MonthLag(t *testing.T) {
	cases := []struct {
		name      string
		fireTime  time.Time
		wantYear  int
		wantMonth int
	}{
		{"midyear", time.Date(2026, 5, 24, 14, 0, 0, 0, time.UTC), 2024, 11},
		{"jan", time.Date(2027, 1, 24, 14, 0, 0, 0, time.UTC), 2025, 7},
		{"junToDec", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 2024, 12},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &testsuite.WorkflowTestSuite{}
			env := s.NewTestWorkflowEnvironment()
			env.SetStartTime(tc.fireTime)

			env.RegisterWorkflow(ArchiveRouterWorkflow)
			env.RegisterWorkflow(PartitionArchiveWorkflow)

			var gotYear, gotMonth int
			env.OnWorkflow(PartitionArchiveWorkflow, mock.Anything, mock.MatchedBy(func(in ArchiveInput) bool {
				gotYear = in.Year
				gotMonth = in.Month
				return true
			})).Return(ArchiveResult{
				PartitionName: "billing.usage_events_xxxx_xx",
				LakeURI:       "s3://b/k",
				RowCount:      1,
				BytesWritten:  100,
			}, nil).Once()

			env.ExecuteWorkflow(ArchiveRouterWorkflow)

			if !env.IsWorkflowCompleted() {
				t.Fatal("router workflow did not complete")
			}
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("router workflow error: %v", err)
			}
			if gotYear != tc.wantYear || gotMonth != tc.wantMonth {
				t.Errorf("child started with %d-%02d, want %d-%02d", gotYear, gotMonth, tc.wantYear, tc.wantMonth)
			}
		})
	}
}
