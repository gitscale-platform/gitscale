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

// registerDEKActivityStubs registers no-op activity stubs under the DEK
// destruction workflow's activity names so OnActivity(...) mocks can attach.
// The mocks always intercept; the function bodies never run.
func registerDEKActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(
		func(context.Context, ListEligibleInput) (ListEligibleResult, error) {
			return ListEligibleResult{}, nil
		},
		activity.RegisterOptions{Name: ActivityNameListEligiblePartitions},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, LegalHoldCheckInput) (LegalHoldCheckResult, error) {
			return LegalHoldCheckResult{}, nil
		},
		activity.RegisterOptions{Name: ActivityNameCheckLegalHold},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, OperatorApprovalInput) (OperatorApprovalResult, error) {
			return OperatorApprovalResult{}, nil
		},
		activity.RegisterOptions{Name: ActivityNameRequestOperatorApproval},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, DestroyDEKInput) (DestroyDEKResult, error) {
			return DestroyDEKResult{}, nil
		},
		activity.RegisterOptions{Name: ActivityNameDestroyDEK},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, EmitDEKDestroyedInput) error { return nil },
		activity.RegisterOptions{Name: ActivityNameEmitDEKDestroyed},
	)
}

func dekTestInput() DEKDestructionInput {
	now := time.Date(2034, 6, 1, 2, 0, 0, 0, time.UTC)
	return DEKDestructionInput{
		RunTime: now,
		Cutoff:  now.AddDate(0, 0, -DEKDestructionRetentionDays),
	}
}

func TestDEKDestructionWorkflow_happyPath(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	registerDEKActivityStubs(env)

	in := dekTestInput()
	parts := []EligiblePartition{
		{Year: 2027, Month: 1, PartitionName: "billing.usage_events_2027_01", LakeURI: "s3://b/k1.parquet", KEKHint: "platform-billing-v1"},
		{Year: 2027, Month: 2, PartitionName: "billing.usage_events_2027_02", LakeURI: "s3://b/k2.parquet", KEKHint: "platform-billing-v2"},
	}
	env.OnActivity(ActivityNameListEligiblePartitions, mock.Anything, ListEligibleInput{Cutoff: in.Cutoff}).
		Return(ListEligibleResult{Partitions: parts}, nil)
	env.OnActivity(ActivityNameCheckLegalHold, mock.Anything, mock.Anything).
		Return(LegalHoldCheckResult{Held: false}, nil)
	env.OnActivity(ActivityNameRequestOperatorApproval, mock.Anything, mock.Anything).
		Return(OperatorApprovalResult{Approved: true}, nil)
	env.OnActivity(ActivityNameDestroyDEK, mock.Anything, DestroyDEKInput{Year: 2027, Month: 1, KEKHint: "platform-billing-v1"}).
		Return(DestroyDEKResult{VaultKeyVersion: 1}, nil)
	env.OnActivity(ActivityNameDestroyDEK, mock.Anything, DestroyDEKInput{Year: 2027, Month: 2, KEKHint: "platform-billing-v2"}).
		Return(DestroyDEKResult{VaultKeyVersion: 2}, nil)
	env.OnActivity(ActivityNameEmitDEKDestroyed, mock.Anything, mock.Anything).
		Return(nil)

	env.ExecuteWorkflow(DEKDestructionWorkflow, in)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result DEKDestructionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if result.PartitionsScanned != 2 {
		t.Errorf("PartitionsScanned=%d want 2", result.PartitionsScanned)
	}
	if result.KeysDestroyed != 2 {
		t.Errorf("KeysDestroyed=%d want 2", result.KeysDestroyed)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("Skipped=%v want empty", result.Skipped)
	}
}

func TestDEKDestructionWorkflow_skipMissingKEKHint(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	registerDEKActivityStubs(env)

	in := dekTestInput()
	env.OnActivity(ActivityNameListEligiblePartitions, mock.Anything, mock.Anything).
		Return(ListEligibleResult{Partitions: []EligiblePartition{
			{Year: 2027, Month: 1, PartitionName: "p1", LakeURI: "s3://b/k.parquet", KEKHint: ""},
		}}, nil)
	// No legal-hold / approval / destroy / emit invocations expected.

	env.ExecuteWorkflow(DEKDestructionWorkflow, in)

	var result DEKDestructionResult
	_ = env.GetWorkflowResult(&result)
	if result.KeysDestroyed != 0 {
		t.Errorf("KeysDestroyed=%d want 0", result.KeysDestroyed)
	}
	if len(result.Skipped) != 1 || !contains(result.Skipped[0], "missing_kek_hint") {
		t.Errorf("Skipped=%v want one missing_kek_hint entry", result.Skipped)
	}
}

func TestDEKDestructionWorkflow_skipLegalHold(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	registerDEKActivityStubs(env)

	env.OnActivity(ActivityNameListEligiblePartitions, mock.Anything, mock.Anything).
		Return(ListEligibleResult{Partitions: []EligiblePartition{
			{Year: 2027, Month: 1, PartitionName: "p1", LakeURI: "s3://b/k.parquet", KEKHint: "platform-billing-v1"},
		}}, nil)
	env.OnActivity(ActivityNameCheckLegalHold, mock.Anything, mock.Anything).
		Return(LegalHoldCheckResult{Held: true, Reason: "litigation-x"}, nil)

	env.ExecuteWorkflow(DEKDestructionWorkflow, dekTestInput())

	var result DEKDestructionResult
	_ = env.GetWorkflowResult(&result)
	if result.KeysDestroyed != 0 {
		t.Errorf("KeysDestroyed=%d want 0", result.KeysDestroyed)
	}
	if len(result.Skipped) != 1 || !contains(result.Skipped[0], "legal_hold:litigation-x") {
		t.Errorf("Skipped=%v want legal_hold entry", result.Skipped)
	}
}

func TestDEKDestructionWorkflow_skipApprovalRejected(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	registerDEKActivityStubs(env)

	env.OnActivity(ActivityNameListEligiblePartitions, mock.Anything, mock.Anything).
		Return(ListEligibleResult{Partitions: []EligiblePartition{
			{Year: 2027, Month: 1, PartitionName: "p1", LakeURI: "s3://b/k.parquet", KEKHint: "platform-billing-v1"},
		}}, nil)
	env.OnActivity(ActivityNameCheckLegalHold, mock.Anything, mock.Anything).
		Return(LegalHoldCheckResult{Held: false}, nil)
	env.OnActivity(ActivityNameRequestOperatorApproval, mock.Anything, mock.Anything).
		Return(OperatorApprovalResult{Approved: false, Reason: "needs-review"}, nil)

	env.ExecuteWorkflow(DEKDestructionWorkflow, dekTestInput())

	var result DEKDestructionResult
	_ = env.GetWorkflowResult(&result)
	if result.KeysDestroyed != 0 {
		t.Errorf("KeysDestroyed=%d want 0", result.KeysDestroyed)
	}
	if len(result.Skipped) != 1 || !contains(result.Skipped[0], "approval_rejected:needs-review") {
		t.Errorf("Skipped=%v want approval_rejected entry", result.Skipped)
	}
}

func TestDEKDestructionWorkflow_skipDestroyError(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	registerDEKActivityStubs(env)

	env.OnActivity(ActivityNameListEligiblePartitions, mock.Anything, mock.Anything).
		Return(ListEligibleResult{Partitions: []EligiblePartition{
			{Year: 2027, Month: 1, PartitionName: "p1", LakeURI: "s3://b/k.parquet", KEKHint: "platform-billing-v1"},
		}}, nil)
	env.OnActivity(ActivityNameCheckLegalHold, mock.Anything, mock.Anything).
		Return(LegalHoldCheckResult{Held: false}, nil)
	env.OnActivity(ActivityNameRequestOperatorApproval, mock.Anything, mock.Anything).
		Return(OperatorApprovalResult{Approved: true}, nil)
	env.OnActivity(ActivityNameDestroyDEK, mock.Anything, mock.Anything).
		Return(DestroyDEKResult{}, errors.New("vault: 503"))

	env.ExecuteWorkflow(DEKDestructionWorkflow, dekTestInput())

	var result DEKDestructionResult
	_ = env.GetWorkflowResult(&result)
	if result.KeysDestroyed != 0 {
		t.Errorf("KeysDestroyed=%d want 0", result.KeysDestroyed)
	}
	if len(result.Skipped) != 1 || !contains(result.Skipped[0], "destroy_error") {
		t.Errorf("Skipped=%v want destroy_error entry", result.Skipped)
	}
}

func TestDEKDestructionWorkflow_emitErrorStillCountsDestroyed(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	registerDEKActivityStubs(env)

	env.OnActivity(ActivityNameListEligiblePartitions, mock.Anything, mock.Anything).
		Return(ListEligibleResult{Partitions: []EligiblePartition{
			{Year: 2027, Month: 1, PartitionName: "p1", LakeURI: "s3://b/k.parquet", KEKHint: "platform-billing-v1"},
		}}, nil)
	env.OnActivity(ActivityNameCheckLegalHold, mock.Anything, mock.Anything).
		Return(LegalHoldCheckResult{Held: false}, nil)
	env.OnActivity(ActivityNameRequestOperatorApproval, mock.Anything, mock.Anything).
		Return(OperatorApprovalResult{Approved: true}, nil)
	env.OnActivity(ActivityNameDestroyDEK, mock.Anything, mock.Anything).
		Return(DestroyDEKResult{VaultKeyVersion: 1}, nil)
	env.OnActivity(ActivityNameEmitDEKDestroyed, mock.Anything, mock.Anything).
		Return(errors.New("billing-svc: 500"))

	env.ExecuteWorkflow(DEKDestructionWorkflow, dekTestInput())

	var result DEKDestructionResult
	_ = env.GetWorkflowResult(&result)
	if result.KeysDestroyed != 1 {
		t.Errorf("KeysDestroyed=%d want 1 (irreversible side effect happened)", result.KeysDestroyed)
	}
	if len(result.Skipped) != 1 || !contains(result.Skipped[0], "emit_error") {
		t.Errorf("Skipped=%v want emit_error entry", result.Skipped)
	}
}

func TestDEKDestructionWorkflow_zeroCutoffRejected(t *testing.T) {
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(DEKDestructionWorkflow)
	env.ExecuteWorkflow(DEKDestructionWorkflow, DEKDestructionInput{})
	if env.GetWorkflowError() == nil {
		t.Error("expected error when cutoff is zero")
	}
}

// contains is a tiny helper to avoid pulling strings into the test imports.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
