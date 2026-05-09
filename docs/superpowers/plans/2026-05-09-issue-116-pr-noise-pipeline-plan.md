# Issue #116 PR noise pipeline enforcement — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the PR noise pipeline from Phase 1 shadow mode to Phase 2 enforcement. Ship a four-stage pipeline (semantic dedup at Qdrant cosine 0.92 / quality signals / reputation lookup / composite routing) under `plane/application/prnoise/`, persist decisions, emit outbox events for downstream consumers, and remove shadow-mode plumbing.

**Architecture:** Pipeline package with sub-packages for `dedup`, `quality`, `reputation`, `router`. `ReputationScorer` interface (ADR-017 swap surface) — rule-based impl ships now, ML deferred to July 2026. Decision row + outbox row written in one Tx (ADR-008). Cross-org dedup behind feature flag, default OFF until August 2026.

**Tech Stack:** Go 1.22, pgx/v5, Qdrant Go client, testcontainers-go (PG + Qdrant), google/uuid.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-116-pr-noise-pipeline-design.md`

**Branch:** `feat/application-pr-noise-pipeline-enforcement` (worktree: `../gitscale.worktrees/feat-application-pr-noise-pipeline-enforcement`)

**ADR-impact:** conforming (ADR-008, ADR-016, ADR-017, ADR-019, ADR-021). No new ADR.

---

## File map

### Create

- `plane/application/prnoise/doc.go` — package doc, ADR refs (008/016/017/019/021), plane boundary statement
- `plane/application/prnoise/models.go` — `PRInput`, `Decision`, `DecisionCode` enum, `DiffStats`, `CIResult`
- `plane/application/prnoise/pipeline.go` — `Pipeline` struct, `Score(ctx, PRInput) (Decision, error)`
- `plane/application/prnoise/pipeline_test.go` — table-driven, fake deduper + fake reputation
- `plane/application/prnoise/events.go` — `EventTypeNoiseDecisionRecorded`, payload struct
- `plane/application/prnoise/service.go` — `Service` interface
- `plane/application/prnoise/postgres_service.go` — Tx-per-call source + outbox write
- `plane/application/prnoise/postgres_service_test.go` — testcontainers PG
- `plane/application/prnoise/stub_service.go` — in-memory `StubService`
- `plane/application/prnoise/stub_service_test.go`
- `plane/application/prnoise/integration_test.go` — testcontainers PG + testcontainers Qdrant
- `plane/application/prnoise/config.go` — `QualityWeights`, `CompositeWeights`, `RouterBands`, `FeatureFlags`
- `plane/application/prnoise/config_test.go` — weight-sum invariants, default-band ordering
- `plane/application/prnoise/dedup/embedder.go` — `Embedder` interface
- `plane/application/prnoise/dedup/dedup.go` — `Deduper` interface, `DuplicateHit`, `DedupCosineThreshold = 0.92` constant (ADR-016)
- `plane/application/prnoise/dedup/qdrant_client.go` — `QdrantDeduper` implementing `Deduper`
- `plane/application/prnoise/dedup/qdrant_client_test.go` — testcontainers Qdrant
- `plane/application/prnoise/dedup/dedup_test.go` — fake Qdrant client
- `plane/application/prnoise/quality/scorer.go` — `QualitySignalScorer` interface
- `plane/application/prnoise/quality/rule_based.go` — `RuleBasedQualityScorer`
- `plane/application/prnoise/quality/rule_based_test.go` — exhaustive sub-signal tests
- `plane/application/prnoise/quality/signals.go` — pure functions per sub-signal (size, coverage, ci_build, lint, agents_md, diversity, churn)
- `plane/application/prnoise/reputation/scorer.go` — `ReputationScorer` interface (ADR-017 swap surface)
- `plane/application/prnoise/reputation/rule_based.go` — `RuleBasedReputationScorer` over `identity.Service`
- `plane/application/prnoise/reputation/rule_based_test.go`
- `plane/application/prnoise/router/composite.go` — `CompositeRouter`
- `plane/application/prnoise/router/composite_test.go` — band boundaries, dedup zeroing, decision-code exhaustiveness

### Modify

- `plane/data/migrations/NNN_pr_noise_decisions.sql` — new migration: `collaboration.pr_noise_decisions` table + index; `DROP TABLE collaboration.prnoise_shadow_decisions IF EXISTS` for shadow cleanup
- `plane/data/store/postgres/compliance_test.go` — extend migrations list
- `plane/application/{pr-engine package}/handler.go` — wire `prnoise.Service.Score` into the PR-create path; remove `prnoise.shadow` flag references (file location confirmed during pre-flight by ripgrep)
- `plane/application/{webhook delivery package}/` — subscribe to `pr.noise_decision_recorded`; translate `reject` → close+comment, `auto_merge_eligible` → label, `maintainer_review` → label (consumer is idempotent on `event_id`)
- `internal/eventschemas/` (or wherever event-schema lint catalog lives) — register `pr.noise_decision_recorded` payload schema

### Untouched (out of scope)

- `plane/git/**`, `plane/workflow/**`, `plane/edge/**` (no plane crossing per ADR-019)
- Vespa wiring (ADR-016: Qdrant is the only vector surface this package touches)
- ML reputation model (CLAUDE.md open question, July 2026)
- Cross-org dedup default switch (CLAUDE.md open question, August 2026)
- GraphQL surface for decisions (issue #113, in flight)
- Reputation feedback loop (decrement on reject) — follow-up issue
- Tuning weights against the design-partner corpus — operational task, not code

---

## Pre-flight (do once before Task 1)

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
mkdir -p /home/mitta/clients/gitscale/repos/gitscale.worktrees
git worktree add -b feat/application-pr-noise-pipeline-enforcement \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-pr-noise-pipeline-enforcement \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-pr-noise-pipeline-enforcement
git status --porcelain
```

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
go test ./plane/application/identity/... -count=1
```

If anything fails, stop — baseline is broken.

- [ ] **Step P.3: Locate existing PR engine handler and webhook delivery worker**

```bash
rg -n "prnoise\.shadow|prnoise_shadow|noise.*pipeline" --type go
rg -n "pr.*engine|PRCreate|CreatePR" plane/application/ --type go
```

Record file paths in a scratch note for Task 8 wiring.

- [ ] **Step P.4: Confirm Phase 1 precision/recall gate already met**

The Phase 1→2 gate (precision ≥ 0.6, recall ≥ 0.7) is a prerequisite
to merging this PR. Confirm with the supervisor that the latest shadow
run cleared the gate before opening the PR. If not, this issue is
blocked at acceptance, not at code.

---

## Task 1 — Schema migration

- [ ] **1.1** Create `plane/data/migrations/NNN_pr_noise_decisions.sql` with the table per spec §Schema (PK on `pr_id`; CHECK constraints on score ranges; CHECK on `decision_code` enum; index on `(repo_id, decided_at DESC)`).
- [ ] **1.2** Append `DROP TABLE IF EXISTS collaboration.prnoise_shadow_decisions;` to the same migration. Document in commit body that shadow cleanup is intentional.
- [ ] **1.3** Extend `plane/data/store/postgres/compliance_test.go` migrations list.
- [ ] **1.4** Acceptance: `go test ./plane/data/store/...` green.

## Task 2 — Models, events, config

- [ ] **2.1** `models.go` — `PRInput`, `DiffStats`, `CIResult`, `Decision`, `DecisionCode` constants. `DecisionCode` is a closed string enum; add an exhaustive `Valid()` helper for the lint check.
- [ ] **2.2** `events.go` — `EventTypeNoiseDecisionRecorded = "pr.noise_decision_recorded"` and `NoiseDecisionRecordedPayload` struct. Match JSON tags exactly with spec.
- [ ] **2.3** `config.go` — `QualityWeights`, `CompositeWeights`, `RouterBands`, `FeatureFlags` (`CrossOrgDedup bool`). Provide `DefaultConfig()` returning the spec defaults.
- [ ] **2.4** `config_test.go` — `DefaultConfig().QualityWeights` sums to 1.00 within ε; `RouterBands.AutoMerge > Reject`; cross-org-dedup defaults to `false`.

## Task 3 — Quality scorer

- [ ] **3.1** `quality/signals.go` — pure functions, one per sub-signal: `SizeScore`, `TestCoverageScore`, `CIBuildScore`, `LintScore`, `AgentsMDScore`, `DiversityScore`, `ChurnScore`. Each takes the relevant slice of `PRInput` and returns `float64` in `[0, 1]`.
- [ ] **3.2** `quality/rule_based.go` — `RuleBasedQualityScorer{Weights QualityWeights}` with `Score(ctx, PRInput) float64` computing the weighted sum. Clamp final to `[0, 1]` defensively.
- [ ] **3.3** `quality/scorer.go` — `QualitySignalScorer` interface (`Score(ctx, PRInput) float64`). `RuleBasedQualityScorer` implements it.
- [ ] **3.4** `quality/rule_based_test.go` — table-driven coverage of every sub-signal at boundary, mid, and out-of-range inputs; assert weighted sum on a synthetic PRInput.

## Task 4 — Reputation scorer (interface + rule-based)

- [ ] **4.1** `reputation/scorer.go` — `ReputationScorer` interface. Document ADR-017 swap surface and the open question (CLAUDE.md, July 2026).
- [ ] **4.2** `reputation/rule_based.go` — `RuleBasedReputationScorer{Identity identity.Service}`. Empty `AgentID` → 1.0. Otherwise call `Identity.GetAgent` and return `agent.ReputationScore`. Forward `ErrAgentNotFound` unchanged.
- [ ] **4.3** `reputation/rule_based_test.go` — three cases: empty AgentID, known agent, unknown agent (ErrAgentNotFound propagated).

## Task 5 — Dedup (Qdrant client + interface)

- [ ] **5.1** `dedup/embedder.go` — `Embedder` interface (`Embed(ctx, text) ([]float32, error)`). No production impl in this PR — wired via the existing embedding service used by Phase 1.
- [ ] **5.2** `dedup/dedup.go` — `Deduper` interface, `DuplicateHit{PRID, Similarity}`, **`const DedupCosineThreshold = 0.92`** with a comment quoting ADR-016 verbatim.
- [ ] **5.3** `dedup/qdrant_client.go` — `QdrantDeduper{client *qdrant.Client, crossOrg bool}`. ANN query top-1; if `crossOrg=false`, filter by `repo_id` payload field. Return `DuplicateHit` only when similarity ≥ `DedupCosineThreshold`.
- [ ] **5.4** `dedup/dedup_test.go` — fake Qdrant client; assert threshold gate, `crossOrg` filter spec captured by recording client.
- [ ] **5.5** `dedup/qdrant_client_test.go` — testcontainers Qdrant; seed two near-duplicate vectors, query, assert hit at ≥ 0.92.
- [ ] **5.6** Acceptance: `go test ./plane/application/prnoise/dedup/... -tags integration` green.

## Task 6 — Composite router

- [ ] **6.1** `router/composite.go` — `CompositeRouter{Weights CompositeWeights, Bands RouterBands}`. `Decide(quality, reputation float64, dup *DuplicateHit) Decision` returning a partially-filled `Decision` (composite + code + reason). The pipeline fills the rest.
- [ ] **6.2** Reason strings are stable: `"duplicate"`, `"composite_below_reject"`, `"composite_in_review_band"`, `"composite_above_auto_merge"`. Tested explicitly.
- [ ] **6.3** `router/composite_test.go` — band boundaries (just below / at / just above each band), dedup zeroing (`DedupScore=1` always rejects), decision-code enum exhaustiveness (every code path is reachable from at least one input).

## Task 7 — Pipeline + service

- [ ] **7.1** `pipeline.go` — `Pipeline{Embedder, Deduper, Quality, Reputation, Router, Config}` with `Score(ctx, PRInput) (Decision, error)` running stages in order. Stage failures bubble up as wrapped errors (`%w`); no partial decisions are returned.
- [ ] **7.2** `pipeline_test.go` — fake embedder + fake deduper + fake reputation. Assert determinism (same input → same Decision, byte-for-byte JSON).
- [ ] **7.3** `service.go` — `Service` interface (`RecordDecision(ctx, PRInput) (Decision, error)`). Conceptually: run pipeline, write source row + outbox row in one Tx.
- [ ] **7.4** `postgres_service.go` — `PostgresService{Pool *pgxpool.Pool, Pipeline *Pipeline}`. `RecordDecision`:
  - Run pipeline (read-only; no Tx yet).
  - `BeginTx` → `INSERT … ON CONFLICT (pr_id) DO UPDATE …` returning the row id; insert outbox row in same Tx (ADR-008); `Commit`.
  - On 40001 serializable retry, retry up to 3× with backoff.
- [ ] **7.5** `postgres_service_test.go` — testcontainers PG. Cases: first call inserts source + outbox; re-score upserts source + emits new outbox row; concurrent goroutines on same PR — exactly one source row, exactly one extra outbox row per re-score.
- [ ] **7.6** `stub_service.go` + `stub_service_test.go` — in-memory map keyed by `pr_id`; mirror `identity.StubService` style.

## Task 8 — Wire into PR engine + webhook consumer

- [ ] **8.1** Locate the PR-create handler (recorded in P.3). Inject `prnoise.Service`. On PR creation, after persistence, call `Service.RecordDecision`. Failures are logged + metric-incremented but **do not** fail the PR-create call (decision recording is async-style; the handler keeps moving).
- [ ] **8.2** Remove `prnoise.shadow` flag references and any shadow-only branches in the handler. Delete the shadow-mode logging path.
- [ ] **8.3** In the webhook delivery worker, add a consumer for `pr.noise_decision_recorded`:
  - `reject` → close PR + comment with `Reason`.
  - `auto_merge_eligible` → label `pr_ready_for_merge`.
  - `maintainer_review` → label `needs_maintainer_review`.
  - Idempotency keyed on `event_id` (existing pattern).
- [ ] **8.4** Tests for both: handler stub-injects `Service`; webhook consumer table-driven over `DecisionCode`.

## Task 9 — Integration test

- [ ] **9.1** `integration_test.go` — testcontainers PG + testcontainers Qdrant.
- [ ] **9.2** Cases:
  - Seed two near-duplicate PRs in Qdrant, then call `RecordDecision` on a new PR with the same embedding → `reject` with `Reason="duplicate"`; `duplicate_of` set.
  - Score a clean PR (high quality + reputation) → `auto_merge_eligible`.
  - Score a borderline PR → `maintainer_review`.
  - Re-score an existing PR → upsert + new outbox row.
  - Cross-org-dedup OFF → matches in another org are not surfaced (recording client asserts the `repo_id` filter).
- [ ] **9.3** Build tag `integration`; match `plane/application/identity/integration_test.go` boilerplate exactly.

## Task 10 — Event-schema registration

- [ ] **10.1** Register `pr.noise_decision_recorded` with the event-schema lint catalog in the conventional location (located via `make lint-events` source).
- [ ] **10.2** Run `make lint-events` — green.

## Task 11 — ADR + plane-boundary checks

- [ ] **11.1** Run `gitscale-adr-guard` — verify no contradiction with ADR-008, ADR-016 (threshold 0.92 quoted; Vespa untouched), ADR-017 (interfaces preserved), ADR-019 (no plane crossing), ADR-021 (Qdrant scoped to dedup only).
- [ ] **11.2** Run `gitscale-plane-boundary` — verify no `plane/git`, `plane/workflow`, `plane/edge` imports in `plane/application/prnoise/**`.
- [ ] **11.3** Run `gitscale-go-conventions`.
- [ ] **11.4** Run `gitscale-outbox-check` — verify `RecordDecision` writes source + outbox in one Tx; no Kafka publish from the package.
- [ ] **11.5** Run `gitscale-event-schema` — verify `pr.noise_decision_recorded` payload registered and stable.

## Task 12 — Self-review battery + PR

- [ ] **12.1** Open follow-up issue: "Reputation feedback loop — decrement `AgentIdentity.ReputationScore` on `pr.noise_decision_recorded` with `decision_code=reject`."
- [ ] **12.2** Run pre-push gates:
  ```bash
  go build ./...
  go vet ./...
  golangci-lint run ./...
  go test -race ./plane/application/prnoise/... ./plane/data/store/...
  go test -tags integration ./plane/application/prnoise/...
  make lint-events
  make lint-determinism
  ```
- [ ] **12.3** Dispatch self-review battery in parallel: `pr-review-toolkit:code-reviewer`, `pr-review-toolkit:silent-failure-hunter`, `adr-historian` (verify ADR-016 threshold quoted, ADR-021 cited), `pr-review-toolkit:type-design-analyzer` (interfaces in `quality/`, `reputation/`, `dedup/`), `pr-review-toolkit:pr-test-analyzer`. Resolve findings.
- [ ] **12.4** Commit using Conventional Commits: `feat(application): PR noise pipeline enforcement — semantic dedup + quality + composite routing (#116)`.
- [ ] **12.5** `gh pr create` with title `[Application] PR noise pipeline enforcement — semantic dedup + quality signals + composite routing`, body containing all sections from plan §pr-quality-bar, `Closes #116`, follow-up issue cross-link, ADR refs (008/016/017/019/021), self-review block, co-author trailer. Include the precision/recall acceptance run output as a code block.

---

## Acceptance criteria (mirror issue body)

- [ ] Qdrant cosine query at 0.92 threshold correctly identifies duplicate PRs (integration test).
- [ ] Quality signal scorer produces a score in `[0, 1]` from test coverage + lint + build + the four other signals (unit tests).
- [ ] Composite router routes to `auto_merge_eligible` / `maintainer_review` / `reject` per band (unit + integration).
- [ ] Enforcement mode: rejected PRs are closed by the webhook consumer; held PRs are not labelled for human review (consumer test).
- [ ] Precision ≥ 0.6, recall ≥ 0.7 on design-partner corpus — documented run captured in PR description.
- [ ] Phase 1 shadow code removed (table dropped, `prnoise.shadow` flag references gone, shadow logging path deleted).
- [ ] PR closes #116, references ADR-008 / ADR-016 / ADR-019 / ADR-021, and links the reputation-feedback follow-up issue.
- [ ] `make test`, `make lint-events`, `make lint-determinism`, integration tests, and the pre-push gates list all green.
- [ ] Self-review battery clean.
