// Package issuenoise classifies inbound issues — produced largely by AI
// agents — into one of four routing verdicts: normal, low_quality,
// duplicate, or spam. The package owns the maintainer-queue lifecycle
// (held → released | auto-closed-on-TTL) and writes a full audit trail
// through the outbox.
//
// Plane boundary (ADR-019):
//
//   - This package lives in the application plane. It MUST NOT import
//     plane/git/..., plane/edge/..., or workflow-plane runtime
//     packages. Temporal interactions go through a workflow-starter
//     interface (see WorkflowStarter) that is wired to a real Temporal
//     client at the cmd/ boot layer.
//   - It MAY import plane/data/store interfaces and plane/data/cache
//     interfaces. It MUST NOT import a concrete pgx driver — store
//     interactions are mediated by DecisionStore + IssueWriter.
//   - Vespa is the duplicate-detection backend (ADR-016). The Qdrant
//     client is explicitly NOT imported here; Qdrant is reserved for
//     PR dedup (#116).
//
// Outbox conformance (ADR-008):
//
//   - Router.Route writes one issues-row state change + one
//     issue_noise_decisions row + one outbox row in a single Tx.
//   - Router.Release does the same for maintainer-driven releases.
//   - Caller is acknowledged on commit. Temporal workflow start is a
//     post-commit, idempotent side-effect; a reconciler sweeps held
//     issues missing a running workflow.
//
// Swap surfaces (ADR-017):
//
//   - IssueScorer is the abstraction that decouples rule-based scoring
//     from the (deferred) ML-based scorer. RuleScorer is the v1 impl.
//   - DecisionStore + IssueWriter + ThresholdsProvider are the data
//     plane surfaces. WorkflowStarter is the workflow-plane surface.
//
// Dark-launch:
//
//   - The package honours the issue_noise.enforce flag passed via
//     RouterConfig. While enforce is false (first 14 days), Router
//     computes the verdict, writes the decision row + outbox event,
//     but does NOT change the issue state away from "open" — i.e. the
//     verdict is observed but not enforced. This is the supervisor-
//     mandated rollout protocol from the spec.
//
// Fail-open:
//
//   - Scorer errors are logged, increment issue_noise_scorer_errors_total,
//     and fall back to VerdictNormal so a misbehaving scorer cannot
//     block the issue submission path. This is silent-failure-hunter
//     bait by design — the metric + alert are the contract.
//
// References:
//   - Issue: https://github.com/gitscale-platform/gitscale/issues/117
//   - Spec:  docs/superpowers/specs/2026-05-09-issue-117-issue-noise-filtering-design.md
//   - Plan:  docs/superpowers/plans/2026-05-09-issue-117-issue-noise-filtering-plan.md
//   - ADR-008 (outbox), ADR-016 (Vespa search), ADR-017 (swap surface),
//     ADR-019 (plane boundary).
package issuenoise
