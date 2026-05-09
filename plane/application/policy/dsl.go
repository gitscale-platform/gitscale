package policy

import (
	"github.com/google/uuid"
)

// PredicateKind enumerates the predicate matchers the Engine knows how to
// evaluate. The set is closed: adding a kind requires a migration and an
// ADR-015 amendment. The string form is the canonical wire encoding inside
// Policy.Rules[].Kind and is what consumers index on.
type PredicateKind string

const (
	// PredicatePRMerge matches PR-merge actions. The Match key "branch" is
	// compared (exact, case-sensitive) to the action's target branch.
	PredicatePRMerge PredicateKind = "pr_merge"

	// PredicateForcePush matches a force-push to a ref. The Match key
	// "ref_pattern" is treated as a glob and compared to the ref name.
	PredicateForcePush PredicateKind = "force_push"

	// PredicateProductionDeploy matches a production-tier deployment. The
	// Match key "environment" is compared (exact) to the action's
	// environment field. ADR-015 designates this kind as the security
	// class with the 4-hour default approval expiry.
	PredicateProductionDeploy PredicateKind = "production_deploy"

	// PredicateBulkAction matches when an action set's cardinality meets or
	// exceeds Rule.Threshold. Used to gate fan-out operations (e.g. closing
	// >=N issues in one batch).
	PredicateBulkAction PredicateKind = "bulk_action"

	// PredicateAgentDefault is the catch-all for AGENTS.md "ask first" hints.
	// It matches any agent-proposed plan when no earlier rule fires; per
	// first-match-wins semantics it must be the LAST rule in the list, or it
	// will pre-empt more specific rules.
	PredicateAgentDefault PredicateKind = "agent_default"
)

// AllPredicateKinds returns the closed set of predicate kinds the engine
// understands. Validation uses this set; any kind not present here is
// rejected as policy_invalid_predicate_kind.
func AllPredicateKinds() []PredicateKind {
	return []PredicateKind{
		PredicatePRMerge,
		PredicateForcePush,
		PredicateProductionDeploy,
		PredicateBulkAction,
		PredicateAgentDefault,
	}
}

// IsValid reports whether k is one of the closed enum values.
func (k PredicateKind) IsValid() bool {
	for _, v := range AllPredicateKinds() {
		if v == k {
			return true
		}
	}
	return false
}

// OnTimeout enumerates the disposition of an unresolved escalation rung when
// its SLA elapses without a quorum decision.
type OnTimeout string

const (
	// OnTimeoutNotifyNext escalates to the next rung.
	OnTimeoutNotifyNext OnTimeout = "notify_next"
	// OnTimeoutAutoDeny rejects the plan when this rung's SLA elapses.
	OnTimeoutAutoDeny OnTimeout = "auto_deny"
	// OnTimeoutFallBack returns to the previous rung. Only valid for rungs
	// after the first; the first rung cannot fall back.
	OnTimeoutFallBack OnTimeout = "fall_back"
)

// IsValid reports whether o is one of the closed enum values.
func (o OnTimeout) IsValid() bool {
	switch o {
	case OnTimeoutNotifyNext, OnTimeoutAutoDeny, OnTimeoutFallBack:
		return true
	}
	return false
}

// Policy is the org/repo-scoped, signed, versioned plan-approval policy.
// Rules are evaluated in slice order with first-match-wins semantics.
// ApproverGroups are referenced by name from each rung's GroupName.
type Policy struct {
	ID             uuid.UUID                `json:"id"`
	OrgID          uuid.UUID                `json:"org_id"`
	RepoID         *uuid.UUID               `json:"repo_id,omitempty"`
	Name           string                   `json:"name"`
	Version        int                      `json:"version"`
	Rules          []Rule                   `json:"rules"`
	ApproverGroups map[string]ApproverGroup `json:"approver_groups"`
}

// Rule pairs a predicate matcher with the escalation ladder that fires when
// the predicate matches. ExpirySeconds bounds how long a granted approval
// remains valid; a plan whose hash mutates after approval re-opens the
// approval cycle (ADR-015 plan-hash binding).
type Rule struct {
	Kind          PredicateKind     `json:"kind"`
	Match         map[string]string `json:"match,omitempty"`
	Threshold     *int              `json:"threshold,omitempty"`
	Ladder        []EscalationRung  `json:"ladder"`
	ExpirySeconds int               `json:"expiry_seconds"`
}

// ApproverGroup is a k-of-n named group of HumanUser principals. ADR-015
// forbids non-human approvers; validation enforces this at the type level.
type ApproverGroup struct {
	Name          string      `json:"name"`
	HumanUserIDs  []uuid.UUID `json:"human_user_ids"`
	RequiredCount int         `json:"required_count"`
}

// EscalationRung is one step of a Rule's ladder. GroupName must reference a
// key in the parent Policy.ApproverGroups map. SLASeconds bounds the wait at
// this rung before OnTimeout fires.
type EscalationRung struct {
	GroupName  string    `json:"group_name"`
	SLASeconds int       `json:"sla_seconds"`
	OnTimeout  OnTimeout `json:"on_timeout"`
}
