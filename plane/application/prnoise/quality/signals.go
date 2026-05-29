package quality

import (
	"path"
	"strings"

	"github.com/gitscale-platform/gitscale/plane/application/prnoise"
)

// clamp01 caps x to the closed interval [0, 1]. Inlined into each
// signal so out-of-range inputs never leak past the contributor.
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// SizeScore: clamp(1 - (additions + deletions) / 1000, 0, 1).
//
// 0-line PR scores 1.0; 1000-line PR scores 0.0; linear in between.
// Negative inputs (defensive) are treated as 0.
func SizeScore(d prnoise.DiffStats) float64 {
	add := d.Additions
	del := d.Deletions
	if add < 0 {
		add = 0
	}
	if del < 0 {
		del = 0
	}
	total := float64(add + del)
	return clamp01(1 - total/1000.0)
}

// TestCoverageScore: clamp(0.5 + coverage_delta * 5, 0, 1).
//
// Flat coverage = 0.5. +0.10 (10pp gain) → 1.0. -0.10 (10pp loss) → 0.0.
// The ×5 multiplier matches the spec table.
func TestCoverageScore(c prnoise.CIResult) float64 {
	return clamp01(0.5 + c.CoverageDelta*5)
}

// CIBuildScore: 1.0 iff Build && Test pass; else 0.0. Lint is scored
// separately by LintScore.
func CIBuildScore(c prnoise.CIResult) float64 {
	if c.Build && c.Test {
		return 1.0
	}
	return 0.0
}

// LintScore: clamp(1 - new_violations / 50, 0, 1).
//
// Zero violations score 1.0; 50+ violations score 0.0. Negative
// counts (defensive) are treated as zero.
func LintScore(c prnoise.CIResult) float64 {
	v := c.LintViolations
	if v < 0 {
		v = 0
	}
	return clamp01(1 - float64(v)/50.0)
}

// AgentsMDScore: 1.0 iff the AGENTS.md compliance gate passed; else 0.0.
// The gate itself lives in issue #114 (the source of truth for AGENTS.md
// content); this signal only consumes its boolean result.
func AgentsMDScore(ok bool) float64 {
	if ok {
		return 1.0
	}
	return 0.0
}

// DiversityScore: clamp(unique_top_dirs(file_paths) / 5, 0, 1).
//
// Five+ distinct top-level directories is "max diversity". Empty path
// lists score 0.0; root-level files share an empty top-dir bucket.
func DiversityScore(d prnoise.DiffStats) float64 {
	if len(d.FilePaths) == 0 {
		return 0.0
	}
	seen := make(map[string]struct{}, len(d.FilePaths))
	for _, p := range d.FilePaths {
		seen[topDir(p)] = struct{}{}
	}
	return clamp01(float64(len(seen)) / 5.0)
}

// topDir returns the first path segment of p; "/" or empty paths return
// "" (rooted-bucket). Used by DiversityScore.
func topDir(p string) string {
	cleaned := path.Clean(p)
	cleaned = strings.TrimPrefix(cleaned, "/")
	idx := strings.IndexByte(cleaned, '/')
	if idx < 0 {
		// File at repo root — bucket together under empty key.
		return ""
	}
	return cleaned[:idx]
}

// ChurnScore: clamp(1 - mean(touches_30d) / 50, 0, 1).
//
// High-churn paths (mean ≥ 50 touches in 30d) score 0.0; quiet paths
// score 1.0. An empty sample list is treated as quiet (1.0) to avoid
// penalising PRs that touch newly-created files.
func ChurnScore(d prnoise.DiffStats) float64 {
	if len(d.ChurnSamples) == 0 {
		return 1.0
	}
	var sum int
	for _, s := range d.ChurnSamples {
		t := s.Touches30
		if t < 0 {
			t = 0
		}
		sum += t
	}
	mean := float64(sum) / float64(len(d.ChurnSamples))
	return clamp01(1 - mean/50.0)
}
