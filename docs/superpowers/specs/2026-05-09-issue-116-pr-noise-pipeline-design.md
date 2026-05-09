# Spec — Issue #116 PR noise pipeline enforcement (semantic dedup + quality signals + composite routing)

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/116
Plane: application
Priority: p2 (Phase 2)
ADR-impact: conforming (ADR-008 outbox, ADR-016 Vespa/Qdrant split, ADR-017 swap surfaces, ADR-019 plane boundary, ADR-021 Qdrant role)

## Problem

Phase 1 ran the PR noise pipeline in shadow mode: scores were
computed and logged but never blocked a PR. The Phase 1→2 gate
criterion (precision ≥ 0.6, recall ≥ 0.7 against the design-partner
corpus) is met. We now need to switch the pipeline to **enforcement**:
hold or auto-close PRs that score below threshold, and route the
remainder into the maintainer review queue.

The design also pre-empts the open architectural question (CLAUDE.md
"PR reputation model: rule-based vs. ML-based, July 2026"): we ship
**rule-based only** today, behind a `ReputationScorer` interface so the
ML implementation can land without touching call sites.

## Goals

1. Add a `plane/application/prnoise` package owning the four-stage
   pipeline (semantic dedup → quality signals → reputation lookup →
   composite routing) with a single `Pipeline.Score(ctx, PRInput)
   (Decision, error)` entry point.
2. Implement semantic dedup against Qdrant at the **cosine similarity
   threshold 0.92 mandated by ADR-016**: "Qdrant is reserved for PR
   deduplication only, with a cosine similarity threshold of 0.92."
3. Implement a deterministic, configurable rule-based quality scorer
   over six signals: PR size, test coverage delta, CI/build success,
   lint pass rate, AGENTS.md compliance, file diversity, and churn.
4. Plumb a `ReputationScorer` interface (ADR-017 swap surface) and ship
   a `RuleBasedReputationScorer` reading `AgentIdentity.ReputationScore`
   directly. Wire the future ML implementation behind the same surface.
5. Implement a composite router that maps the `(dedup, quality,
   reputation)` triple to one of three terminal decisions:
   `auto_merge_eligible`, `maintainer_review`, `reject` (high noise).
6. Persist the decision and write a `pr.noise_decision_recorded` outbox
   row in the same Tx as the source row (ADR-008). Webhook delivery
   and PR state mutation (close, label, comment) consume the event
   downstream — never inline.
7. Cross-org dedup is gated behind a feature flag `prnoise.cross_org_dedup`
   defaulted **OFF until August 2026** (CLAUDE.md open question).
8. Remove Phase 1 shadow-mode plumbing once enforcement is live.

## Non-goals

- ML-based reputation scoring (open question; July 2026).
- A new search surface for code/issues — Vespa is the primary search
  backend per ADR-016; Qdrant is **only** consumed by this package.
- Owning the embedding model. The embedder is an injected dependency
  (`Embedder` interface); model choice and hosting are tracked
  separately.
- Inline PR-state mutation. Auto-close/hold/label happen in a webhook
  delivery worker subscribed to `pr.noise_decision_recorded`, not in
  the request handler.
- GraphQL surface for the decision (issue #113, in flight).
- Cross-org dedup behaviour beyond exposing the feature flag.
- Tuning weights against the design-partner corpus — that work is the
  acceptance gate, not a code change to this package.

## Design decisions (defaults selected by supervisor)

| Question | Choice | Rationale |
|---|---|---|
| Reputation model | **Rule-based only**, behind `ReputationScorer` interface | CLAUDE.md open question gates ML until July 2026; the interface defers the choice without blocking enforcement (ADR-017). |
| Dedup threshold | **0.92 cosine similarity** | Quoted directly from ADR-016 §Decision; not a tunable. |
| Cross-org dedup | Feature flag `prnoise.cross_org_dedup`, default OFF | CLAUDE.md open question — August 2026 decision. The flag is read once at pipeline construction, not per request, to avoid scoring drift mid-evaluation. |
| Decision codes | Closed enum: `auto_merge_eligible`, `maintainer_review`, `reject` | Stable contract for downstream consumers (webhooks, audit, billing). |
| Score range | All sub-scores in `[0, 1]`; composite in `[0, 1]` | Matches reputation field; allows uniform weight algebra. |
| Where mutation happens | Outbox-driven webhook worker | ADR-008: caller acks on DB commit, never on Kafka publish; PR-state side effects are downstream consumers. |
| Quality scorer extensibility | `QualitySignalScorer` interface, ship `RuleBasedQualityScorer` | Future-proof for ML quality signals without an ADR change. |
| Embedder source | Injected `Embedder` interface; production wires the same embedding service that Phase 1 used | Keeps this PR scoped to enforcement; embedder choice is orthogonal. |
| Pipeline transactionality | Decision record + outbox row in one Tx; **no Qdrant write inside that Tx** | Qdrant writes are best-effort and idempotent on `pr_id`; failure to upsert into Qdrant after commit retries via outbox consumer. |

## Architecture

### Package layout

```
plane/application/prnoise/
  doc.go                   package doc, ADR-016 / ADR-021 citations, plane boundary
  models.go                PRInput, Decision, DecisionCode, scoring records
  pipeline.go              Pipeline struct, Score() entry point
  pipeline_test.go         table-driven scoring, deterministic
  events.go                EventTypeNoiseDecisionRecorded = "pr.noise_decision_recorded", payload struct
  service.go               Service interface (Score + persist)
  postgres_service.go      PostgresService — opens Tx, writes source row + outbox row
  postgres_service_test.go testcontainers PG service-level
  stub_service.go          in-memory StubService for upstream tests
  stub_service_test.go
  integration_test.go      testcontainers PG end-to-end
  config.go                Weights, thresholds, feature flags (cross-org dedup)
  config_test.go
  dedup/
    qdrant_client.go       Qdrant client wrapper (cosine threshold 0.92 from ADR-016)
    qdrant_client_test.go  testcontainers Qdrant
    dedup.go               Deduper interface, QdrantDeduper impl
    dedup_test.go
    embedder.go            Embedder interface
  quality/
    scorer.go              QualitySignalScorer interface
    rule_based.go          RuleBasedQualityScorer impl + signal weights
    rule_based_test.go     exhaustive sub-signal tests
    signals.go             Signal contributors (size, coverage, lint, AGENTS.md, diversity, churn)
  reputation/
    scorer.go              ReputationScorer interface (ADR-017 swap surface)
    rule_based.go          RuleBasedReputationScorer reads AgentIdentity.ReputationScore
    rule_based_test.go
  router/
    composite.go           CompositeRouter: (dedup, quality, reputation) → DecisionCode
    composite_test.go
```

A new binary is **not** added in this issue — the pipeline is consumed
by the existing PR engine handler in the application plane and by the
webhook delivery worker (subscribed via outbox/Kafka).

### Pipeline contract

```go
type PRInput struct {
    PRID         uuid.UUID
    RepoID       uuid.UUID
    OrgID        uuid.UUID
    AgentID      uuid.UUID            // empty UUID if PR opened by a human
    Title        string
    Description  string
    DiffStats    DiffStats            // additions, deletions, files_changed, file_paths
    CIResult     CIResult             // build, test, lint, coverage_delta
    AgentsMDOK   bool                 // result of AGENTS.md compliance gate (issue #114)
    OpenedAt     time.Time
}

type DiffStats struct {
    Additions     int
    Deletions     int
    FilesChanged  int
    FilePaths     []string             // for diversity signal
    ChurnSamples  []ChurnSample        // 30d touch counts per file
}

type Decision struct {
    PRID            uuid.UUID
    DedupScore      float64            // [0,1]; 1.0 = duplicate at ≥0.92 cosine
    DuplicateOf     *uuid.UUID         // nil if not a duplicate
    QualityScore    float64            // [0,1]
    ReputationScore float64            // [0,1]
    CompositeScore  float64            // [0,1]
    Code            DecisionCode       // closed enum
    Reason          string             // human-readable, stable across runs
    DecidedAt       time.Time
}

type DecisionCode string
const (
    DecisionAutoMergeEligible DecisionCode = "auto_merge_eligible"
    DecisionMaintainerReview  DecisionCode = "maintainer_review"
    DecisionReject            DecisionCode = "reject"
)
```

### Stage 1 — Semantic dedup (Qdrant, ADR-016 / ADR-021)

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}

type Deduper interface {
    NearestDuplicate(ctx context.Context, repoID uuid.UUID, vec []float32) (*DuplicateHit, error)
}

type DuplicateHit struct {
    PRID       uuid.UUID
    Similarity float64                   // cosine in [-1, 1]
}
```

`QdrantDeduper.NearestDuplicate` issues an ANN query scoped to one
Qdrant collection per repository (`prnoise_pr_<repo_id>`) — or, when
`prnoise.cross_org_dedup` is OFF (default), filters by `repo_id`
within a shared collection per region. Cross-org dedup, when enabled,
removes the filter at construction time only.

Decision logic:

| Top-1 cosine | DedupScore | DuplicateOf |
|---|---|---|
| ≥ 0.92 | 1.0 | the matched PR ID |
| < 0.92 | 0.0 | nil |

The threshold is wired from a constant `DedupCosineThreshold = 0.92`
that is **not configurable**. Tuning it would silently change ADR-016
contract surface; a change requires an ADR amendment.

### Stage 2 — Quality signals (rule-based)

`RuleBasedQualityScorer.Score(ctx, PRInput) float64` computes a
weighted average of six sub-signals. Each sub-signal returns a value
in `[0, 1]` (1.0 is best).

| Signal | Weight | Computation |
|---|---:|---|
| `size` | 0.20 | `clamp(1 - (additions + deletions) / 1000, 0, 1)` — 1000-line PR scores 0; 0-line scores 1; linear in between. |
| `test_coverage` | 0.20 | `clamp(0.5 + coverage_delta * 5, 0, 1)` — flat coverage = 0.5; +10% → 1.0; -10% → 0.0. |
| `ci_build` | 0.15 | 1.0 if `CIResult.Build == pass` and `CIResult.Test == pass`; 0.0 otherwise. |
| `lint` | 0.10 | `clamp(1 - new_violations / 50, 0, 1)`. |
| `agents_md` | 0.20 | 1.0 if `AgentsMDOK`; 0.0 otherwise. Issue #114 is the source of truth. |
| `diversity` | 0.10 | `clamp(unique_top_dirs(file_paths) / 5, 0, 1)` — five+ top-level dirs is max diversity. |
| `churn` | 0.05 | `clamp(1 - mean(touches_30d) / 50, 0, 1)` — high-churn paths penalised. |

Weights sum to 1.00 and are exposed as a `QualityWeights` struct in
`config.go`. Weight changes are configuration, not code, but every
production deployment ships the defaults above.

### Stage 3 — Reputation (interface, rule-based impl)

```go
type ReputationScorer interface {
    Score(ctx context.Context, agentID uuid.UUID) (float64, error)  // [0, 1]
}

type RuleBasedReputationScorer struct {
    Identity identity.Service
}
```

`RuleBasedReputationScorer.Score` looks up the agent via
`identity.Service.GetAgent` and returns `agent.ReputationScore`
directly. Human-authored PRs (empty `AgentID`) score 1.0 — humans are
trusted by default at this stage; abuse mitigation is handled at the
edge plane (rate-limit, identity revocation), not here.

The interface exists so that the July-2026 ML model can be swapped in
without touching pipeline call sites or composite-router math
(ADR-017).

### Stage 4 — Composite routing

```go
type CompositeRouter struct {
    Weights       CompositeWeights
    AutoMergeBand float64    // composite ≥ this → auto_merge_eligible
    RejectBand    float64    // composite <  this → reject
}
```

Default bands and weights:

| Knob | Default | Notes |
|---|---:|---|
| `Weights.Quality` | 0.5 | Quality dominates the composite. |
| `Weights.Reputation` | 0.4 | Reputation second. |
| `Weights.DedupPenalty` | 1.0 | Multiplies `(1 - DedupScore)` — duplicate (DedupScore=1) zeroes the composite. |
| `AutoMergeBand` | 0.85 | Above this we route to auto-merge eligible. |
| `RejectBand` | 0.30 | Below this we reject (auto-close downstream). |

Routing math:

```
base       = Weights.Quality * QualityScore + Weights.Reputation * ReputationScore
composite  = base * (1 - DedupScore * Weights.DedupPenalty)

if DuplicateOf != nil          → reject (with Reason="duplicate of <id>")
else if composite >= AutoMerge → auto_merge_eligible
else if composite >= Reject    → maintainer_review
else                            → reject
```

`Reason` is stable and machine-readable: `"duplicate"`,
`"composite_below_reject"`, `"composite_in_review_band"`,
`"composite_above_auto_merge"`. Used by the audit trail and the
rejection-comment template.

### Schema

New migration: `plane/data/migrations/NNN_pr_noise_decisions.sql`.

```sql
CREATE TABLE collaboration.pr_noise_decisions (
  pr_id             UUID PRIMARY KEY,
  repo_id           UUID NOT NULL,
  org_id            UUID NOT NULL,
  agent_id          UUID,
  dedup_score       DOUBLE PRECISION NOT NULL CHECK (dedup_score BETWEEN 0 AND 1),
  duplicate_of      UUID,
  quality_score     DOUBLE PRECISION NOT NULL CHECK (quality_score BETWEEN 0 AND 1),
  reputation_score  DOUBLE PRECISION NOT NULL CHECK (reputation_score BETWEEN 0 AND 1),
  composite_score   DOUBLE PRECISION NOT NULL CHECK (composite_score BETWEEN 0 AND 1),
  decision_code     TEXT NOT NULL CHECK (decision_code IN
                      ('auto_merge_eligible','maintainer_review','reject')),
  reason            TEXT NOT NULL,
  decided_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX pr_noise_decisions_repo_idx
  ON collaboration.pr_noise_decisions (repo_id, decided_at DESC);
```

A re-scoring of an existing PR upserts (`ON CONFLICT (pr_id) DO
UPDATE`) and emits a new outbox row. Downstream consumers must be
idempotent on `event_id`, not `pr_id` (ADR-008).

### Outbox event

```go
const EventTypeNoiseDecisionRecorded = "pr.noise_decision_recorded"

type NoiseDecisionRecordedPayload struct {
    PRID            uuid.UUID    `json:"pr_id"`
    RepoID          uuid.UUID    `json:"repo_id"`
    OrgID           uuid.UUID    `json:"org_id"`
    AgentID         *uuid.UUID   `json:"agent_id,omitempty"`
    DecisionCode    DecisionCode `json:"decision_code"`
    DedupScore      float64      `json:"dedup_score"`
    DuplicateOf     *uuid.UUID   `json:"duplicate_of,omitempty"`
    QualityScore    float64      `json:"quality_score"`
    ReputationScore float64      `json:"reputation_score"`
    CompositeScore  float64      `json:"composite_score"`
    Reason          string       `json:"reason"`
    DecidedAt       time.Time    `json:"decided_at"`
}
```

Consumers (in this repo or follow-ups):

- **Webhook delivery worker** — translates `reject` to PR-close + comment;
  `auto_merge_eligible` to a `pr_ready_for_merge` label; `maintainer_review`
  to a routing label.
- **Audit pipeline** — writes the decision into the audit log for
  precision/recall measurement against the design-partner corpus.
- **Reputation feedback loop** (future) — decrement
  `AgentIdentity.ReputationScore` for `reject` outcomes via
  `SetAgentReputationScore`.

### Plane boundary (ADR-019)

`plane/application/prnoise` is purely application-plane. It calls only:

- `identity.Service` (in-process, application-plane)
- `store.MetadataStore` Tx for source + outbox writes (data-plane interface)
- `dedup.Deduper` (Qdrant client; data-plane infrastructure, but the
  client is configured at the app-plane edge — Qdrant is a sibling
  data store under ADR-016, not a cross-plane RPC)
- Injected `Embedder` (orthogonal infrastructure)

No imports from `plane/git/internal/**`, `plane/workflow/internal/**`,
or `plane/edge/**`.

### Phase 1 shadow-mode removal

Phase 1 introduced a feature flag `prnoise.shadow` and a parallel
`prnoise_shadow_decisions` table. Both are deleted in this issue:

- `DROP TABLE collaboration.prnoise_shadow_decisions` (migration).
- `prnoise.shadow` flag references in PR engine handler removed.
- Shadow-only logging in webhook delivery worker removed.

## Test plan

| Layer | Test |
|---|---|
| Unit (rule-based quality) | Each of the seven signal contributors has a table-driven test covering boundary, mid, and out-of-range inputs |
| Unit (composite router) | Bands, dedup zeroing, weight algebra, decision-code enum exhaustiveness |
| Unit (rule-based reputation) | Empty AgentID → 1.0; known agent → forwarded score; unknown → `ErrAgentNotFound` propagated |
| Unit (Qdrant deduper, fake client) | Top-1 ≥ 0.92 → DedupScore=1; top-1 < 0.92 → DedupScore=0; cross-org filter respected when flag OFF |
| Service (testcontainer PG) | First call inserts source + outbox; re-score upserts source + emits new outbox row; concurrent goroutines on same PR — exactly one source row, exactly one extra outbox row per re-score |
| Integration (testcontainer PG + testcontainer Qdrant) | Full pipeline end-to-end: insert seed PRs, embed, score a duplicate → `reject`, score a clean PR → `auto_merge_eligible`, score a borderline PR → `maintainer_review`. Outbox payload JSON matches `EventTypeNoiseDecisionRecorded` schema |
| Feature-flag test | `prnoise.cross_org_dedup=false` filters by repo; `=true` queries across orgs (asserted via Qdrant filter spec captured by a recording client) |
| Determinism | Given a fixed `PRInput`, the pipeline produces a deterministic `Decision` (same composite, same code) — tested with frozen embedder and fake deduper |

All testcontainer tests gated by `//go:build integration`.

## Acceptance checklist (from issue body)

- [ ] Qdrant cosine query at 0.92 threshold correctly identifies duplicates
- [ ] Quality scorer produces a score in `[0, 1]` from coverage + lint + build inputs (and the four other signals listed above)
- [ ] Composite router routes to `auto_merge_eligible` / `maintainer_review` / `reject` per score band
- [ ] Enforcement mode blocks rejected PRs from entering human review queue (downstream webhook consumer responsibility; this PR emits the event the consumer reads)
- [ ] Precision ≥ 0.6, recall ≥ 0.7 on design-partner corpus (acceptance test run, results documented in PR description; not a code artifact)
- [ ] Phase 1 shadow code removed (table dropped, flag references deleted)
- [ ] PR description references ADR-008, ADR-016, ADR-019, ADR-021

## Open questions

- **Reputation feedback loop** (decrement on `reject`): in scope for
  follow-up; this PR ships the event, not the consumer.
- **Composite weights tuning**: defaults are seeded by Phase 1 shadow
  data; production tuning is a config-rollout, not a code change.
- **Cross-org dedup default switch** (CLAUDE.md, August 2026): tracked
  by the open-question entry; this PR exposes the flag, default OFF.
- **ML reputation model** (CLAUDE.md, July 2026): tracked separately;
  `ReputationScorer` interface is the swap surface (ADR-017).

## References

- ADR-008 (outbox pattern) — `docs/architecture.md §8`
- ADR-016 (Vespa primary, Qdrant for PR dedup at cosine 0.92) — `docs/architecture.md §8`
- ADR-017 (swap surfaces) — `docs/architecture.md §8`
- ADR-019 (plane boundary) — `docs/architecture.md §8`
- ADR-021 (Qdrant role: PR dedup only) — referenced from issue body
- Pipeline diagram: `docs/architecture.md §2.6`
- Pattern reference: `plane/application/identity/` (issue #15)
- REST API surface (consumer): `plane/application/restapi/` (issue #111, merged)
- MCP server (consumer): `plane/application/mcp/` (issue #112, merged)
- GraphQL surface (consumer, in flight): issue #113
- AGENTS.md compliance gate (signal source): issue #114
