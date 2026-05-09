package appclient

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/gitscale-platform/gitscale/plane/application/policy"
)

// PolicyClient is the workflow-plane view of the application-plane policy
// engine (ADR-019). The workflow plane MUST NOT call plane/data/store from
// approval activities; every state change goes through this client which
// fans out to plane/application/policy.ApprovalService.
//
// The interface mirrors policy.ApprovalService. Implementations: a real
// gRPC client (filed as a follow-up) and StubPolicyClient (in-memory) for
// workflow unit tests.
type PolicyClient interface {
	SubmitPlan(ctx context.Context, plan *policy.Plan) (policy.Decision, error)
	RecordDecision(ctx context.Context, in policy.DecisionInput) (policy.DecisionOutcome, error)
	Escalate(ctx context.Context, in policy.EscalateInput) (policy.EscalateOutcome, error)
	GetPlanStatus(ctx context.Context, planID uuid.UUID) (policy.PlanStatus, error)
}

// StubPolicyClient records calls in memory and delegates to the supplied
// service. Used by workflow unit tests; production wires the gRPC client.
type StubPolicyClient struct {
	mu      sync.Mutex
	svc     policy.ApprovalService
	submits []policy.Plan
	escs    []policy.EscalateInput
	polls   []uuid.UUID
}

// NewStubPolicyClient wraps svc.
func NewStubPolicyClient(svc policy.ApprovalService) *StubPolicyClient {
	return &StubPolicyClient{svc: svc}
}

// SubmitPlan delegates to the wrapped service.
func (s *StubPolicyClient) SubmitPlan(ctx context.Context, plan *policy.Plan) (policy.Decision, error) {
	s.mu.Lock()
	s.submits = append(s.submits, *plan)
	s.mu.Unlock()
	return s.svc.SubmitPlan(ctx, plan)
}

// RecordDecision delegates to the wrapped service.
func (s *StubPolicyClient) RecordDecision(ctx context.Context, in policy.DecisionInput) (policy.DecisionOutcome, error) {
	return s.svc.RecordDecision(ctx, in)
}

// Escalate delegates to the wrapped service.
func (s *StubPolicyClient) Escalate(ctx context.Context, in policy.EscalateInput) (policy.EscalateOutcome, error) {
	s.mu.Lock()
	s.escs = append(s.escs, in)
	s.mu.Unlock()
	return s.svc.Escalate(ctx, in)
}

// GetPlanStatus delegates to the wrapped service.
func (s *StubPolicyClient) GetPlanStatus(ctx context.Context, planID uuid.UUID) (policy.PlanStatus, error) {
	s.mu.Lock()
	s.polls = append(s.polls, planID)
	s.mu.Unlock()
	return s.svc.GetPlanStatus(ctx, planID)
}

// SubmitCount returns the recorded SubmitPlan calls.
func (s *StubPolicyClient) SubmitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.submits)
}

// EscalateInputs returns a snapshot of all Escalate calls in order.
func (s *StubPolicyClient) EscalateInputs() []policy.EscalateInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]policy.EscalateInput(nil), s.escs...)
}

// PollCount returns the number of GetPlanStatus calls.
func (s *StubPolicyClient) PollCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.polls)
}
