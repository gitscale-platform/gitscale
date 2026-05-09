package prnoise

// QualityWeights are the per-sub-signal weights consumed by the
// rule-based quality scorer. Defaults sum to exactly 1.00 (verified in
// config_test). Order matches the spec table; weights live in [0, 1].
type QualityWeights struct {
	Size         float64
	TestCoverage float64
	CIBuild      float64
	Lint         float64
	AgentsMD     float64
	Diversity    float64
	Churn        float64
}

// Sum returns the total of all sub-signal weights. Used by validation.
func (w QualityWeights) Sum() float64 {
	return w.Size + w.TestCoverage + w.CIBuild + w.Lint + w.AgentsMD + w.Diversity + w.Churn
}

// CompositeWeights govern how the composite router blends quality and
// reputation, and how heavily a duplicate hit zeros the composite.
type CompositeWeights struct {
	Quality      float64
	Reputation   float64
	DedupPenalty float64 // multiplies (1 - DedupScore)
}

// RouterBands are the inclusive thresholds for the composite router.
// AutoMerge must be strictly greater than Reject.
type RouterBands struct {
	AutoMerge float64
	Reject    float64
}

// FeatureFlags gate non-default pipeline behaviours.
type FeatureFlags struct {
	// CrossOrgDedup, when true, removes the per-repo filter from the
	// Qdrant query. Default false (CLAUDE.md open question, August 2026
	// decision). Read once at pipeline construction (per spec) to avoid
	// scoring drift mid-evaluation.
	CrossOrgDedup bool
}

// Config bundles every tuning surface. The pipeline reads it once at
// construction; it never re-reads inside Score.
type Config struct {
	QualityWeights QualityWeights
	Composite      CompositeWeights
	Bands          RouterBands
	FeatureFlags   FeatureFlags
}

// DefaultConfig returns the production defaults documented in the spec
// (issue #116 §Architecture). Weights sum to 1.00, AutoMergeBand=0.85,
// RejectBand=0.30, cross-org dedup OFF.
func DefaultConfig() Config {
	return Config{
		QualityWeights: QualityWeights{
			Size:         0.20,
			TestCoverage: 0.20,
			CIBuild:      0.15,
			Lint:         0.10,
			AgentsMD:     0.20,
			Diversity:    0.10,
			Churn:        0.05,
		},
		Composite: CompositeWeights{
			Quality:      0.5,
			Reputation:   0.4,
			DedupPenalty: 1.0,
		},
		Bands: RouterBands{
			AutoMerge: 0.85,
			Reject:    0.30,
		},
		FeatureFlags: FeatureFlags{CrossOrgDedup: false},
	}
}
