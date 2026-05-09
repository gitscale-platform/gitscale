package issuenoise

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/issuenoise/rules"
	"github.com/google/uuid"
)

// fixedScorer returns a constant Score.
type fixedScorer struct {
	s   Score
	err error
}

func (f fixedScorer) Score(_ context.Context, _ IssueDraft) (Score, error) {
	return f.s, f.err
}

func newRouterT(t *testing.T, scorer IssueScorer, enforce bool) (*Router, *StubStore, *RecordingWorkflowStarter) {
	t.Helper()
	store := NewStubStore()
	wf := &RecordingWorkflowStarter{}
	r, err := NewRouter(RouterConfig{
		Store:     store,
		Scorer:    scorer,
		Workflows: wf,
		Enforce:   enforce,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r, store, wf
}

func draft() IssueDraft {
	return IssueDraft{
		ID:         uuid.New(),
		RepoID:     uuid.New(),
		ReporterID: uuid.New(),
		Title:      "title",
		Body:       "body",
		CreatedAt:  time.Now(),
	}
}

func TestNewRouter_RequiresStoreAndScorer(t *testing.T) {
	if _, err := NewRouter(RouterConfig{Scorer: fixedScorer{}}); !errors.Is(err, ErrNoStore) {
		t.Errorf("want ErrNoStore, got %v", err)
	}
	if _, err := NewRouter(RouterConfig{Store: NewStubStore()}); !errors.Is(err, ErrNoScorer) {
		t.Errorf("want ErrNoScorer, got %v", err)
	}
}

func TestRoute_NormalVerdict_AnchorsOpen_NoWorkflow(t *testing.T) {
	r, store, wf := newRouterT(t, fixedScorer{s: Score{ScorerVersion: "rule-v1"}}, true)
	d := draft()
	res, err := r.Route(context.Background(), d)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Verdict != VerdictNormal {
		t.Errorf("verdict=%v want normal", res.Verdict)
	}
	if got := store.IssueState(d.ID); got != IssueStateOpen {
		t.Errorf("state=%q want open", got)
	}
	if len(store.Decisions()) != 1 {
		t.Errorf("decisions=%d want 1", len(store.Decisions()))
	}
	if len(store.Outbox()) != 1 || store.Outbox()[0].EventType != EventTypeRoutingDecided {
		t.Errorf("outbox=%+v", store.Outbox())
	}
	if len(wf.Started) != 0 {
		t.Errorf("workflow must not start for normal verdict")
	}
}

func TestRoute_LowQuality_HoldsAndStartsWorkflow(t *testing.T) {
	r, store, wf := newRouterT(t, fixedScorer{s: Score{LowQuality: 0.6, ScorerVersion: "rule-v1"}}, true)
	d := draft()
	res, err := r.Route(context.Background(), d)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Verdict != VerdictLowQuality {
		t.Fatalf("verdict=%v", res.Verdict)
	}
	if store.IssueState(d.ID) != IssueStateHeld {
		t.Fatalf("state=%v want held", store.IssueState(d.ID))
	}
	if len(wf.Started) != 1 {
		t.Fatalf("workflow=%d want 1", len(wf.Started))
	}
	if wf.Started[0].IssueID != d.ID {
		t.Errorf("workflow started for wrong issue")
	}
}

func TestRoute_Spam_AutoCloses(t *testing.T) {
	r, store, wf := newRouterT(t, fixedScorer{s: Score{Spam: 0.9, ScorerVersion: "rule-v1"}}, true)
	d := draft()
	res, err := r.Route(context.Background(), d)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Verdict != VerdictSpam {
		t.Fatalf("verdict=%v", res.Verdict)
	}
	if store.IssueState(d.ID) != IssueStateAutoClosedSpam {
		t.Fatalf("state=%v want auto_closed_spam", store.IssueState(d.ID))
	}
	if len(wf.Started) != 0 {
		t.Errorf("spam must not start hold workflow")
	}
}

func TestRoute_Duplicate_HoldsWithDuplicateOf(t *testing.T) {
	parent := uuid.New()
	r, store, _ := newRouterT(t, fixedScorer{s: Score{Duplicate: 0.95, DuplicateOf: &parent, ScorerVersion: "rule-v1"}}, true)
	d := draft()
	res, err := r.Route(context.Background(), d)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Verdict != VerdictDuplicate {
		t.Fatalf("verdict=%v", res.Verdict)
	}
	if res.DuplicateOf == nil || *res.DuplicateOf != parent {
		t.Fatalf("DuplicateOf=%v want %v", res.DuplicateOf, parent)
	}
	if store.IssueState(d.ID) != IssueStateHeld {
		t.Fatalf("state=%v want held", store.IssueState(d.ID))
	}
}

func TestRoute_DarkLaunch_AlwaysOpen(t *testing.T) {
	// Enforce=false must keep the issue open even on a spam verdict,
	// while still recording the verdict in the outbox payload.
	r, store, wf := newRouterT(t, fixedScorer{s: Score{Spam: 0.99, ScorerVersion: "rule-v1"}}, false)
	d := draft()
	res, err := r.Route(context.Background(), d)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Verdict != VerdictSpam {
		t.Errorf("verdict still computed: got %v", res.Verdict)
	}
	if res.State != IssueStateOpen {
		t.Errorf("dark-launch must anchor open, got %v", res.State)
	}
	if res.Enforced {
		t.Errorf("Enforced flag must be false")
	}
	if store.IssueState(d.ID) != IssueStateOpen {
		t.Fatalf("issue must be open during dark-launch")
	}
	if len(wf.Started) != 0 {
		t.Errorf("dark-launch must not start workflows")
	}
	// Decision + outbox row still written.
	if len(store.Decisions()) != 1 || len(store.Outbox()) != 1 {
		t.Errorf("decision/outbox missing")
	}
	// Outbox payload records enforced=false.
	p, ok := store.Outbox()[0].Payload.(RoutingDecidedPayload)
	if !ok {
		t.Fatalf("payload type=%T", store.Outbox()[0].Payload)
	}
	if p.Enforced {
		t.Errorf("payload.Enforced must be false during dark-launch")
	}
	if p.Verdict != "spam" {
		t.Errorf("payload.Verdict=%q want spam", p.Verdict)
	}
}

func TestRoute_TxFailure_AllOrNothing(t *testing.T) {
	// Force the stub store to error after fn returns; the router must
	// surface the error and no state must be visible.
	store := NewStubStore()
	store.FailOnTx = errors.New("forced rollback")
	wf := &RecordingWorkflowStarter{}
	r, err := NewRouter(RouterConfig{
		Store:     store,
		Scorer:    fixedScorer{s: Score{Spam: 0.9, ScorerVersion: "rule-v1"}},
		Workflows: wf,
		Enforce:   true,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	d := draft()
	if _, err := r.Route(context.Background(), d); err == nil {
		t.Fatalf("expected error from forced rollback")
	}
	if store.IssueState(d.ID) != "" {
		t.Errorf("issue must not be committed on rollback")
	}
	if len(store.Decisions()) != 0 || len(store.Outbox()) != 0 {
		t.Errorf("decisions/outbox must be empty on rollback")
	}
	if len(wf.Started) != 0 {
		t.Errorf("workflow must not start on rollback")
	}
}

func TestRoute_FailOpenOnScorerError(t *testing.T) {
	// Scorer error must NOT block the route. The router falls back to
	// a neutral score and admits the issue.
	before := ScorerErrorCount()
	r, store, _ := newRouterT(t, fixedScorer{err: errors.New("scorer down")}, true)
	d := draft()
	res, err := r.Route(context.Background(), d)
	if err != nil {
		t.Fatalf("Route must not error on scorer failure: %v", err)
	}
	if res.Verdict != VerdictNormal {
		t.Errorf("expected fail-open to VerdictNormal, got %v", res.Verdict)
	}
	if store.IssueState(d.ID) != IssueStateOpen {
		t.Errorf("issue must be admitted on scorer fail-open")
	}
	if ScorerErrorCount() != before+1 {
		t.Errorf("scorer error counter not incremented")
	}
}

func TestRoute_PostCommitWorkflowErrorDoesNotBlockAck(t *testing.T) {
	// Even if the workflow start fails, the route succeeds and
	// state is committed (the reconciler is the safety net).
	store := NewStubStore()
	wf := &RecordingWorkflowStarter{StartErr: errors.New("temporal down")}
	r, err := NewRouter(RouterConfig{
		Store:     store,
		Scorer:    fixedScorer{s: Score{LowQuality: 0.5, ScorerVersion: "rule-v1"}},
		Workflows: wf,
		Enforce:   true,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	d := draft()
	if _, err := r.Route(context.Background(), d); err != nil {
		t.Fatalf("Route must not fail on post-commit workflow error: %v", err)
	}
	if store.IssueState(d.ID) != IssueStateHeld {
		t.Errorf("state not committed")
	}
}

func TestRelease_FlipsHeldToOpenAndSignals(t *testing.T) {
	r, store, wf := newRouterT(t, fixedScorer{s: Score{LowQuality: 0.6, ScorerVersion: "rule-v1"}}, true)
	d := draft()
	if _, err := r.Route(context.Background(), d); err != nil {
		t.Fatalf("Route: %v", err)
	}
	maintainer := uuid.New()
	if err := r.Release(context.Background(), d.ID, d.RepoID, maintainer); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if store.IssueState(d.ID) != IssueStateOpen {
		t.Fatalf("state=%v want open after release", store.IssueState(d.ID))
	}
	// Two decision rows: routing + release.
	if len(store.Decisions()) != 2 {
		t.Fatalf("decisions=%d want 2", len(store.Decisions()))
	}
	if store.Decisions()[1].DecidedBy != "maintainer:"+maintainer.String() {
		t.Errorf("DecidedBy=%q", store.Decisions()[1].DecidedBy)
	}
	// Outbox: routing_decided + released.
	if len(store.Outbox()) != 2 {
		t.Fatalf("outbox=%d want 2", len(store.Outbox()))
	}
	if store.Outbox()[1].EventType != EventTypeReleased {
		t.Errorf("second outbox event=%q want %q", store.Outbox()[1].EventType, EventTypeReleased)
	}
	if len(wf.Released) != 1 || wf.Released[0] != d.ID {
		t.Errorf("expected workflow signal for issue, got %+v", wf.Released)
	}
}

func TestRoute_Idempotency_TwoIdenticalRoutesProduceTwoDecisions(t *testing.T) {
	// Idempotency at the issue-id level is enforced by the database
	// PRIMARY KEY in production; the router itself is non-idempotent
	// on duplicate Route calls — callers (REST/MCP/GraphQL handlers)
	// gate on issue_id uniqueness. This test documents that contract:
	// the router does write twice if called twice.
	r, store, _ := newRouterT(t, fixedScorer{s: Score{ScorerVersion: "rule-v1"}}, true)
	d := draft()
	for i := 0; i < 2; i++ {
		if _, err := r.Route(context.Background(), d); err != nil {
			t.Fatalf("Route %d: %v", i, err)
		}
	}
	if len(store.Decisions()) != 2 {
		t.Errorf("decisions=%d want 2 (caller is responsible for issue_id uniqueness)", len(store.Decisions()))
	}
}

// ensure Router exists in same package as rules
var _ rules.Rule = func(_ context.Context, _ rules.Input) (rules.Result, error) {
	return rules.Result{}, nil
}
