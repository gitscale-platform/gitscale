package rules

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DuplicateCandidate is a single result returned by the search
// backend. Score is normalized to [0, 1] (the wrapper translates
// Vespa's native scoring).
type DuplicateCandidate struct {
	IssueID uuid.UUID
	Score   float64
}

// DuplicateSearcher is the search-backend surface — a Vespa client
// wrapper in production (ADR-016: Vespa is the customer-facing search;
// Qdrant is reserved for PR dedup). Implementations MUST scope the
// query to the same repo and to issues with state in {open, held}.
type DuplicateSearcher interface {
	FindCandidates(ctx context.Context, repoID uuid.UUID, body string, k int) ([]DuplicateCandidate, error)
}

// DuplicateMaxK is the top-k requested from the search backend.
const DuplicateMaxK = 3

// Duplicate returns a rule closure that consults s. The rule emits
// CategoryDuplicate with the top candidate's normalized score and
// sets Result.DuplicateOf so the router can link the parent in the
// audit trail.
//
// On searcher error, the rule returns the error — duplicate detection
// is one of two rules whose failure surfaces (the other is the
// scorer-level fail-open). Operators can disable the rule entirely
// via a feature flag if the Vespa schema is not yet wired (see
// spec risks).
func Duplicate(s DuplicateSearcher) Rule {
	return func(ctx context.Context, in Input) (Result, error) {
		if s == nil {
			return Result{}, nil
		}
		// Don't fire on tiny bodies — too little signal for similarity.
		if len(in.Body) < 30 {
			return Result{}, nil
		}
		cands, err := s.FindCandidates(ctx, in.RepoID, in.Body, DuplicateMaxK)
		if err != nil {
			return Result{}, fmt.Errorf("duplicate: %w", err)
		}
		if len(cands) == 0 {
			return Result{}, nil
		}
		top := cands[0]
		// Don't link to ourself if the search backend includes the
		// in-flight draft (which it shouldn't, but defend in depth).
		if top.IssueID == in.IssueID {
			if len(cands) < 2 {
				return Result{}, nil
			}
			top = cands[1]
		}
		dupOf := top.IssueID
		return Result{
			Category:    CategoryDuplicate,
			DuplicateOf: &dupOf,
			Signal: Signal{
				Name:   "duplicate_vespa",
				Weight: top.Score,
				Detail: fmt.Sprintf("top=%s score=%.3f", top.IssueID, top.Score),
			},
		}, nil
	}
}
