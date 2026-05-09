package policy

import (
	"path"
	"strings"
)

// matchRule reports whether r fires against actions submitted by proposer.
// A rule fires when at least one action satisfies the predicate-specific
// criteria (or, for bulk_action, when the cardinality threshold is met, and
// for agent_default, when the proposer is an agent and no earlier rule fired).
//
// The "no earlier rule fired" check is the caller's responsibility — the
// engine evaluates rules in order and matchRule has no view of prior matches.
// agent_default therefore matches purely on proposer_kind here; the first-
// match-wins loop in Engine.SubmitPlan ensures the catch-all only wins when
// nothing earlier did.
func matchRule(r Rule, actions []Action, proposer ProposerKind) bool {
	switch r.Kind {
	case PredicatePRMerge:
		return matchByField(actions, "pr_merge", "branch", r.Match["branch"])
	case PredicateForcePush:
		return matchByPattern(actions, "force_push", "ref", r.Match["ref_pattern"])
	case PredicateProductionDeploy:
		return matchByField(actions, "production_deploy", "environment", r.Match["environment"])
	case PredicateBulkAction:
		if r.Threshold == nil {
			return false
		}
		return len(actions) >= *r.Threshold
	case PredicateAgentDefault:
		return proposer == ProposerKindAgent
	}
	return false
}

// matchByField reports whether any action of the given kind has a field
// whose value equals want. An empty want matches any value (so a rule with
// no Match constraint fires on every action of the kind).
func matchByField(actions []Action, kind, field, want string) bool {
	for _, a := range actions {
		if a.Kind != kind {
			continue
		}
		if want == "" {
			return true
		}
		if a.Fields[field] == want {
			return true
		}
	}
	return false
}

// matchByPattern reports whether any action of kind has a field matching
// the glob pattern. Patterns are filepath-style globs; an empty pattern
// matches any value. Used by force_push for ref_pattern matching like
// "refs/heads/release/*".
func matchByPattern(actions []Action, kind, field, pattern string) bool {
	for _, a := range actions {
		if a.Kind != kind {
			continue
		}
		v := a.Fields[field]
		if pattern == "" {
			return true
		}
		// path.Match returns ErrBadPattern only for malformed patterns; we
		// treat that as a non-match and let validation surface upstream.
		if ok, err := path.Match(pattern, v); err == nil && ok {
			return true
		}
		// Convenience: trailing-slash and prefix matching for refs like
		// "refs/heads/release/" matching "refs/heads/release/v1".
		if strings.HasSuffix(pattern, "/*") && strings.HasPrefix(v, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}
