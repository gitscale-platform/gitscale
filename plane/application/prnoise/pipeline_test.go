package prnoise

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeQuality returns a fixed score regardless of PRInput. Pipeline
// tests in this package use it instead of the rule-based scorer to
// keep the test in the prnoise package (avoiding a quality → prnoise
// → quality test-time cycle).
type fakeQuality struct{ score float64 }

func (f fakeQuality) Score(_ context.Context, _ PRInput) float64 { return f.score }

// frozenEmbedder returns a fixed vector regardless of input. Used for
// determinism tests where the embedder must not introduce randomness.
type frozenEmbedder struct{ vec []float32 }

func (e *frozenEmbedder) Embed(_ context.Context, _ string) ([]float32, error) { return e.vec, nil }

type errEmbedder struct{ err error }

func (e *errEmbedder) Embed(_ context.Context, _ string) ([]float32, error) { return nil, e.err }

type fakeDeduper struct {
	hit *DuplicateHit
	err error
}

func (d *fakeDeduper) NearestDuplicate(_ context.Context, _ uuid.UUID, _ []float32) (*DuplicateHit, error) {
	return d.hit, d.err
}

type fakeReputation struct {
	score float64
	err   error
}

func (f *fakeReputation) Score(_ context.Context, _ uuid.UUID) (float64, error) {
	return f.score, f.err
}

func cleanInput() PRInput {
	return PRInput{
		PRID:        uuid.New(),
		RepoID:      uuid.New(),
		OrgID:       uuid.New(),
		AgentID:     uuid.New(),
		Title:       "Add foo",
		Description: "Adds the foo widget",
		DiffStats: DiffStats{
			Additions: 0, Deletions: 0,
			FilePaths: []string{"a/x", "b/x", "c/x", "d/x", "e/x"},
		},
		CIResult:   CIResult{Build: true, Test: true, LintViolations: 0, CoverageDelta: 0.10},
		AgentsMDOK: true,
		OpenedAt:   time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
}

// newPipelineWithFakes wires the pipeline with a fake embedder,
// deduper, and reputation scorer; quality defaults to a fakeQuality of
// 1.0 so high-reputation cases hit the auto-merge band.
func newPipelineWithFakes(emb Embedder, dd Deduper, rep float64) *Pipeline {
	return newPipelineWithQuality(emb, dd, fakeQuality{score: 1.0}, rep)
}

func newPipelineWithQuality(emb Embedder, dd Deduper, q QualitySignalScorer, rep float64) *Pipeline {
	cfg := DefaultConfig()
	return NewPipeline(emb, dd, q, &fakeReputation{score: rep}, cfg)
}

func TestPipeline_Score_AutoMergeOnCleanInput(t *testing.T) {
	t.Parallel()
	p := newPipelineWithFakes(&frozenEmbedder{vec: []float32{1, 0}}, &fakeDeduper{}, 1.0)
	d, err := p.Score(context.Background(), cleanInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Code != DecisionAutoMergeEligible {
		t.Errorf("code = %v, want auto_merge_eligible", d.Code)
	}
	if d.DuplicateOf != nil {
		t.Errorf("DuplicateOf = %v, want nil", *d.DuplicateOf)
	}
	if d.DedupScore != 0 {
		t.Errorf("DedupScore = %v, want 0", d.DedupScore)
	}
}

func TestPipeline_Score_DuplicateAlwaysRejects(t *testing.T) {
	t.Parallel()
	dupID := uuid.New()
	p := newPipelineWithFakes(
		&frozenEmbedder{vec: []float32{1}},
		&fakeDeduper{hit: &DuplicateHit{PRID: dupID, Similarity: 0.99}},
		1.0,
	)
	d, err := p.Score(context.Background(), cleanInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Code != DecisionReject {
		t.Errorf("code = %v, want reject", d.Code)
	}
	if d.DedupScore != 1.0 {
		t.Errorf("DedupScore = %v, want 1.0", d.DedupScore)
	}
	if d.DuplicateOf == nil || *d.DuplicateOf != dupID {
		t.Errorf("DuplicateOf = %v, want %v", d.DuplicateOf, dupID)
	}
	if d.CompositeScore != 0 {
		t.Errorf("CompositeScore = %v, want 0 (duplicate zeros it)", d.CompositeScore)
	}
}

func TestPipeline_Score_BorderlineRoutesToReview(t *testing.T) {
	t.Parallel()
	// quality 0.5, rep 0.4 → composite 0.41, in [0.30, 0.85) review band.
	p := newPipelineWithQuality(&frozenEmbedder{vec: []float32{1}}, &fakeDeduper{}, fakeQuality{score: 0.5}, 0.4)
	d, err := p.Score(context.Background(), cleanInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Code != DecisionMaintainerReview {
		t.Errorf("code = %v (composite=%v), want maintainer_review", d.Code, d.CompositeScore)
	}
}

func TestPipeline_Score_DeterministicForSameInput(t *testing.T) {
	t.Parallel()
	in := cleanInput()
	p1 := newPipelineWithFakes(&frozenEmbedder{vec: []float32{1, 1}}, &fakeDeduper{}, 0.6)
	p2 := newPipelineWithFakes(&frozenEmbedder{vec: []float32{1, 1}}, &fakeDeduper{}, 0.6)
	a, err := p1.Score(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p2.Score(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("non-deterministic: %+v vs %+v", a, b)
	}
}

func TestPipeline_Score_EmbedderErrorWrapped(t *testing.T) {
	t.Parallel()
	want := errors.New("embedder kaboom")
	p := newPipelineWithFakes(&errEmbedder{err: want}, &fakeDeduper{}, 1.0)
	_, err := p.Score(context.Background(), cleanInput())
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrapped %v", err, want)
	}
}

func TestPipeline_Score_DedupErrorWrapped(t *testing.T) {
	t.Parallel()
	want := errors.New("qdrant gone")
	p := newPipelineWithFakes(&frozenEmbedder{vec: []float32{1}}, &fakeDeduper{err: want}, 1.0)
	_, err := p.Score(context.Background(), cleanInput())
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrapped %v", err, want)
	}
}

func TestPipeline_Score_ReputationErrorWrapped(t *testing.T) {
	t.Parallel()
	want := errors.New("identity offline")
	cfg := DefaultConfig()
	p := NewPipeline(&frozenEmbedder{vec: []float32{1}}, &fakeDeduper{}, fakeQuality{score: 1.0}, &fakeReputation{err: want}, cfg)
	_, err := p.Score(context.Background(), cleanInput())
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrapped %v", err, want)
	}
}

func TestPipeline_Score_DecidedAtMirrorsOpenedAt(t *testing.T) {
	t.Parallel()
	p := newPipelineWithFakes(&frozenEmbedder{vec: []float32{1}}, &fakeDeduper{}, 1.0)
	in := cleanInput()
	d, err := p.Score(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !d.DecidedAt.Equal(in.OpenedAt) {
		t.Errorf("DecidedAt = %v, want %v", d.DecidedAt, in.OpenedAt)
	}
}
