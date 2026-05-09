package issuenoise

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/application/issuenoise/rules"
)

// RuleScorerVersion is the wire-version stamped on every Score that
// RuleScorer produces. Bump when changing the registry, weights, or
// thresholds — consumers can use it to filter audit rows.
const RuleScorerVersion = "rule-v1"

// RuleScorer is the v1 IssueScorer impl: it walks a fixed registry of
// rule closures, sums per-category weights, clamps each category to
// [0, 1], and returns a Score. The registry is composed at boot time
// via NewRuleScorer; tests can construct a scorer with a single rule
// or no rules at all.
//
// Concurrency: safe for concurrent use as long as the Rule funcs in
// the registry are themselves safe (the in-tree ones all are).
type RuleScorer struct {
	rules []rules.Rule
}

// NewRuleScorer composes the default registry from the dependencies a
// real deployment needs. nil dependencies are tolerated — the
// corresponding rule contributes nothing. This shape lets unit tests
// pass nil for everything and integration tests pass real backends.
func NewRuleScorer(
	repCounter rules.ReporterRateCounter,
	repLookup rules.ReputationLookup,
	violationCounter rules.AgentsMDViolationCounter,
	dupSearcher rules.DuplicateSearcher,
) *RuleScorer {
	return &RuleScorer{
		rules: []rules.Rule{
			rules.LinkDensity,
			rules.Length,
			rules.Language,
			rules.ReporterRate(repCounter),
			rules.Reputation(repLookup),
			rules.AgentsMDViolations(violationCounter),
			rules.Duplicate(dupSearcher),
		},
	}
}

// NewRuleScorerWithRules constructs a scorer over the explicitly-passed
// rule slice. Used by tests that want full control over which rules
// fire; production code should use NewRuleScorer.
func NewRuleScorerWithRules(rs []rules.Rule) *RuleScorer {
	return &RuleScorer{rules: rs}
}

// Score implements IssueScorer. Each rule is run in registration
// order; per-category weights are summed and clamped. The duplicate
// rule's DuplicateOf is propagated to Score.DuplicateOf when present.
//
// Errors from individual rules are aggregated: a single rule failure
// is logged in the Signal detail rather than aborting the scoring
// pass — except for context cancellation, which short-circuits.
func (s *RuleScorer) Score(ctx context.Context, d IssueDraft) (Score, error) {
	in := rules.Input{
		IssueID:    d.ID,
		RepoID:     d.RepoID,
		ReporterID: d.ReporterID,
		Title:      d.Title,
		Body:       d.Body,
	}
	out := Score{ScorerVersion: RuleScorerVersion}
	var firstErr error
	for _, r := range s.rules {
		if err := ctx.Err(); err != nil {
			// Honour cancellation; do not return a partial score on a
			// cancelled request — caller will treat it as a scorer
			// failure (fail-open).
			return Score{}, fmt.Errorf("scorer: %w", err)
		}
		res, err := r(ctx, in)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if res.Signal.Weight == 0 {
			continue
		}
		switch res.Category {
		case rules.CategorySpam:
			out.Spam += res.Signal.Weight
		case rules.CategoryLowQuality:
			out.LowQuality += res.Signal.Weight
		case rules.CategoryDuplicate:
			out.Duplicate += res.Signal.Weight
			if res.DuplicateOf != nil {
				dupOf := *res.DuplicateOf
				out.DuplicateOf = &dupOf
			}
		default:
			// Unknown category — defensive; will not happen for the
			// in-tree rules but ensures a future rule that returns a
			// bogus category fails loudly.
			if firstErr == nil {
				firstErr = errors.New("scorer: rule returned unknown category")
			}
			continue
		}
		out.Signals = append(out.Signals, Signal{
			Name:   res.Signal.Name,
			Weight: res.Signal.Weight,
			Detail: res.Signal.Detail,
		})
	}
	out.Spam = clamp01(out.Spam)
	out.LowQuality = clamp01(out.LowQuality)
	out.Duplicate = clamp01(out.Duplicate)
	if firstErr != nil {
		return out, firstErr
	}
	return out, nil
}
