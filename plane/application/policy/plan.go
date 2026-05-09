package policy

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ProposerKind is the closed enum identifying who submitted the plan. ADR-015
// allows agents and services to submit; only HumanUsers may decide.
type ProposerKind string

const (
	// ProposerKindAgent identifies an agent-proposed plan. AGENTS.md
	// "ask first" hints surface as agent-proposed plans.
	ProposerKindAgent ProposerKind = "agent"
	// ProposerKindService identifies a service-proposed plan (e.g. CI
	// pipeline requesting production-deploy approval).
	ProposerKindService ProposerKind = "service"
)

// IsValid reports whether k is one of the closed enum values.
func (k ProposerKind) IsValid() bool {
	switch k {
	case ProposerKindAgent, ProposerKindService:
		return true
	}
	return false
}

// Action is one item in a submitted plan. Kind names the operation type
// ("pr_merge", "force_push", "production_deploy", etc.) and Fields carries
// kind-specific keys ("branch", "ref", "environment", ...). Both are part of
// the canonical plan-hash; mutating either after approval re-opens the plan.
type Action struct {
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Plan is what an agent or service submits. The application plane stamps
// the ID and ComputeHash fills PlanHash before persistence.
type Plan struct {
	ID           uuid.UUID    `json:"id"`
	OrgID        uuid.UUID    `json:"org_id"`
	RepoID       *uuid.UUID   `json:"repo_id,omitempty"`
	PolicyID     uuid.UUID    `json:"policy_id"`
	ProposerID   uuid.UUID    `json:"proposer_id"`
	ProposerKind ProposerKind `json:"proposer_kind"`
	Actions      []Action     `json:"actions"`
	PlanHash     [32]byte     `json:"-"`
	CreatedAt    time.Time    `json:"created_at"`
}

// PlanStatus enumerates the lifecycle states of a Plan. Each transition
// emits an outbox row; consumers idempotent on event_id (ADR-008).
type PlanStatus string

const (
	PlanStatusPending             PlanStatus = "pending"
	PlanStatusApproved            PlanStatus = "approved"
	PlanStatusRejected            PlanStatus = "rejected"
	PlanStatusExpired             PlanStatus = "expired"
	PlanStatusEscalated           PlanStatus = "escalated"
	PlanStatusAutoApprovedNoRule  PlanStatus = "auto_approved_no_rule"
	PlanStatusAutoDenied          PlanStatus = "auto_denied"
)

// Decision is the engine's verdict for a SubmitPlan call. When AutoApproved
// is true, no human gating fired; the plan was admitted because no rule
// matched. When MatchedRuleIndex >= 0, callers route the plan through the
// EscalationLadder.
type Decision struct {
	PlanID           uuid.UUID
	Status           PlanStatus
	AutoApproved     bool
	MatchedRuleIndex int  // -1 when no rule matched
	Ladder           []EscalationRung
	ExpirySeconds    int
}

// CanonicalActionsJSON returns a stable JSON encoding of actions for hashing.
// Top-level actions slice order is preserved (caller-meaningful); within
// each action, Fields keys are sorted to remove map-iteration nondeterminism.
// The encoding rejects whitespace by using a custom emitter.
func CanonicalActionsJSON(actions []Action) ([]byte, error) {
	// Build a representation with sorted Fields per action.
	type canonAction struct {
		Kind   string            `json:"kind"`
		Fields map[string]string `json:"fields,omitempty"`
	}
	out := make([]canonAction, len(actions))
	for i, a := range actions {
		var fields map[string]string
		if len(a.Fields) > 0 {
			// Re-emit in sorted-key order via an intermediate slice.
			keys := make([]string, 0, len(a.Fields))
			for k := range a.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fields = make(map[string]string, len(keys))
			for _, k := range keys {
				fields[k] = a.Fields[k]
			}
		}
		out[i] = canonAction{Kind: a.Kind, Fields: fields}
	}
	// json.Marshal emits map keys in sorted order (encoding/json contract),
	// so the emitted bytes are deterministic given the sorted-input map.
	return json.Marshal(out)
}

// ComputePlanHash stamps PlanHash on p using the canonical encoding of
// p.Actions. Any subsequent mutation to Actions is detectable by re-running
// this function and comparing.
func ComputePlanHash(p *Plan) error {
	b, err := CanonicalActionsJSON(p.Actions)
	if err != nil {
		return err
	}
	p.PlanHash = sha256.Sum256(b)
	return nil
}
