package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/prnoise"
	"github.com/google/uuid"
)

// recordingClient is a QdrantSearcher fake that records the last call
// and returns a programmable result. Tests assert both the threshold
// gate and the cross-org filter behaviour through it.
type recordingClient struct {
	hits   []QdrantHit
	err    error
	gotK   int
	gotVec []float32
	gotRF  *uuid.UUID
	calls  int
}

func (c *recordingClient) Search(_ context.Context, vec []float32, k int, repoFilter *uuid.UUID) ([]QdrantHit, error) {
	c.calls++
	c.gotK = k
	c.gotVec = vec
	c.gotRF = repoFilter
	return c.hits, c.err
}

func TestNearestDuplicate_ThresholdGate(t *testing.T) {
	t.Parallel()
	hit := uuid.New()
	tests := []struct {
		name      string
		topScore  float64
		expectHit bool
	}{
		{"above_threshold", 0.95, true},
		{"at_threshold", prnoise.DedupCosineThreshold, true},
		{"just_below_threshold", prnoise.DedupCosineThreshold - 0.001, false},
		{"far_below_threshold", 0.10, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &recordingClient{hits: []QdrantHit{{PRID: hit, Score: tc.topScore}}}
			d := NewQdrantDeduper(c, false)
			got, err := d.NearestDuplicate(context.Background(), uuid.New(), []float32{1, 0, 0})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.expectHit {
				if got == nil {
					t.Fatalf("expected hit, got nil")
				}
				if got.PRID != hit {
					t.Errorf("PRID = %v, want %v", got.PRID, hit)
				}
				if got.Similarity != tc.topScore {
					t.Errorf("Similarity = %v, want %v", got.Similarity, tc.topScore)
				}
			} else if got != nil {
				t.Errorf("expected nil, got %+v", got)
			}
		})
	}
}

func TestNearestDuplicate_NoHits(t *testing.T) {
	t.Parallel()
	c := &recordingClient{hits: nil}
	d := NewQdrantDeduper(c, false)
	got, err := d.NearestDuplicate(context.Background(), uuid.New(), []float32{1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestNearestDuplicate_CrossOrgFlagControlsFilter(t *testing.T) {
	t.Parallel()
	repoID := uuid.New()

	// Flag OFF → repoFilter must be set.
	cOff := &recordingClient{hits: []QdrantHit{{PRID: uuid.New(), Score: 0.99}}}
	dOff := NewQdrantDeduper(cOff, false)
	if _, err := dOff.NearestDuplicate(context.Background(), repoID, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cOff.gotRF == nil {
		t.Fatalf("cross-org OFF: expected non-nil repoFilter")
	}
	if *cOff.gotRF != repoID {
		t.Errorf("repoFilter = %v, want %v", *cOff.gotRF, repoID)
	}

	// Flag ON → repoFilter must be nil.
	cOn := &recordingClient{hits: []QdrantHit{{PRID: uuid.New(), Score: 0.99}}}
	dOn := NewQdrantDeduper(cOn, true)
	if _, err := dOn.NearestDuplicate(context.Background(), repoID, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cOn.gotRF != nil {
		t.Errorf("cross-org ON: expected nil repoFilter, got %v", *cOn.gotRF)
	}
}

func TestNearestDuplicate_TopKIsOne(t *testing.T) {
	t.Parallel()
	c := &recordingClient{hits: []QdrantHit{{PRID: uuid.New(), Score: 0.99}}}
	d := NewQdrantDeduper(c, false)
	if _, err := d.NearestDuplicate(context.Background(), uuid.New(), []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.gotK != 1 {
		t.Errorf("top-K = %d, want 1", c.gotK)
	}
}

func TestNearestDuplicate_SearchErrorIsWrapped(t *testing.T) {
	t.Parallel()
	want := errors.New("qdrant exploded")
	c := &recordingClient{err: want}
	d := NewQdrantDeduper(c, false)
	_, err := d.NearestDuplicate(context.Background(), uuid.New(), []float32{1})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped %v, got %v", want, err)
	}
}

func TestDedupCosineThresholdIsADR016Value(t *testing.T) {
	// Anchor: an ADR-016 amendment must update both the constant and
	// this test together. Drift is caught here.
	if prnoise.DedupCosineThreshold != 0.92 {
		t.Errorf("DedupCosineThreshold = %v, want 0.92 (ADR-016)", prnoise.DedupCosineThreshold)
	}
}
