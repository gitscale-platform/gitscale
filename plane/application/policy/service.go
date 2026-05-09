package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// API error codes returned at the gRPC / REST boundary. Closed enum stable
// across the wire surfaces; consumers map to user-facing copy.
const (
	CodePlanAlreadyDecided      = "plan_already_decided"
	CodePlanExpired             = "plan_expired"
	CodeDecisionUnauthorized    = "decision_unauthorized"
	CodeDecisionQuorumAlreadyMet = "decision_quorum_already_met"
	CodePlanNotFound            = "plan_not_found"
	CodeRungOutOfRange          = "rung_out_of_range"
	CodeFinalRungReached        = "final_rung_reached"
)

// APIError carries a closed-enum code and a contextual message. Mapped to
// the appropriate gRPC status / HTTP status by the boundary handlers.
type APIError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("policy: %s: %s", e.Code, e.Message)
}

// IsAPICode reports whether err (or any wrapped error) is an APIError with
// the given code. Used by boundary handlers and by tests.
func IsAPICode(err error, code string) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Code == code
}

// DecisionInput records a single human approver's verdict on a plan.
// Approve=true means the approver has voted to approve; false means
// reject. Reason is required for rejections (enforced by the service);
// optional for approvals.
type DecisionInput struct {
	PlanID    uuid.UUID
	ActorID   uuid.UUID  // HumanUser principal
	Approve   bool
	Reason    string
	RungIndex int        // the rung this decision applies to
}

// DecisionOutcome reports the post-decision state of the plan.
type DecisionOutcome struct {
	Status     PlanStatus
	QuorumMet  bool
	Approvals  int
	Rejections int
}

// EscalateInput moves the plan to the next rung when the current one's
// SLA expired. Caller is the workflow plane (ApprovalActivity); the
// service emits the corresponding outbox + audit row.
type EscalateInput struct {
	PlanID      uuid.UUID
	FromRung    int
	Reason      string // 'sla_breach' | 'fall_back'
}

// EscalateOutcome reports the next rung index and whether the ladder is
// now exhausted (final_rung_reached → caller decides per OnTimeout).
type EscalateOutcome struct {
	NewRung      int
	OnTimeout    OnTimeout // OnTimeout of the rung that just elapsed
	FinalRung    bool      // true when no further rung exists
}

// ApprovalService is the application-plane API the workflow plane consumes.
// In-process callers (REST handlers from #111 follow-up) and the gRPC
// service surface both delegate here.
//
// Implementations MUST persist every state change inside a single SQL
// transaction with the corresponding outbox row and (where applicable) a
// policy_audit row chained off the latest row for the policy (ADR-008,
// ADR-015 audit chain).
type ApprovalService interface {
	// SubmitPlan invokes the engine, persists the plan + audit row, and
	// returns the Decision the workflow should act on.
	SubmitPlan(ctx context.Context, plan *Plan) (Decision, error)

	// RecordDecision records a single human approver's verdict on a plan.
	// Returns the post-decision plan status. Caller's authorisation
	// (membership in the rung's ApproverGroup) is the boundary handler's
	// responsibility; the service trusts the ActorID claim presented to
	// it (ADR-010 SVID).
	RecordDecision(ctx context.Context, in DecisionInput) (DecisionOutcome, error)

	// Escalate advances the plan to the next rung. Returns final_rung=true
	// when no further rung exists; caller (ApprovalActivity) then applies
	// the elapsed rung's OnTimeout.
	Escalate(ctx context.Context, in EscalateInput) (EscalateOutcome, error)

	// GetPlanStatus returns the current plan status. The workflow plane's
	// ApprovalActivity polls this between escalations; in-process callers
	// use it for read-after-write checks. Stateless; safe to call outside
	// any Tx.
	GetPlanStatus(ctx context.Context, planID uuid.UUID) (PlanStatus, error)
}

// AlreadyDecided is returned (wrapped in APIError{CodePlanAlreadyDecided})
// when RecordDecision or Escalate observes a non-pending plan. ADR-015:
// no double-decision once approved/rejected/expired/auto_*.
var AlreadyDecided = errors.New("policy: plan already decided")

// NewAPIError constructs an APIError. Convenience for service implementers.
func NewAPIError(code, msg string) *APIError {
	return &APIError{Code: code, Message: msg}
}
