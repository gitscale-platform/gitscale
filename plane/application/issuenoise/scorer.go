package issuenoise

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Signal is a single contributing factor to a Score, recorded for audit
// and operator-tuning. Name is a stable identifier (e.g. "link_density",
// "reputation"); Weight is the scalar contribution (post-clamping it may
// be reduced when the category sum exceeds 1.0); Detail is a human-
// readable description ("8 links / 412 chars", "agent_reputation=0.28").
type Signal struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail"`
}

// Score is the structured output of an IssueScorer for one IssueDraft.
// All three category fields are clamped to [0, 1]. Duplicate is special:
// when > 0, DuplicateOf is set to the candidate parent issue id.
type Score struct {
	Spam          float64    `json:"spam"`
	LowQuality    float64    `json:"low_quality"`
	Duplicate     float64    `json:"duplicate"`
	DuplicateOf   *uuid.UUID `json:"duplicate_of,omitempty"`
	Signals       []Signal   `json:"signals"`
	ScorerVersion string     `json:"scorer_version"`
}

// IssueDraft is the input to IssueScorer.Score. It is the not-yet-
// committed projection of the issue the caller is about to insert.
type IssueDraft struct {
	ID         uuid.UUID
	RepoID     uuid.UUID
	ReporterID uuid.UUID
	Title      string
	Body       string
	CreatedAt  time.Time
}

// IssueScorer is the swap surface between rule-based scoring (today)
// and the deferred ML scorer (July 2026 open arch question). Concrete
// implementations include *RuleScorer; call sites depend only on this
// interface.
type IssueScorer interface {
	// Score evaluates d and returns a Score. Implementations must be
	// safe for concurrent use. An error indicates the scorer was unable
	// to produce a verdict; callers fall back to VerdictNormal and
	// increment issue_noise_scorer_errors_total (fail-open contract).
	Score(ctx context.Context, d IssueDraft) (Score, error)
}

// clamp01 limits x to the [0, 1] interval. Used by every rule and the
// scorer aggregator to enforce the contract that category sums are
// probabilities, not raw weights.
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
