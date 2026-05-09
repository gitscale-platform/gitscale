package policy

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// makeService builds a MemService seeded with a 2-rung policy on org X
// requiring 1 approval at rung 0 (sla 60s, notify_next), and 2 approvals
// at rung 1 (sla 30s, auto_deny).
func makeService(t *testing.T, clock func() time.Time) (*MemService, *Policy, []uuid.UUID) {
	t.Helper()
	approver1 := uuid.New()
	approver2 := uuid.New()
	approver3 := uuid.New()
	p := &Policy{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Name:  "default",
		ApproverGroups: map[string]ApproverGroup{
			"oncall":   {Name: "oncall", HumanUserIDs: []uuid.UUID{approver1}, RequiredCount: 1},
			"security": {Name: "security", HumanUserIDs: []uuid.UUID{approver2, approver3}, RequiredCount: 2},
		},
		Rules: []Rule{
			{
				Kind:          PredicatePRMerge,
				Match:         map[string]string{"branch": "main"},
				ExpirySeconds: 86400,
				Ladder: []EscalationRung{
					{GroupName: "oncall", SLASeconds: 60, OnTimeout: OnTimeoutNotifyNext},
					{GroupName: "security", SLASeconds: 30, OnTimeout: OnTimeoutAutoDeny},
				},
			},
		},
	}
	svc := NewMemService(clock)
	if err := svc.RegisterPolicy(p); err != nil {
		t.Fatal(err)
	}
	return svc, p, []uuid.UUID{approver1, approver2, approver3}
}

func TestMem_SubmitThenApproveRung0(t *testing.T) {
	svc, p, approvers := makeService(t, nil)
	d, err := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != PlanStatusPending {
		t.Fatalf("want pending, got %s", d.Status)
	}
	out, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID:    d.PlanID,
		ActorID:   approvers[0],
		Approve:   true,
		RungIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != PlanStatusApproved || !out.QuorumMet {
		t.Errorf("want approved+quorum, got %+v", out)
	}
	st, err := svc.GetPlanStatus(context.Background(), d.PlanID)
	if err != nil || st != PlanStatusApproved {
		t.Errorf("status: want approved, got %s err=%v", st, err)
	}
	// Audit chain: submitted + approved (genesis chain length 2).
	rows := svc.AuditChain(p.ID)
	if len(rows) != 2 {
		t.Fatalf("want 2 audit rows, got %d", len(rows))
	}
	if idx, err := VerifyChain(rows); idx != -1 || err != nil {
		t.Errorf("audit chain: idx=%d err=%v", idx, err)
	}
}

func TestMem_EscalateThenAutoDeny(t *testing.T) {
	svc, p, approvers := makeService(t, nil)
	d, err := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// First rung notify_next → escalate to rung 1
	out, err := svc.Escalate(context.Background(), EscalateInput{PlanID: d.PlanID, FromRung: 0, Reason: "sla_breach"})
	if err != nil {
		t.Fatal(err)
	}
	if out.NewRung != 1 || out.FinalRung {
		t.Errorf("want rung=1 not-final, got %+v", out)
	}
	// Decision at rung 1 needs 2 approvals (k-of-n = 2-of-2). One approval = still pending.
	if _, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: approvers[1], Approve: true, RungIndex: 1,
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := svc.GetPlanStatus(context.Background(), d.PlanID)
	if st != PlanStatusPending {
		t.Errorf("after 1 approval at rung 1: want pending, got %s", st)
	}
	// Now escalate from rung 1 with auto_deny.
	out, err = svc.Escalate(context.Background(), EscalateInput{PlanID: d.PlanID, FromRung: 1, Reason: "sla_breach"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.FinalRung || out.OnTimeout != OnTimeoutAutoDeny {
		t.Errorf("want final auto_deny, got %+v", out)
	}
	st, _ = svc.GetPlanStatus(context.Background(), d.PlanID)
	if st != PlanStatusAutoDenied {
		t.Errorf("status: want auto_denied, got %s", st)
	}
}

func TestMem_RejectionShortCircuits(t *testing.T) {
	svc, p, approvers := makeService(t, nil)
	d, _ := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	out, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: approvers[0], Approve: false, Reason: "looks risky", RungIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != PlanStatusRejected {
		t.Errorf("want rejected, got %s", out.Status)
	}
}

func TestMem_RejectRequiresReason(t *testing.T) {
	svc, p, approvers := makeService(t, nil)
	d, _ := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	_, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: approvers[0], Approve: false, RungIndex: 0,
	})
	if !IsAPICode(err, CodeDecisionUnauthorized) {
		t.Errorf("want CodeDecisionUnauthorized, got %v", err)
	}
}

func TestMem_NonApproverDecisionUnauthorized(t *testing.T) {
	svc, p, _ := makeService(t, nil)
	d, _ := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	stranger := uuid.New()
	_, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: stranger, Approve: true, RungIndex: 0,
	})
	if !IsAPICode(err, CodeDecisionUnauthorized) {
		t.Errorf("want CodeDecisionUnauthorized, got %v", err)
	}
}

func TestMem_PlanAlreadyDecidedAfterApproval(t *testing.T) {
	svc, p, approvers := makeService(t, nil)
	d, _ := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if _, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: approvers[0], Approve: true, RungIndex: 0,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: approvers[0], Approve: true, RungIndex: 0,
	})
	if !IsAPICode(err, CodePlanAlreadyDecided) {
		t.Errorf("want CodePlanAlreadyDecided, got %v", err)
	}
}

func TestMem_PlanExpiry(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cur := now
	svc, p, approvers := makeService(t, func() time.Time { return cur })
	d, _ := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	// Advance the clock past expiry (24h).
	cur = now.Add(25 * time.Hour)
	_, err := svc.RecordDecision(context.Background(), DecisionInput{
		PlanID: d.PlanID, ActorID: approvers[0], Approve: true, RungIndex: 0,
	})
	if !IsAPICode(err, CodePlanExpired) {
		t.Errorf("want CodePlanExpired, got %v", err)
	}
}

func TestMem_AutoApprovedNoRule_AuditEmitted(t *testing.T) {
	svc, p, _ := makeService(t, nil)
	// Service proposer + non-main branch + no agent_default → no rule fires.
	d, err := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindService,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "feature-x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.AutoApproved {
		t.Fatalf("expected auto-approved, got %+v", d)
	}
	rows := svc.AuditChain(p.ID)
	if len(rows) != 1 || rows[0].EventKind != AuditEventAutoApprovedNoRule {
		t.Errorf("expected one auto_approved_no_rule audit row, got %+v", rows)
	}
}

func TestMem_AuditChainGrowsWithDecisions(t *testing.T) {
	svc, p, approvers := makeService(t, nil)
	d, _ := svc.SubmitPlan(context.Background(), &Plan{
		OrgID:        p.OrgID,
		ProposerKind: ProposerKindAgent,
		ProposerID:   uuid.New(),
		Actions:      []Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	_, _ = svc.Escalate(context.Background(), EscalateInput{PlanID: d.PlanID, FromRung: 0, Reason: "sla"})
	_, _ = svc.RecordDecision(context.Background(), DecisionInput{PlanID: d.PlanID, ActorID: approvers[1], Approve: true, RungIndex: 1})
	_, _ = svc.RecordDecision(context.Background(), DecisionInput{PlanID: d.PlanID, ActorID: approvers[2], Approve: true, RungIndex: 1})
	rows := svc.AuditChain(p.ID)
	// submit + escalate + partial-approval(rung1, 1/2) + final approval = 4 rows
	if len(rows) != 4 {
		t.Fatalf("want 4 audit rows, got %d: %+v", len(rows), rows)
	}
	if idx, err := VerifyChain(rows); idx != -1 || err != nil {
		t.Errorf("chain verify: idx=%d err=%v", idx, err)
	}
}
