package prnoise

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func stubInput() PRInput {
	return PRInput{
		PRID:        uuid.New(),
		RepoID:      uuid.New(),
		OrgID:       uuid.New(),
		AgentID:     uuid.New(),
		Title:       "stub",
		Description: "stub body",
		DiffStats: DiffStats{
			Additions: 0, Deletions: 0,
			FilePaths: []string{"a/x", "b/x", "c/x", "d/x", "e/x"},
		},
		CIResult:   CIResult{Build: true, Test: true, CoverageDelta: 0.1},
		AgentsMDOK: true,
		OpenedAt:   time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
	}
}

type embedderStub struct{}

func (embedderStub) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0}, nil
}

type deduperStub struct{ hit *DuplicateHit }

func (d deduperStub) NearestDuplicate(_ context.Context, _ uuid.UUID, _ []float32) (*DuplicateHit, error) {
	return d.hit, nil
}

type repStub struct{ score float64 }

func (r repStub) Score(_ context.Context, _ uuid.UUID) (float64, error) { return r.score, nil }

// stubQuality is a fixed-score QualitySignalScorer used by the stub
// tests in this package; using the rule-based scorer would create a
// quality → prnoise → quality test-time import cycle.
type stubQuality struct{ score float64 }

func (s stubQuality) Score(_ context.Context, _ PRInput) float64 { return s.score }

func newStubServiceForTest(t *testing.T, dup *DuplicateHit, rep float64) *StubService {
	t.Helper()
	cfg := DefaultConfig()
	p := NewPipeline(embedderStub{}, deduperStub{hit: dup}, stubQuality{score: 1.0}, repStub{score: rep}, cfg)
	s, err := NewStubService(p)
	if err != nil {
		t.Fatalf("NewStubService: %v", err)
	}
	return s
}

func TestStubService_NilPipelineRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewStubService(nil); err != ErrPipelineUnconfigured {
		t.Errorf("err = %v, want ErrPipelineUnconfigured", err)
	}
}

func TestStubService_RecordsDecisionAndOutbox(t *testing.T) {
	t.Parallel()
	s := newStubServiceForTest(t, nil, 1.0)
	in := stubInput()
	d, err := s.RecordDecision(context.Background(), in)
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if d.Code != DecisionAutoMergeEligible {
		t.Errorf("code = %v, want auto_merge_eligible", d.Code)
	}
	if got := s.Decisions(); len(got) != 1 {
		t.Errorf("decisions count = %d, want 1", len(got))
	}
	out := s.Outbox()
	if len(out) != 1 {
		t.Fatalf("outbox count = %d, want 1", len(out))
	}
	if out[0].EventType != EventTypeNoiseDecisionRecorded {
		t.Errorf("event type = %q, want %q", out[0].EventType, EventTypeNoiseDecisionRecorded)
	}
	if out[0].PRID != in.PRID {
		t.Errorf("event pr_id = %v, want %v", out[0].PRID, in.PRID)
	}
}

func TestStubService_RescoreUpsertsAndAppendsOutbox(t *testing.T) {
	t.Parallel()
	s := newStubServiceForTest(t, nil, 1.0)
	in := stubInput()
	if _, err := s.RecordDecision(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDecision(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := s.Decisions(); len(got) != 1 {
		t.Errorf("decisions count after rescore = %d, want 1 (upsert)", len(got))
	}
	if got := s.Outbox(); len(got) != 2 {
		t.Errorf("outbox count after rescore = %d, want 2 (one row per call)", len(got))
	}
}

func TestStubService_DuplicatePathRejects(t *testing.T) {
	t.Parallel()
	dupID := uuid.New()
	s := newStubServiceForTest(t, &DuplicateHit{PRID: dupID, Similarity: 0.99}, 1.0)
	in := stubInput()
	d, err := s.RecordDecision(context.Background(), in)
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if d.Code != DecisionReject {
		t.Errorf("code = %v, want reject", d.Code)
	}
	if d.DuplicateOf == nil || *d.DuplicateOf != dupID {
		t.Errorf("DuplicateOf = %v, want %v", d.DuplicateOf, dupID)
	}
	out := s.Outbox()[0]
	if out.Payload.DuplicateOf == nil || *out.Payload.DuplicateOf != dupID {
		t.Errorf("payload duplicate_of = %v, want %v", out.Payload.DuplicateOf, dupID)
	}
}

func TestStubService_HumanAuthorOmitsAgentIDInPayload(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	p := NewPipeline(embedderStub{}, deduperStub{}, stubQuality{score: 1.0}, repStub{score: 1.0}, cfg)
	s, err := NewStubService(p)
	if err != nil {
		t.Fatal(err)
	}
	in := stubInput()
	in.AgentID = uuid.Nil
	if _, err := s.RecordDecision(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	out := s.Outbox()[0]
	if out.Payload.AgentID != nil {
		t.Errorf("payload agent_id = %v, want nil for human author", out.Payload.AgentID)
	}
}
