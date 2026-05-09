// Package prnoise owns the PR noise pipeline that scores incoming pull
// requests across four stages — semantic dedup, quality signals, agent
// reputation, and a composite router — and persists the resulting Decision
// alongside an outbox event for downstream consumers (webhook delivery,
// audit, future reputation feedback loop).
//
// The pipeline is deterministic: same PRInput → same Decision (modulo the
// embedder, which is an injected dependency). All sub-scores live in
// [0, 1] and are uniformly weighted.
//
// ADR alignment:
//
//   - ADR-008 (outbox): RecordDecision writes the prnoise decision row and
//     a pr.noise_decision_recorded outbox row in the same Tx. Callers ack on
//     DB commit, never on Kafka publish.
//   - ADR-016 (Vespa primary, Qdrant for PR dedup): Stage 1 issues a
//     cosine ANN query against Qdrant at the non-configurable threshold
//     of 0.92 quoted verbatim from the ADR. Vespa is untouched by this
//     package.
//   - ADR-017 (swap surfaces): ReputationScorer and QualitySignalScorer
//     are interfaces. The current shipping impl is rule-based; the open
//     question (CLAUDE.md, July 2026) over an ML reputation model is
//     resolved by swapping in a new ReputationScorer with no call-site
//     changes.
//   - ADR-019 (plane boundary): the package depends only on the identity
//     service (in-process, application-plane), an injected MetadataStore
//     for outbox writes, an injected Embedder, and a Qdrant client. No
//     imports from plane/git, plane/workflow, or plane/edge.
//   - ADR-021 (Qdrant role): Qdrant is consumed exclusively by this
//     package and exclusively for PR dedup.
//
// Cross-org dedup is gated behind the prnoise.cross_org_dedup feature
// flag, default OFF (CLAUDE.md open question, August 2026 decision).
//
// PR-state mutation (close, label, comment) is the responsibility of the
// webhook delivery worker subscribed to pr.noise_decision_recorded, not
// of any code in this package — see ADR-008 acknowledgement boundary.
package prnoise
