package billing

import (
	"context"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/activity"
)

// ActivityNameRequestOperatorApproval is the registered name for the
// RequestOperatorApprovalActivity.Execute method. The DEK destruction
// workflow gates the irreversible Vault transit trim behind this activity.
const ActivityNameRequestOperatorApproval = "billing.RequestOperatorApprovalForDEKDestroy"

// OperatorApprovalInput identifies the partition awaiting approval.
type OperatorApprovalInput struct {
	Year          int
	Month         int
	PartitionName string
	KEKHint       string
}

// OperatorApprovalResult is the boundary's verdict.
type OperatorApprovalResult struct {
	Approved bool
	// Reason is filled when Approved is false; it propagates as the
	// workflow's skip reason for the partition.
	Reason string
}

// OperatorApprover is the workflow-plane abstraction over the ADR-015
// operator-approval mechanism. The mechanism itself (UI, gRPC waiting list,
// signal-driven workflow, etc.) is not yet implemented at the time of #80;
// production wires whichever implementation lands first.
type OperatorApprover interface {
	RequestApproval(ctx context.Context, in OperatorApprovalInput) (OperatorApprovalResult, error)
}

// RequestOperatorApprovalActivity wraps an OperatorApprover so the
// workflow can call the boundary as a Temporal activity. Long-blocking
// human-in-the-loop approvers should configure the activity with a long
// StartToCloseTimeout and rely on heartbeats rather than expanding the
// boundary surface here.
type RequestOperatorApprovalActivity struct {
	approver OperatorApprover
}

// NewRequestOperatorApprovalActivity wraps approver.
func NewRequestOperatorApprovalActivity(approver OperatorApprover) *RequestOperatorApprovalActivity {
	return &RequestOperatorApprovalActivity{approver: approver}
}

// Execute requests approval for in. The activity surfaces the verdict
// rather than failing on rejection; the workflow records a skip on a non-
// approved verdict so a single rejected partition does not abort the whole
// run.
func (a *RequestOperatorApprovalActivity) Execute(ctx context.Context, in OperatorApprovalInput) (OperatorApprovalResult, error) {
	if a.approver == nil {
		return OperatorApprovalResult{}, fmt.Errorf("billing.RequestOperatorApproval: approver is nil")
	}
	return a.approver.RequestApproval(ctx, in)
}

// AutoApproveStub auto-approves every request and logs the event via the
// activity logger. Used until the real ADR-015 approval mechanism lands.
//
// TODO(ADR-015): replace with a signal-driven approval workflow or a thin
// gRPC client to the operator-approval service. The runbook
// (docs/runbooks/billing-dek-destruction.md) documents this gap and the
// operator-side mitigation: pause the DEK destruction schedule and audit
// recent emit events whenever the auto-approve stub is enabled.
type AutoApproveStub struct{}

// NewAutoApproveStub returns the auto-approving stub.
func NewAutoApproveStub() AutoApproveStub { return AutoApproveStub{} }

// RequestApproval logs the approval event via the Temporal activity logger
// and returns Approved=true. Workflow tests can substitute a mock
// OperatorApprover to exercise the rejection path without touching this
// stub.
func (AutoApproveStub) RequestApproval(ctx context.Context, in OperatorApprovalInput) (OperatorApprovalResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Warn("DEK-destruction operator-approval auto-approved (ADR-015 stub)",
		slog.Int("year", in.Year),
		slog.Int("month", in.Month),
		slog.String("partition_name", in.PartitionName),
		slog.String("kek_hint", in.KEKHint),
	)
	return OperatorApprovalResult{Approved: true, Reason: ""}, nil
}
