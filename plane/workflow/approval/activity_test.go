package approval

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gitscale-platform/gitscale/plane/application/policy"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

// makeServiceWithPolicy seeds a 2-rung policy on a fresh org and returns
// the service, the org id, and the approver IDs.
func makeServiceWithPolicy(t *testing.T) (*policy.MemService, uuid.UUID, []uuid.UUID) {
	t.Helper()
	a1, a2, a3 := uuid.New(), uuid.New(), uuid.New()
	p := &policy.Policy{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Name:  "test",
		ApproverGroups: map[string]policy.ApproverGroup{
			"oncall":   {Name: "oncall", HumanUserIDs: []uuid.UUID{a1}, RequiredCount: 1},
			"security": {Name: "security", HumanUserIDs: []uuid.UUID{a2, a3}, RequiredCount: 2},
		},
		Rules: []policy.Rule{
			{
				Kind:          policy.PredicatePRMerge,
				Match:         map[string]string{"branch": "main"},
				ExpirySeconds: 86400,
				Ladder: []policy.EscalationRung{
					{GroupName: "oncall", SLASeconds: 1, OnTimeout: policy.OnTimeoutNotifyNext},
					{GroupName: "security", SLASeconds: 1, OnTimeout: policy.OnTimeoutAutoDeny},
				},
			},
		},
	}
	svc := policy.NewMemService(nil)
	if err := svc.RegisterPolicy(p); err != nil {
		t.Fatal(err)
	}
	return svc, p.OrgID, []uuid.UUID{a1, a2, a3}
}

// probeClient wraps a real PolicyClient and signals on every poll so the
// test can react when the activity has submitted the plan.
type probeClient struct {
	inner    appclient.PolicyClient
	planIDCh chan uuid.UUID
	once     bool
}

func (p *probeClient) SubmitPlan(ctx context.Context, plan *policy.Plan) (policy.Decision, error) {
	d, err := p.inner.SubmitPlan(ctx, plan)
	if err == nil && !p.once {
		p.once = true
		// non-blocking send so the test can drop the channel.
		select {
		case p.planIDCh <- d.PlanID:
		default:
		}
	}
	return d, err
}

func (p *probeClient) RecordDecision(ctx context.Context, in policy.DecisionInput) (policy.DecisionOutcome, error) {
	return p.inner.RecordDecision(ctx, in)
}

func (p *probeClient) Escalate(ctx context.Context, in policy.EscalateInput) (policy.EscalateOutcome, error) {
	return p.inner.Escalate(ctx, in)
}

func (p *probeClient) GetPlanStatus(ctx context.Context, planID uuid.UUID) (policy.PlanStatus, error) {
	return p.inner.GetPlanStatus(ctx, planID)
}

func TestActivity_ApprovesAtRung0_WithProbe(t *testing.T) {
	svc, orgID, approvers := makeServiceWithPolicy(t)
	probe := &probeClient{
		inner:    appclient.NewStubPolicyClient(svc),
		planIDCh: make(chan uuid.UUID, 1),
	}
	a := NewActivity(probe)
	a.SetTimingForTest(20*time.Millisecond, 200*time.Millisecond, nil)

	// Approver goroutine: wait for plan ID, then approve at rung 0.
	go func() {
		select {
		case planID := <-probe.planIDCh:
			time.Sleep(30 * time.Millisecond)
			if _, err := svc.RecordDecision(context.Background(), policy.DecisionInput{
				PlanID: planID, ActorID: approvers[0], Approve: true, RungIndex: 0,
			}); err != nil {
				t.Errorf("approver record: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("never received plan ID")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := a.Execute(ctx, Input{
		OrgID:        orgID,
		ProposerID:   uuid.New(),
		ProposerKind: policy.ProposerKindAgent,
		Actions:      []policy.Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != policy.PlanStatusApproved {
		t.Errorf("want approved, got %s", res.Status)
	}
}

func TestActivity_AutoApprovedNoRule_TerminatesImmediately(t *testing.T) {
	svc, orgID, _ := makeServiceWithPolicy(t)
	client := appclient.NewStubPolicyClient(svc)
	a := NewActivity(client)
	a.SetTimingForTest(20*time.Millisecond, 50*time.Millisecond, nil)

	// Service proposer + non-main branch + no agent_default → auto-approved
	// at submission, no polling needed.
	res, err := a.Execute(context.Background(), Input{
		OrgID:        orgID,
		ProposerID:   uuid.New(),
		ProposerKind: policy.ProposerKindService,
		Actions:      []policy.Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "feature-x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != policy.PlanStatusAutoApprovedNoRule {
		t.Errorf("want auto_approved_no_rule, got %s", res.Status)
	}
	if client.PollCount() != 0 {
		t.Errorf("auto-approved should not poll, got %d polls", client.PollCount())
	}
}

func TestActivity_AllRungsExhausted_AutoDenied(t *testing.T) {
	svc, orgID, _ := makeServiceWithPolicy(t)
	client := appclient.NewStubPolicyClient(svc)
	a := NewActivity(client)
	// Tight timing: SLA of 1 second + jitter 0 → escalate as soon as the
	// fake clock advances past the rung deadline. The clock is read via
	// an atomic counter so there is no data race between the test
	// goroutine advancing it and the activity reading it.
	startNanos := time.Now().UTC().UnixNano()
	var advance int64
	clock := func() time.Time {
		extra := atomic.LoadInt64(&advance)
		return time.Unix(0, startNanos+extra).UTC()
	}
	a.SetTimingForTest(5*time.Millisecond, 0, clock)

	// Goroutine: advance the fake clock past each SLA so the activity
	// observes deadline elapsed and calls Escalate. After two escalations
	// the second rung's auto_deny finalises the plan.
	done := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		atomic.StoreInt64(&advance, int64(2*time.Second))
		time.Sleep(40 * time.Millisecond)
		atomic.StoreInt64(&advance, int64(4*time.Second))
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := a.Execute(ctx, Input{
		OrgID:        orgID,
		ProposerID:   uuid.New(),
		ProposerKind: policy.ProposerKindAgent,
		Actions:      []policy.Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if res.Status != policy.PlanStatusAutoDenied {
		t.Errorf("want auto_denied, got %s", res.Status)
	}
}

func TestActivity_NilClient(t *testing.T) {
	a := &Activity{client: nil, pollInterval: PollInterval, slaJitter: SLAJitter, now: func() time.Time { return time.Now().UTC() }}
	_, err := a.Execute(context.Background(), Input{
		OrgID:        uuid.New(),
		ProposerKind: policy.ProposerKindAgent,
	})
	if err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestActivity_InvalidProposerKind(t *testing.T) {
	svc, orgID, _ := makeServiceWithPolicy(t)
	client := appclient.NewStubPolicyClient(svc)
	a := NewActivity(client)
	_, err := a.Execute(context.Background(), Input{
		OrgID:        orgID,
		ProposerKind: policy.ProposerKind("alien"),
	})
	if err == nil {
		t.Fatal("expected error for invalid proposer_kind")
	}
}

func TestActivity_SubmitTransportError(t *testing.T) {
	failing := &failingClient{err: errors.New("grpc unavailable")}
	a := NewActivity(failing)
	_, err := a.Execute(context.Background(), Input{
		OrgID:        uuid.New(),
		ProposerKind: policy.ProposerKindAgent,
		Actions:      []policy.Action{{Kind: "pr_merge"}},
	})
	if err == nil {
		t.Fatal("expected error on submit failure")
	}
}

func TestActivity_ContextCancelDuringPoll(t *testing.T) {
	svc, orgID, _ := makeServiceWithPolicy(t)
	client := appclient.NewStubPolicyClient(svc)
	a := NewActivity(client)
	a.SetTimingForTest(50*time.Millisecond, 1*time.Hour, nil) // never escalate

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := a.Execute(ctx, Input{
		OrgID:        orgID,
		ProposerID:   uuid.New(),
		ProposerKind: policy.ProposerKindAgent,
		Actions:      []policy.Action{{Kind: "pr_merge", Fields: map[string]string{"branch": "main"}}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want DeadlineExceeded, got %v", err)
	}
}

// failingClient returns err on every call.
type failingClient struct{ err error }

func (f *failingClient) SubmitPlan(_ context.Context, _ *policy.Plan) (policy.Decision, error) {
	return policy.Decision{}, f.err
}
func (f *failingClient) RecordDecision(_ context.Context, _ policy.DecisionInput) (policy.DecisionOutcome, error) {
	return policy.DecisionOutcome{}, f.err
}
func (f *failingClient) Escalate(_ context.Context, _ policy.EscalateInput) (policy.EscalateOutcome, error) {
	return policy.EscalateOutcome{}, f.err
}
func (f *failingClient) GetPlanStatus(_ context.Context, _ uuid.UUID) (policy.PlanStatus, error) {
	return "", f.err
}
