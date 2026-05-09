package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemService is an in-memory ApprovalService used in tests and as the
// reference implementation for the PG-backed service that follows in a
// later issue. It threads the engine, plan store, and audit chain through
// a single mutex; production reaches the same call shape via SQL Tx.
//
// Concurrency: every method takes the mutex for its full duration. This is
// not the production threading model — the PG impl uses Tx isolation —
// but it is sufficient for unit and ApprovalActivity tests in this issue.
type MemService struct {
	mu       sync.Mutex
	policies map[uuid.UUID]*Policy
	plans    map[uuid.UUID]*memPlan
	audit    map[uuid.UUID][]AuditRow // policyID → rows
	clock    func() time.Time
}

type memPlan struct {
	plan        Plan
	status      PlanStatus
	currentRung int
	approvals   map[int]map[uuid.UUID]bool // rung → approver → approved
	rejections  map[int]map[uuid.UUID]bool
	ladder      []EscalationRung
	policy      *Policy
	expiresAt   time.Time
}

// NewMemService returns an empty in-memory ApprovalService. clock is
// optional; nil falls back to time.Now (UTC).
func NewMemService(clock func() time.Time) *MemService {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &MemService{
		policies: map[uuid.UUID]*Policy{},
		plans:    map[uuid.UUID]*memPlan{},
		audit:    map[uuid.UUID][]AuditRow{},
		clock:    clock,
	}
}

// RegisterPolicy seeds a policy. The PG impl persists via Tx.
func (s *MemService) RegisterPolicy(p *Policy) error {
	if err := Validate(p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[p.OrgID] = p
	return nil
}

// LoadPolicy implements PolicyLoader.
func (s *MemService) LoadPolicy(_ context.Context, orgID uuid.UUID, _ *uuid.UUID) (*Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[orgID]
	if !ok {
		return nil, nil
	}
	return p, nil
}

// RecordSubmission implements PlanRecorder. It is called by the engine
// inside SubmitPlan with the mutex already taken (see SubmitPlan below),
// so we do not re-take it here.
func (s *MemService) RecordSubmission(_ context.Context, plan *Plan, d Decision) error {
	pol := s.policies[plan.OrgID]
	if pol == nil {
		return ErrPolicyNotFound
	}
	now := s.clock()
	mp := &memPlan{
		plan:        *plan,
		status:      d.Status,
		currentRung: 0,
		approvals:   map[int]map[uuid.UUID]bool{},
		rejections:  map[int]map[uuid.UUID]bool{},
		ladder:      d.Ladder,
		policy:      pol,
	}
	if d.ExpirySeconds > 0 {
		mp.expiresAt = now.Add(time.Duration(d.ExpirySeconds) * time.Second)
	}
	s.plans[plan.ID] = mp

	kind := AuditEventSubmitted
	if d.AutoApproved {
		kind = AuditEventAutoApprovedNoRule
	}
	payload, _ := json.Marshal(map[string]any{
		"plan_id":            plan.ID.String(),
		"matched_rule_index": d.MatchedRuleIndex,
		"plan_hash":          fmt.Sprintf("%x", plan.PlanHash),
		"proposer_kind":      string(plan.ProposerKind),
	})
	return s.appendAuditLocked(pol.ID, &plan.ID, kind, &plan.ProposerID, actorKindFromProposer(plan.ProposerKind), payload, now)
}

// SubmitPlan implements ApprovalService.
func (s *MemService) SubmitPlan(ctx context.Context, plan *Plan) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Engine.SubmitPlan calls back into LoadPolicy and RecordSubmission;
	// to avoid mutex re-entry we inline a minimal evaluation that mirrors
	// engine semantics and shares the predicate logic via matchRule.
	if plan == nil {
		return Decision{}, fmt.Errorf("policy: nil plan")
	}
	if !plan.ProposerKind.IsValid() {
		return Decision{}, fmt.Errorf("policy: invalid proposer_kind %q", plan.ProposerKind)
	}
	pol, ok := s.policies[plan.OrgID]
	if !ok {
		return Decision{}, ErrPolicyNotFound
	}
	if err := Validate(pol); err != nil {
		return Decision{}, err
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
				MatchedRuleIndex: i,
				Ladder:           append([]EscalationRung(nil), r.Ladder...),
				ExpirySeconds:    r.ExpirySeconds,
			}
			if err := s.RecordSubmission(ctx, plan, d); err != nil {
				return Decision{}, err
			}
			return d, nil
		}
	}
	d := Decision{
		PlanID:           plan.ID,
		Status:           PlanStatusAutoApprovedNoRule,
		AutoApproved:     true,
		MatchedRuleIndex: -1,
	}
	if err := s.RecordSubmission(ctx, plan, d); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// RecordDecision implements ApprovalService.
func (s *MemService) RecordDecision(_ context.Context, in DecisionInput) (DecisionOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.plans[in.PlanID]
	if !ok {
		return DecisionOutcome{}, NewAPIError(CodePlanNotFound, "no such plan")
	}
	if mp.status != PlanStatusPending {
		return DecisionOutcome{}, NewAPIError(CodePlanAlreadyDecided, fmt.Sprintf("status=%s", mp.status))
	}
	now := s.clock()
	if !mp.expiresAt.IsZero() && now.After(mp.expiresAt) {
		mp.status = PlanStatusExpired
		_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventExpired, nil, ActorKindSystem,
			rawJSON(map[string]any{"reason": "expiry_window_elapsed"}), now)
		return DecisionOutcome{}, NewAPIError(CodePlanExpired, "approval window elapsed")
	}
	if in.RungIndex < 0 || in.RungIndex >= len(mp.ladder) {
		return DecisionOutcome{}, NewAPIError(CodeRungOutOfRange, fmt.Sprintf("rung=%d", in.RungIndex))
	}
	if in.RungIndex != mp.currentRung {
		return DecisionOutcome{}, NewAPIError(CodeRungOutOfRange,
			fmt.Sprintf("decision rung %d != current rung %d", in.RungIndex, mp.currentRung))
	}
	rung := mp.ladder[mp.currentRung]
	group, ok := mp.policy.ApproverGroups[rung.GroupName]
	if !ok {
		return DecisionOutcome{}, NewAPIError(CodeDecisionUnauthorized, "rung group missing")
	}
	if !contains(group.HumanUserIDs, in.ActorID) {
		return DecisionOutcome{}, NewAPIError(CodeDecisionUnauthorized, "actor not in approver group")
	}
	if in.Approve {
		if mp.approvals[in.RungIndex] == nil {
			mp.approvals[in.RungIndex] = map[uuid.UUID]bool{}
		}
		mp.approvals[in.RungIndex][in.ActorID] = true
	} else {
		if in.Reason == "" {
			return DecisionOutcome{}, NewAPIError(CodeDecisionUnauthorized, "reject requires reason")
		}
		if mp.rejections[in.RungIndex] == nil {
			mp.rejections[in.RungIndex] = map[uuid.UUID]bool{}
		}
		mp.rejections[in.RungIndex][in.ActorID] = true
	}
	approvals := len(mp.approvals[in.RungIndex])
	rejections := len(mp.rejections[in.RungIndex])
	out := DecisionOutcome{
		Approvals:  approvals,
		Rejections: rejections,
	}
	// Reject as soon as any rejection appears — single rejection at any
	// rung blocks the plan (ADR-015 conservative posture).
	if rejections > 0 {
		mp.status = PlanStatusRejected
		out.Status = PlanStatusRejected
		actor := in.ActorID
		_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventRejected, &actor, ActorKindHuman,
			rawJSON(map[string]any{"rung": in.RungIndex, "reason": in.Reason}), now)
		return out, nil
	}
	if approvals >= group.RequiredCount {
		mp.status = PlanStatusApproved
		out.Status = PlanStatusApproved
		out.QuorumMet = true
		actor := in.ActorID
		_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventApproved, &actor, ActorKindHuman,
			rawJSON(map[string]any{"rung": in.RungIndex, "approvals": approvals}), now)
		return out, nil
	}
	out.Status = PlanStatusPending
	actor := in.ActorID
	_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventSubmitted, &actor, ActorKindHuman,
		rawJSON(map[string]any{"rung": in.RungIndex, "vote": "approve_partial", "approvals": approvals}), now)
	return out, nil
}

// Escalate implements ApprovalService.
func (s *MemService) Escalate(_ context.Context, in EscalateInput) (EscalateOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.plans[in.PlanID]
	if !ok {
		return EscalateOutcome{}, NewAPIError(CodePlanNotFound, "no such plan")
	}
	if mp.status != PlanStatusPending {
		return EscalateOutcome{}, NewAPIError(CodePlanAlreadyDecided, fmt.Sprintf("status=%s", mp.status))
	}
	if in.FromRung != mp.currentRung {
		return EscalateOutcome{}, NewAPIError(CodeRungOutOfRange,
			fmt.Sprintf("escalate from %d but current is %d", in.FromRung, mp.currentRung))
	}
	rung := mp.ladder[mp.currentRung]
	now := s.clock()
	out := EscalateOutcome{OnTimeout: rung.OnTimeout}
	switch rung.OnTimeout {
	case OnTimeoutAutoDeny:
		mp.status = PlanStatusAutoDenied
		out.NewRung = mp.currentRung
		out.FinalRung = true
		_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventAutoDenied, nil, ActorKindSystem,
			rawJSON(map[string]any{"rung": mp.currentRung, "reason": in.Reason}), now)
	case OnTimeoutFallBack:
		// fall_back is invalid on rung 0 (validation rejects), so this is safe.
		mp.currentRung--
		out.NewRung = mp.currentRung
		_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventEscalated, nil, ActorKindSystem,
			rawJSON(map[string]any{"from_rung": in.FromRung, "to_rung": mp.currentRung, "kind": "fall_back"}), now)
	case OnTimeoutNotifyNext:
		next := mp.currentRung + 1
		if next >= len(mp.ladder) {
			// Final rung exhausted with notify_next semantics: ADR-015
			// requires explicit final disposition. Treat as auto_denied
			// — the operator should configure auto_deny on the final
			// rung; we surface FinalRung=true so the caller can act.
			mp.status = PlanStatusAutoDenied
			out.NewRung = mp.currentRung
			out.FinalRung = true
			_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventAutoDenied, nil, ActorKindSystem,
				rawJSON(map[string]any{"rung": mp.currentRung, "reason": "ladder_exhausted"}), now)
		} else {
			mp.currentRung = next
			mp.status = PlanStatusPending // remain pending
			out.NewRung = next
			_ = s.appendAuditLocked(mp.policy.ID, &mp.plan.ID, AuditEventEscalated, nil, ActorKindSystem,
				rawJSON(map[string]any{"from_rung": in.FromRung, "to_rung": next, "kind": "notify_next"}), now)
		}
	}
	return out, nil
}

// GetPlanStatus implements ApprovalService.
func (s *MemService) GetPlanStatus(_ context.Context, planID uuid.UUID) (PlanStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.plans[planID]
	if !ok {
		return "", NewAPIError(CodePlanNotFound, "no such plan")
	}
	return mp.status, nil
}

// AuditChain returns the audit rows for a policy in append order. Test-only
// access; production callers go through a dedicated read API.
func (s *MemService) AuditChain(policyID uuid.UUID) []AuditRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditRow, len(s.audit[policyID]))
	copy(out, s.audit[policyID])
	return out
}

// appendAuditLocked appends an audit row under the assumption that mu is
// already held by the caller.
func (s *MemService) appendAuditLocked(policyID uuid.UUID, planID *uuid.UUID, kind AuditEventKind, actorID *uuid.UUID, actorKind ActorKind, payload []byte, now time.Time) error {
	prev := GenesisHash
	if rows := s.audit[policyID]; len(rows) > 0 {
		prev = rows[len(rows)-1].RowHash
	}
	row := AuditRow{
		ID:        int64(len(s.audit[policyID]) + 1),
		PolicyID:  policyID,
		PlanID:    planID,
		EventKind: kind,
		ActorID:   actorID,
		ActorKind: actorKind,
		Payload:   payload,
		CreatedAt: now,
	}
	if err := AppendRow(prev, &row); err != nil {
		return err
	}
	s.audit[policyID] = append(s.audit[policyID], row)
	return nil
}

// rawJSON serialises v ignoring errors. Audit payloads are simple maps; the
// test helper stays terse to keep call sites readable.
func rawJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func contains(haystack []uuid.UUID, needle uuid.UUID) bool {
	for _, id := range haystack {
		if id == needle {
			return true
		}
	}
	return false
}

func actorKindFromProposer(p ProposerKind) ActorKind {
	switch p {
	case ProposerKindAgent:
		return ActorKindAgent
	case ProposerKindService:
		return ActorKindService
	}
	return ActorKindSystem
}
