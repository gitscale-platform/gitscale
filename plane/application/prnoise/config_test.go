package prnoise

import (
	"math"
	"testing"
)

func TestDefaultConfig_QualityWeightsSumToOne(t *testing.T) {
	t.Parallel()
	w := DefaultConfig().QualityWeights
	if math.Abs(w.Sum()-1.0) > 1e-9 {
		t.Errorf("QualityWeights sum = %v, want 1.0", w.Sum())
	}
}

func TestDefaultConfig_BandsAreOrdered(t *testing.T) {
	t.Parallel()
	b := DefaultConfig().Bands
	if !(b.AutoMerge > b.Reject) {
		t.Errorf("bands: AutoMerge (%v) must be > Reject (%v)", b.AutoMerge, b.Reject)
	}
}

func TestDefaultConfig_CrossOrgDedupOff(t *testing.T) {
	t.Parallel()
	if DefaultConfig().FeatureFlags.CrossOrgDedup {
		t.Errorf("CrossOrgDedup default = true, want false (CLAUDE.md open question, August 2026)")
	}
}

func TestDefaultConfig_BandValues(t *testing.T) {
	t.Parallel()
	c := DefaultConfig()
	if c.Bands.AutoMerge != 0.85 {
		t.Errorf("AutoMerge = %v, want 0.85", c.Bands.AutoMerge)
	}
	if c.Bands.Reject != 0.30 {
		t.Errorf("Reject = %v, want 0.30", c.Bands.Reject)
	}
}

func TestDefaultConfig_CompositeWeights(t *testing.T) {
	t.Parallel()
	w := DefaultConfig().Composite
	if w.Quality != 0.5 {
		t.Errorf("Quality = %v, want 0.5", w.Quality)
	}
	if w.Reputation != 0.4 {
		t.Errorf("Reputation = %v, want 0.4", w.Reputation)
	}
	if w.DedupPenalty != 1.0 {
		t.Errorf("DedupPenalty = %v, want 1.0", w.DedupPenalty)
	}
}

func TestDefaultConfig_QualityWeightTable(t *testing.T) {
	t.Parallel()
	w := DefaultConfig().QualityWeights
	tests := []struct {
		name string
		got  float64
		want float64
	}{
		{"size", w.Size, 0.20},
		{"test_coverage", w.TestCoverage, 0.20},
		{"ci_build", w.CIBuild, 0.15},
		{"lint", w.Lint, 0.10},
		{"agents_md", w.AgentsMD, 0.20},
		{"diversity", w.Diversity, 0.10},
		{"churn", w.Churn, 0.05},
	}
	for _, tc := range tests {
		if math.Abs(tc.got-tc.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestDecisionCode_Valid(t *testing.T) {
	t.Parallel()
	for _, c := range []DecisionCode{
		DecisionAutoMergeEligible,
		DecisionMaintainerReview,
		DecisionReject,
	} {
		if !c.Valid() {
			t.Errorf("DecisionCode %q: Valid() = false", c)
		}
	}
	if DecisionCode("garbage").Valid() {
		t.Errorf("invalid code passed Valid()")
	}
}
