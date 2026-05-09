package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Engine evaluates submitted plans against a Policy and returns a Decision.
// It is the in-process API exposed to the gRPC service and to in-process
// callers; the persistence concern (writing the source row + outbox) is
// pushed below into PlanRecorder.
//
// The Engine itself performs no I/O directly: it loads a Policy via
// PolicyLoader, evaluates predicates, and records the decision via
// PlanRecorder. All three injection points are interfaces (ADR-017) so
// tests can drive the engine without standing up a database.
type Engine struct {
	loader   PolicyLoader
	recorder PlanRecorder
}

// PolicyLoader returns the Policy applicable to the given org/repo pair.
// Implementations resolve repo-level policies before falling back to
// org-level (RepoID = nil).
type PolicyLoader interface {
	LoadPolicy(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) (*Policy, error)
}

// PlanRecorder persists a Plan and an audit row recording the engine's
// decision. Implementations MUST write both rows in the same SQL
// transaction along with the appropriate outbox row (ADR-008). The
// in-memory implementation used in tests is a no-op recorder.
type PlanRecorder interface {
	// RecordSubmission persists plan with status pending|auto_approved_no_rule
	// and writes the policy_audit row corresponding to the engine decision.
	// The same Tx writes the outbox event(s) per ADR-008.
	RecordSubmission(ctx context.Context, plan *Plan, decision Decision) error
}

// NewEngine wires loader and recorder into an Engine. Both are required;
// pass a stub recorder when only evaluation (not persistence) is needed.
func NewEngine(loader PolicyLoader, recorder PlanRecorder) *Engine {
	return &Engine{loader: loader, recorder: recorder}
}

// ErrPolicyNotFound is returned when no policy is configured for the
// (org_id, repo_id) pair. Callers map this to a 404 / NOT_FOUND. ADR-015
// forbids implicit auto-approve for missing policies; absence of a policy
// means the org has not yet onboarded the engine and submissions must
// fail rather than silently pass.
var ErrPolicyNotFound = errors.New("policy: no policy configured for org/repo")

// SubmitPlan evaluates plan against the applicable Policy and persists the
// resulting decision. Returns the Decision the caller should act on (route
// through the ladder, or treat as auto-admitted).
//
// Algorithm (ADR-015):
//  1. Load Policy by (OrgID, RepoID).
//  2. Validate the policy structurally (defence-in-depth).
//  3. Stamp PlanHash via ComputePlanHash.
//  4. Walk Rules in slice order; first match wins.
//  5. On match: persist plan with status=pending, decision carries the rung
//     ladder + expiry.
//  6. On no match: persist plan with status=auto_approved_no_rule, decision
//     carries AutoApproved=true. Audit row event_kind="auto_approved_no_rule"
//     emitted by the recorder. Ops dashboards alert when this rate exceeds
//     5%/hour (signal of mis-configuration).
func (e *Engine) SubmitPlan(ctx context.Context, plan *Plan) (Decision, error) {
	if plan == nil {
		return Decision{}, errors.New("policy: nil plan")
	}
	if !plan.ProposerKind.IsValid() {
		return Decision{}, fmt.Errorf("policy: invalid proposer_kind %q", plan.ProposerKind)
	}
	pol, err := e.loader.LoadPolicy(ctx, plan.OrgID, plan.RepoID)
	if err != nil {
		return Decision{}, err
	}
	if pol == nil {
		return Decision{}, ErrPolicyNotFound
	}
	if err := Validate(pol); err != nil {
		return Decision{}, fmt.Errorf("policy: loaded policy fails validation: %w", err)
	}
	plan.PolicyID = pol.ID
	if err := ComputePlanHash(plan); err != nil {
		return Decision{}, err
	}
	if plan.ID == uuid.Nil {
		plan.ID = uuid.New()
	}

	for i, r := range pol.Rules {
		if matchRule(r, plan.Actions, plan.ProposerKind) {
			d := Decision{
				PlanID:           plan.ID,
				Status:           PlanStatusPending,
				AutoApproved:     false,
				MatchedRuleIndex: i,
				Ladder:           append([]EscalationRung(nil), r.Ladder...),
				ExpirySeconds:    r.ExpirySeconds,
			}
			if err := e.recorder.RecordSubmission(ctx, plan, d); err != nil {
				return Decision{}, err
			}
			return d, nil
		}
	}

	// No rule matched. ADR-015 forbids silent admission: emit an explicit
	// auto_approved_no_rule audit row. Operators alert on the rate.
	d := Decision{
		PlanID:           plan.ID,
		Status:           PlanStatusAutoApprovedNoRule,
		AutoApproved:     true,
		MatchedRuleIndex: -1,
	}
	if err := e.recorder.RecordSubmission(ctx, plan, d); err != nil {
		return Decision{}, err
	}
	return d, nil
}
