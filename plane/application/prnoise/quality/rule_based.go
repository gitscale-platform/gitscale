package quality

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/application/prnoise"
)

// RuleBasedQualityScorer is the rule-based implementation of
// QualitySignalScorer. It computes the seven sub-signals (size,
// test_coverage, ci_build, lint, agents_md, diversity, churn) and
// returns their weighted sum, clamped to [0, 1] defensively.
//
// The struct is intentionally a value, not a pointer, so the pipeline
// can construct it cheaply per request — there is no hidden state.
type RuleBasedQualityScorer struct {
	Weights prnoise.QualityWeights
}

// NewRuleBasedQualityScorer returns a scorer with the supplied weights.
// Use prnoise.DefaultConfig().QualityWeights for the production tuning.
func NewRuleBasedQualityScorer(w prnoise.QualityWeights) RuleBasedQualityScorer {
	return RuleBasedQualityScorer{Weights: w}
}

// Score computes the weighted sum of all seven sub-signals.
func (s RuleBasedQualityScorer) Score(_ context.Context, in prnoise.PRInput) float64 {
	w := s.Weights
	sum := w.Size*SizeScore(in.DiffStats) +
		w.TestCoverage*TestCoverageScore(in.CIResult) +
		w.CIBuild*CIBuildScore(in.CIResult) +
		w.Lint*LintScore(in.CIResult) +
		w.AgentsMD*AgentsMDScore(in.AgentsMDOK) +
		w.Diversity*DiversityScore(in.DiffStats) +
		w.Churn*ChurnScore(in.DiffStats)
	return clamp01(sum)
}
