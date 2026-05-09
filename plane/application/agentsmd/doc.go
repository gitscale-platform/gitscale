// Package agentsmd parses, lints, merges, and evaluates AGENTS.md
// policy documents — the GitScale mechanism by which humans communicate
// hard constraints to AI agents.
//
// This package is pure logic. It MUST NOT import anything from
// plane/git/...; the adapter that bridges into the pre-receive hook lives
// in the sibling package plane/application/agentsmd/hook (see ADR-019 on
// plane boundaries).
//
// Schema: only `gitscale/v1` is accepted in this iteration. The schema
// versioning policy is an open architecture question (target: July 2026);
// once resolved, the parser will gain a version-tracking strategy.
//
// Public surface:
//
//   - Parse([]byte) (*Policy, []Diagnostic, error) — extract a policy plus
//     structured diagnostics. A nil/empty input yields an "empty" policy.
//   - Lint([]byte) []Diagnostic — diagnostics-only convenience wrapper.
//   - Merge(org, repo *Policy) *Policy — combine policies; org wins on
//     conflicting predicates.
//   - Evaluate(*Policy, EvaluationInput) []Violation — evaluate predicates
//     against a set of ref updates.
//
// References:
//   - Issue: https://github.com/gitscale-platform/gitscale/issues/114
//   - Spec:  docs/superpowers/specs/2026-05-09-issue-114-agents-md-never-enforcement-design.md
//   - ADR-008 (outbox), ADR-012 (hook layer), ADR-019 (plane boundary).
package agentsmd
