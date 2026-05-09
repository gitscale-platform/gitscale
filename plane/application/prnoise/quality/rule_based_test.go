package quality

import (
	"context"
	"math"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/prnoise"
)

const epsilon = 1e-9

func near(a, b float64) bool { return math.Abs(a-b) < epsilon }

func TestSizeScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   prnoise.DiffStats
		want float64
	}{
		{"zero_lines", prnoise.DiffStats{}, 1.0},
		{"500_lines_mid", prnoise.DiffStats{Additions: 250, Deletions: 250}, 0.5},
		{"1000_lines_floor", prnoise.DiffStats{Additions: 700, Deletions: 300}, 0.0},
		{"over_1000_clamps_to_zero", prnoise.DiffStats{Additions: 5000}, 0.0},
		{"negative_inputs_treated_as_zero", prnoise.DiffStats{Additions: -100, Deletions: -50}, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SizeScore(tc.in); !near(got, tc.want) {
				t.Errorf("SizeScore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTestCoverageScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   prnoise.CIResult
		want float64
	}{
		{"flat_coverage", prnoise.CIResult{CoverageDelta: 0}, 0.5},
		{"plus_10pp", prnoise.CIResult{CoverageDelta: 0.10}, 1.0},
		{"plus_5pp", prnoise.CIResult{CoverageDelta: 0.05}, 0.75},
		{"minus_10pp", prnoise.CIResult{CoverageDelta: -0.10}, 0.0},
		{"minus_50pp_clamps", prnoise.CIResult{CoverageDelta: -0.50}, 0.0},
		{"plus_50pp_clamps", prnoise.CIResult{CoverageDelta: 0.50}, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TestCoverageScore(tc.in); !near(got, tc.want) {
				t.Errorf("TestCoverageScore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCIBuildScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   prnoise.CIResult
		want float64
	}{
		{"both_pass", prnoise.CIResult{Build: true, Test: true}, 1.0},
		{"build_fail", prnoise.CIResult{Build: false, Test: true}, 0.0},
		{"test_fail", prnoise.CIResult{Build: true, Test: false}, 0.0},
		{"both_fail", prnoise.CIResult{}, 0.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CIBuildScore(tc.in); !near(got, tc.want) {
				t.Errorf("CIBuildScore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLintScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   prnoise.CIResult
		want float64
	}{
		{"clean", prnoise.CIResult{LintViolations: 0}, 1.0},
		{"25_violations", prnoise.CIResult{LintViolations: 25}, 0.5},
		{"50_violations", prnoise.CIResult{LintViolations: 50}, 0.0},
		{"100_violations_clamps", prnoise.CIResult{LintViolations: 100}, 0.0},
		{"negative_clamps_to_clean", prnoise.CIResult{LintViolations: -5}, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LintScore(tc.in); !near(got, tc.want) {
				t.Errorf("LintScore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentsMDScore(t *testing.T) {
	t.Parallel()
	if got := AgentsMDScore(true); got != 1.0 {
		t.Errorf("AgentsMDScore(true) = %v, want 1.0", got)
	}
	if got := AgentsMDScore(false); got != 0.0 {
		t.Errorf("AgentsMDScore(false) = %v, want 0.0", got)
	}
}

func TestDiversityScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		paths []string
		want  float64
	}{
		{"empty", nil, 0.0},
		{"single_dir", []string{"foo/a.go", "foo/b.go"}, 0.2},
		{"three_dirs", []string{"a/x.go", "b/x.go", "c/x.go"}, 0.6},
		{"five_dirs_max", []string{"a/x", "b/x", "c/x", "d/x", "e/x"}, 1.0},
		{"more_than_five_clamps", []string{"a/x", "b/x", "c/x", "d/x", "e/x", "f/x"}, 1.0},
		{"root_files_share_bucket", []string{"a.go", "b.go"}, 0.2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := prnoise.DiffStats{FilePaths: tc.paths}
			if got := DiversityScore(d); !near(got, tc.want) {
				t.Errorf("DiversityScore(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}

func TestChurnScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		samples []prnoise.ChurnSample
		want    float64
	}{
		{"empty_means_quiet", nil, 1.0},
		{"all_zero_touches", []prnoise.ChurnSample{{FilePath: "a", Touches30: 0}}, 1.0},
		{"mean_25", []prnoise.ChurnSample{{Touches30: 25}}, 0.5},
		{"mean_50_floor", []prnoise.ChurnSample{{Touches30: 50}}, 0.0},
		{"mean_over_50_clamps", []prnoise.ChurnSample{{Touches30: 100}}, 0.0},
		{"mixed_average", []prnoise.ChurnSample{{Touches30: 0}, {Touches30: 50}}, 0.5},
		{"negative_treated_as_zero", []prnoise.ChurnSample{{Touches30: -10}}, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := prnoise.DiffStats{ChurnSamples: tc.samples}
			if got := ChurnScore(d); !near(got, tc.want) {
				t.Errorf("ChurnScore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRuleBasedQualityScorer_WeightedSum(t *testing.T) {
	t.Parallel()
	w := prnoise.DefaultConfig().QualityWeights
	s := NewRuleBasedQualityScorer(w)

	// PR with all signals at 1.0 must yield exactly Sum(weights) = 1.0.
	in := prnoise.PRInput{
		DiffStats: prnoise.DiffStats{
			Additions: 0, Deletions: 0,
			FilePaths: []string{"a/x", "b/x", "c/x", "d/x", "e/x"},
			// empty churn samples → ChurnScore = 1.0
		},
		CIResult:   prnoise.CIResult{Build: true, Test: true, LintViolations: 0, CoverageDelta: 0.10},
		AgentsMDOK: true,
	}
	got := s.Score(context.Background(), in)
	if !near(got, 1.0) {
		t.Errorf("all-perfect input: Score = %v, want 1.0", got)
	}

	// PR with all signals at 0.0 must yield 0.0.
	bad := prnoise.PRInput{
		DiffStats: prnoise.DiffStats{
			Additions: 5000,
			ChurnSamples: []prnoise.ChurnSample{{Touches30: 100}},
			// no FilePaths → DiversityScore = 0
		},
		CIResult:   prnoise.CIResult{LintViolations: 100, CoverageDelta: -0.5},
		AgentsMDOK: false,
	}
	got = s.Score(context.Background(), bad)
	if !near(got, 0.0) {
		t.Errorf("all-zero input: Score = %v, want 0.0", got)
	}
}

func TestRuleBasedQualityScorer_BoundedToUnitInterval(t *testing.T) {
	t.Parallel()
	// Defensive: out-of-range weights should not let Score escape [0,1].
	weird := prnoise.QualityWeights{Size: 10, AgentsMD: 10}
	s := NewRuleBasedQualityScorer(weird)
	in := prnoise.PRInput{AgentsMDOK: true}
	got := s.Score(context.Background(), in)
	if got < 0 || got > 1 {
		t.Errorf("Score = %v, want clamped to [0,1]", got)
	}
}
