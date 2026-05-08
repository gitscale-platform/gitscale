package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

// TestDEKDestructionRouter_Computes7y30dCutoff fixes workflow.Now to a known
// timestamp and asserts the child workflow is started with Cutoff = now − 7y30d.
func TestDEKDestructionRouter_Computes7y30dCutoff(t *testing.T) {
	cases := []struct {
		name       string
		fireTime   time.Time
		wantCutoff time.Time
	}{
		{
			name:       "midyear",
			fireTime:   time.Date(2034, 6, 1, 2, 0, 0, 0, time.UTC),
			wantCutoff: time.Date(2034, 6, 1, 2, 0, 0, 0, time.UTC).AddDate(0, 0, -DEKDestructionRetentionDays),
		},
		{
			name:       "epoch-edge",
			fireTime:   time.Date(2033, 1, 1, 0, 0, 0, 0, time.UTC),
			wantCutoff: time.Date(2033, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -DEKDestructionRetentionDays),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &testsuite.WorkflowTestSuite{}
			env := s.NewTestWorkflowEnvironment()
			env.SetStartTime(tc.fireTime)

			env.RegisterWorkflow(DEKDestructionRouterWorkflow)
			env.RegisterWorkflow(DEKDestructionWorkflow)

			var got DEKDestructionInput
			env.OnWorkflow(DEKDestructionWorkflow, mock.Anything, mock.MatchedBy(func(in DEKDestructionInput) bool {
				got = in
				return true
			})).Return(DEKDestructionResult{}, nil).Once()

			env.ExecuteWorkflow(DEKDestructionRouterWorkflow)

			if !env.IsWorkflowCompleted() {
				t.Fatal("router workflow did not complete")
			}
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("router workflow error: %v", err)
			}
			if !got.Cutoff.Equal(tc.wantCutoff) {
				t.Errorf("Cutoff=%v want %v", got.Cutoff, tc.wantCutoff)
			}
			if !got.RunTime.Equal(tc.fireTime) {
				t.Errorf("RunTime=%v want %v", got.RunTime, tc.fireTime)
			}
		})
	}
}
