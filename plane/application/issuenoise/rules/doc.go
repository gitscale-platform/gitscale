// Package rules holds the individual classification rules that the
// RuleScorer composes. Each rule is independently testable: pure
// functions where possible, mocked dependencies otherwise.
//
// A rule produces a Signal (name + weight + detail) and tags it as
// contributing to one of three Score categories: spam, low_quality,
// or duplicate. The RuleScorer aggregates per-category signals and
// clamps each category to [0, 1].
//
// Plane boundary (ADR-019): rules are pure logic packages — they
// import only stdlib + plane/data interfaces. They never hold a pgx
// pool, never call out over the network directly except through
// passed-in interfaces.
package rules

import (
	"context"

	"github.com/google/uuid"
)

// Category names a Score field a Signal contributes to.
type Category int

const (
	// CategorySpam contributes to Score.Spam.
	CategorySpam Category = iota
	// CategoryLowQuality contributes to Score.LowQuality.
	CategoryLowQuality
	// CategoryDuplicate contributes to Score.Duplicate (and may set
	// DuplicateOf).
	CategoryDuplicate
)

// Signal is the rule output shape. Mirrors issuenoise.Signal but
// kept local to avoid an import cycle with the parent package; the
// scorer translates between the two.
type Signal struct {
	Name   string
	Weight float64
	Detail string
}

// Result is what every rule returns. DuplicateOf is set only by the
// duplicate rule; other rules leave it nil.
type Result struct {
	Signal      Signal
	Category    Category
	DuplicateOf *uuid.UUID
}

// Input is the read-only projection a rule receives. Mirrors
// issuenoise.IssueDraft to avoid an import cycle.
type Input struct {
	IssueID    uuid.UUID
	RepoID     uuid.UUID
	ReporterID uuid.UUID
	Title      string
	Body       string
}

// Rule is the function signature every rule implements. A rule that
// has nothing to contribute returns Result{} (zero Signal.Weight); the
// scorer ignores zero-weight signals.
type Rule func(ctx context.Context, in Input) (Result, error)
