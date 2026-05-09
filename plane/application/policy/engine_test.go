package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// stubLoader returns a fixed policy or error.
type stubLoader struct {
	policy *Policy
	err    error
}

func (s *stubLoader) LoadPolicy(_ context.Context, _ uuid.UUID, _ *uuid.UUID) (*Policy, error) {
	return s.policy, s.err
}

// captureRecorder records the last RecordSubmission call.
type captureRecorder struct {
	plan     *Plan
	decision Decision
	calls    int
	failWith error
}

func (c *captureRecorder) RecordSubmission(_ context.Context, plan *Plan, d Decision) error {
	c.calls++
	c.plan = plan
	c.decision = d
	return c.failWith
}

// engineWith returns an Engine wired to a fresh policy and capture recorder.
func engineWith(t *testing.T, p *Policy) (*Engine, *captureRecorder) {
	t.Helper()
	rec := &captureRecorder{}
	return NewEngine(&stubLoader{policy: p}, rec), rec
}

func TestSubmitPlan_PRMergeMatch(t *testing.T) {
	e, rec := engineWith(t, validPolicy())
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerID:   uuid.New(),
		ProposerKind: ProposerKindAgent,
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if d.MatchedRuleIndex != 0 {
		t.Errorf("want rule[0] match, got %d", d.MatchedRuleIndex)
	}
	if d.Status != PlanStatusPending {
		t.Errorf("want pending, got %s", d.Status)
	}
	if d.AutoApproved {
		t.Error("PR-merge match must not auto-approve")
	}
	if rec.calls != 1 {
		t.Errorf("recorder calls: want 1 got %d", rec.calls)
	}
}

func TestSubmitPlan_PRMergeNonMatchingBranch(t *testing.T) {
	e, _ := engineWith(t, validPolicy())
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindService,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "feature-x"}}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// Service proposer + non-main branch + no agent_default rule → no match.
	if !d.AutoApproved {
		t.Errorf("want auto-approved-no-rule, got %+v", d)
	}
	if d.Status != PlanStatusAutoApprovedNoRule {
		t.Errorf("want auto_approved_no_rule, got %s", d.Status)
	}
	if d.MatchedRuleIndex != -1 {
		t.Errorf("want -1, got %d", d.MatchedRuleIndex)
	}
}

func TestSubmitPlan_BulkActionThreshold(t *testing.T) {
	e, _ := engineWith(t, validPolicy())
	actions := make([]Action, 60)
	for i := range actions {
		actions[i] = Action{Kind: "issue_close"}
	}
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      actions,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if d.MatchedRuleIndex != 1 {
		t.Errorf("want rule[1] (bulk_action), got %d", d.MatchedRuleIndex)
	}
}

func TestSubmitPlan_AgentDefaultLastResort(t *testing.T) {
	p := validPolicy()
	// Append agent_default catch-all so an agent-proposed unrelated action
	// still gets gated.
	p.Rules = append(p.Rules, Rule{
		Kind:          PredicateAgentDefault,
		ExpirySeconds: 86400,
		Ladder: []EscalationRung{
			{GroupName: "oncall", SLASeconds: 3600, OnTimeout: OnTimeoutAutoDeny},
		},
	})
	e, _ := engineWith(t, p)
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "rename_file"}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if d.MatchedRuleIndex != 2 {
		t.Errorf("want rule[2] (agent_default), got %d", d.MatchedRuleIndex)
	}
	if d.AutoApproved {
		t.Error("agent_default match must NOT auto-approve")
	}
}

func TestSubmitPlan_AgentDefaultIgnoresService(t *testing.T) {
	p := validPolicy()
	p.Rules = append(p.Rules, Rule{
		Kind:          PredicateAgentDefault,
		ExpirySeconds: 86400,
		Ladder: []EscalationRung{
			{GroupName: "oncall", SLASeconds: 3600, OnTimeout: OnTimeoutAutoDeny},
		},
	})
	e, _ := engineWith(t, p)
	// Service proposer with no matching kinds: agent_default must not fire.
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindService,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "rename_file"}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !d.AutoApproved {
		t.Errorf("service proposer + no match → expected auto-approved-no-rule, got %+v", d)
	}
}

func TestSubmitPlan_FirstMatchWins(t *testing.T) {
	// Build a policy where rule[0] is pr_merge main and rule[1] is agent_default.
	// An agent proposing pr_merge to main must hit rule[0] (more specific).
	p := validPolicy()
	p.Rules = append(p.Rules, Rule{
		Kind:          PredicateAgentDefault,
		ExpirySeconds: 86400,
		Ladder: []EscalationRung{
			{GroupName: "oncall", SLASeconds: 3600, OnTimeout: OnTimeoutAutoDeny},
		},
	})
	e, _ := engineWith(t, p)
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if d.MatchedRuleIndex != 0 {
		t.Errorf("first-match-wins broken: want 0, got %d", d.MatchedRuleIndex)
	}
}

func TestSubmitPlan_ForcePushPattern(t *testing.T) {
	p := &Policy{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Name:  "force",
		ApproverGroups: map[string]ApproverGroup{
			"oncall": {Name: "oncall", HumanUserIDs: []uuid.UUID{uuid.New()}, RequiredCount: 1},
		},
		Rules: []Rule{
			{
				Kind:          PredicateForcePush,
				Match:         map[string]string{"ref_pattern": "refs/heads/release/*"},
				ExpirySeconds: 86400,
				Ladder: []EscalationRung{
					{GroupName: "oncall", SLASeconds: 60, OnTimeout: OnTimeoutAutoDeny},
				},
			},
		},
	}
	e, _ := engineWith(t, p)
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "force_push", Fields: map[string]string{"ref": "refs/heads/release/v1"}}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if d.MatchedRuleIndex != 0 {
		t.Errorf("force_push glob did not match: %+v", d)
	}
}

func TestSubmitPlan_ProductionDeploy(t *testing.T) {
	p := &Policy{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Name:  "prod",
		ApproverGroups: map[string]ApproverGroup{
			"sec": {Name: "sec", HumanUserIDs: []uuid.UUID{uuid.New(), uuid.New()}, RequiredCount: 2},
		},
		Rules: []Rule{{
			Kind:          PredicateProductionDeploy,
			Match:         map[string]string{"environment": "prod"},
			ExpirySeconds: 14400,
			Ladder: []EscalationRung{
				{GroupName: "sec", SLASeconds: 14400, OnTimeout: OnTimeoutAutoDeny},
			},
		}},
	}
	e, _ := engineWith(t, p)
	d, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindService,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "production_deploy", Fields: map[string]string{"environment": "prod"}}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if d.MatchedRuleIndex != 0 || d.ExpirySeconds != 14400 {
		t.Errorf("expected 4h prod gate, got %+v", d)
	}
}

func TestSubmitPlan_NoPolicyConfigured(t *testing.T) {
	e := NewEngine(&stubLoader{policy: nil}, &captureRecorder{})
	_, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
	})
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("want ErrPolicyNotFound, got %v", err)
	}
}

func TestSubmitPlan_RecorderError(t *testing.T) {
	rec := &captureRecorder{failWith: errors.New("db down")}
	e := NewEngine(&stubLoader{policy: validPolicy()}, rec)
	_, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err == nil {
		t.Fatal("expected error from recorder, got nil")
	}
}

func TestSubmitPlan_InvalidProposerKind(t *testing.T) {
	e := NewEngine(&stubLoader{policy: validPolicy()}, &captureRecorder{})
	_, err := e.SubmitPlan(context.Background(), &Plan{
		OrgID:        uuid.New(),
		ProposerKind: ProposerKind("alien"),
		ProposerID:   uuid.New(),
	})
	if err == nil {
		t.Fatal("expected error for invalid proposer_kind")
	}
}

func TestComputePlanHash_StableAcrossFieldOrder(t *testing.T) {
	a := &Plan{Actions: []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main", "repo": "r1"}}}}
	b := &Plan{Actions: []Action{{Kind: "pr_merge", Fields: map[string]string{"repo": "r1", "branch": "main"}}}}
	if err := ComputePlanHash(a); err != nil {
		t.Fatal(err)
	}
	if err := ComputePlanHash(b); err != nil {
		t.Fatal(err)
	}
	if a.PlanHash != b.PlanHash {
		t.Errorf("hash should be stable across map iteration order, got %x vs %x", a.PlanHash, b.PlanHash)
	}
}

func TestComputePlanHash_DiffersOnMutation(t *testing.T) {
	a := &Plan{Actions: []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}}}
	b := &Plan{Actions: []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "develop"}}}}
	_ = ComputePlanHash(a)
	_ = ComputePlanHash(b)
	if a.PlanHash == b.PlanHash {
		t.Error("hash must change when actions change")
	}
}
