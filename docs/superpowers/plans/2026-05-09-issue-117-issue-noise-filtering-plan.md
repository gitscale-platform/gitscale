# Issue #117 Issue noise filtering — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up `plane/application/issuenoise` with an `IssueScorer`
interface, ship a rule-based scorer, route incoming issues into
`spam → drop`, `low_quality → maintainer queue`, `duplicate → held`, or
`normal → standard queue`, with held-issue TTL via Temporal and a full
audit trail through the outbox.

**Architecture:** rule-based `IssueScorer` (ML deferred per the same
open arch question as #116), pure `Decide(Score, Thresholds) Verdict`,
single-Tx Router that writes issue + decision + outbox, post-commit
Temporal workflow for hold TTL. Vespa for duplicate detection (ADR-016);
Qdrant explicitly out.

**Tech Stack:** Go 1.22, pgx/v5, Temporal Go SDK, Vespa client wrapper
(data plane), testcontainers-go, google/uuid.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-117-issue-noise-filtering-design.md`

**Branch:** `feat/application-issue-noise-filtering` (worktree:
`../gitscale.worktrees/feat-application-issue-noise-filtering`)

**ADR-impact:** conforming (ADR-008, ADR-016, ADR-017, ADR-019). No new ADR.

---

## File map

### Create

- `plane/application/issuenoise/doc.go` — package doc, ADR refs, plane boundary statement
- `plane/application/issuenoise/scorer.go` — `IssueScorer`, `Score`, `Signal`, `IssueDraft`
- `plane/application/issuenoise/rule_scorer.go` — `RuleScorer`, registry composition
- `plane/application/issuenoise/rule_scorer_test.go`
- `plane/application/issuenoise/rules/link_density.go` (+ `_test.go`)
- `plane/application/issuenoise/rules/length.go` (+ `_test.go`)
- `plane/application/issuenoise/rules/language.go` (+ `_test.go`)
- `plane/application/issuenoise/rules/reporter_rate.go` (+ `_test.go`)
- `plane/application/issuenoise/rules/reputation.go` (+ `_test.go`)
- `plane/application/issuenoise/rules/agentsmd_violations.go` (+ `_test.go`)
- `plane/application/issuenoise/rules/duplicate.go` (Vespa wrapper) (+ `_test.go`)
- `plane/application/issuenoise/decide.go` — `Verdict`, `Thresholds`, `Decide`
- `plane/application/issuenoise/decide_test.go`
- `plane/application/issuenoise/router.go` — `Router.Route`, `Router.Release`
- `plane/application/issuenoise/router_test.go`
- `plane/application/issuenoise/events.go` — event_type constants, payload structs
- `plane/application/issuenoise/store.go` — `DecisionStore`, `ThresholdsProvider` interfaces
- `plane/application/issuenoise/integration_test.go`
- `plane/data/store/postgres/metadata_issue_noise.go`
- `plane/data/store/postgres/migrations/008_issue_noise.sql`
- `plane/data/store/stub/metadata_issue_noise.go`
- `plane/workflow/issuehold/workflow.go`
- `plane/workflow/issuehold/workflow_test.go`
- `plane/workflow/issuehold/activities.go`

### Modify

- `plane/data/store/metadata.go` — add `IssueWriter.UpdateState(ctx, id, state)` if missing; expose `IssueReader.Get`
- `plane/application/restapi/issues_handlers.go` (assumes #111 follow-up; if absent, add minimal handlers gated by spec note) — call `Router.Route` instead of direct insert; add `POST /v1/issues/{id}/release`
- `plane/application/mcp/tools_issues.go` (#112) — call `Router.Route` from the `issue_create` tool path
- `cmd/rest-api/main.go` — wire `issuenoise.Router` deps
- `cmd/workflow-worker/main.go` — register `IssueHoldExpiryWorkflow` + activities
- `docs/event-schema/registry.yaml` (or wherever `make lint-events` reads) — add `issue_noise.routing_decided` and `issue_noise.released`
- `plane/data/store/postgres/compliance_test.go` — extend migration list

### Untouched (out of scope)

- PR noise filter (#116)
- ML scorer (deferred July 2026)
- Customer-facing similarity API
- Cross-repo dedup
- Reputation-score computation loop

---

## Pre-flight (do once before Task 1)

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
mkdir -p /home/mitta/clients/gitscale/repos/gitscale.worktrees
git worktree add -b feat/application-issue-noise-filtering \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-issue-noise-filtering \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-issue-noise-filtering
git status --porcelain
```

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
go test ./plane/application/identity/... -count=1
```

- [ ] **Step P.3: Confirm #111 + #112 deps merged on main; check #113 (GraphQL) status — if merged, plan §Task 7 covers GraphQL wiring; if not, defer to a follow-up issue.

---

## Task 1 — Migration `008_issue_noise.sql`

- [ ] **1.1** Create the SQL file per spec (decisions table, config table, enum values).
- [ ] **1.2** Add to migrations runner list.
- [ ] **1.3** Extend `compliance_test.go` to include `008_issue_noise.sql`.
- [ ] **1.4** Confirm `repositories.issue_state` is an enum (not free-form text); if free-form, switch to a CHECK constraint update instead of `ALTER TYPE`.
- [ ] **1.5** Acceptance: `go test ./plane/data/store/postgres/...` green; testcontainers runs the migration cleanly.

## Task 2 — Store interfaces + impls

- [ ] **2.1** `plane/application/issuenoise/store.go`:
  ```go
  type DecisionStore interface {
      Insert(ctx context.Context, tx pgx.Tx, d Decision) error
      ListByIssue(ctx context.Context, issueID uuid.UUID) ([]Decision, error)
  }
  type ThresholdsProvider interface {
      Get(ctx context.Context, repoID uuid.UUID) (Thresholds, error)
  }
  ```
- [ ] **2.2** Postgres impl: `Insert` takes `pgx.Tx` to enforce same-Tx guarantee. Default thresholds applied if no row exists.
- [ ] **2.3** Stub impl: in-memory map.
- [ ] **2.4** Tests on both impls; pagination not required (decisions per issue is bounded).

## Task 3 — Scorer scaffolding + rules

- [ ] **3.1** `scorer.go`: types per spec. `IssueScorer` interface — small, no leaking pgx types.
- [ ] **3.2** `rule_scorer.go`: registry of rules; each rule is `func(ctx, IssueDraft, deps) (Signal, category)` where category ∈ {spam, low_quality, duplicate}. `RuleScorer.Score` walks the registry, sums weights per category, clamps to [0,1].
- [ ] **3.3** Each rule under `rules/` is independently testable (pure or mocked deps).
  - `link_density`: count URLs / chars; ≥0.10 contributes spam 0.3.
  - `length`: < 30 chars or > 50KB → low_quality 0.4.
  - `language`: non-ASCII-letter ratio + simple lang detect; non-target-lang → low_quality 0.2 (configurable allowlist).
  - `reporter_rate`: Redis counter `(reporter_id, hour_bucket)` ≥ 20/h → spam 0.5.
  - `reputation`: < 0.3 → low_quality 0.25; < 0.1 → spam 0.5.
  - `agentsmd_violations`: ≥3 in 24h → spam 0.4.
  - `duplicate`: Vespa top-1 normalized score; if ≥ threshold, sets `Score.Duplicate` and `DuplicateOf`.
- [ ] **3.4** Per-rule unit tests.
- [ ] **3.5** `rule_scorer_test.go` — table-driven scenarios producing expected category sums.

## Task 4 — Decide + Router

- [ ] **4.1** `decide.go`: precedence per spec; pure function; exhaustive test.
- [ ] **4.2** `events.go`: `EventTypeRoutingDecided = "issue_noise.routing_decided"`, `EventTypeReleased = "issue_noise.released"`. Payload structs with stable JSON tags.
- [ ] **4.3** `router.go`:
  ```go
  func (r *Router) Route(ctx context.Context, d IssueDraft) (Verdict, error)
  func (r *Router) Release(ctx context.Context, issueID, maintainerID uuid.UUID) error
  ```
  Single Tx pattern: BeginTx → write issues row (with new state) → write decision → write outbox → Commit. Post-commit: start (Route) or signal (Release) Temporal workflow.
- [ ] **4.4** `router_test.go` with stub store + stub Temporal client; assert all-or-nothing on Tx; assert post-commit workflow start is idempotent (called twice, one workflow).
- [ ] **4.5** Add metric `issue_noise_route_total{verdict=...}` and `issue_noise_route_duration_seconds`. **No silent failure**: any error from scorer increments `issue_noise_scorer_errors_total` and falls back to `VerdictNormal` (fail-open) — documented in spec follow-up; alert rule ships in observability config.

## Task 5 — Temporal hold-expiry workflow

- [ ] **5.1** `plane/workflow/issuehold/workflow.go`:
  ```go
  func IssueHoldExpiryWorkflow(ctx workflow.Context, p Params) error
  ```
  Sleeps `p.HoldTTL`. On wake, calls activity `AutoCloseIfStillHeld(issueID)`. On `release` signal, returns clean.
- [ ] **5.2** `activities.go`: `AutoCloseIfStillHeld` calls into app plane via gRPC (per ADR-019; no direct DB from workflow plane), updating issue state and writing outbox.
- [ ] **5.3** `workflow_test.go`: Temporal testsuite, virtual time, asserts auto-close on expiry, no-op on prior release.
- [ ] **5.4** Workflow ID: `issue-hold-{issue_id}`. Idempotent on start.
- [ ] **5.5** Reconciler: a workflow-plane scheduled task (every 5 min) lists held issues without a running workflow and starts one. Out of scope for this PR if the existing reconciler pattern (e.g. archival #69) covers — extend it; otherwise add `cmd/workflow-worker` schedule.

## Task 6 — Wire submission paths

- [ ] **6.1** REST: in the issue-create handler, replace direct `IssueWriter.Insert` with `Router.Route(ctx, draft)`. Surface verdict in response (so callers see `held` immediately).
- [ ] **6.2** MCP (#112): same swap in `tools_issues.go` `issue_create` tool. Tool response includes verdict.
- [ ] **6.3** GraphQL (#113): if merged, swap resolver. Otherwise add follow-up issue.
- [ ] **6.4** Add `POST /v1/issues/{id}/release` handler calling `Router.Release`. Authorization: caller must be maintainer of the repo (existing identity scope `repo:maintainer`).
- [ ] **6.5** Tests for each submission path: spam → 200 with `verdict=spam`, no row visible in standard list; held → visible in maintainer list only; normal → both lists.

## Task 7 — Event schema + outbox lint

- [ ] **7.1** Register `issue_noise.routing_decided` + `issue_noise.released` in the event registry that `make lint-events` consumes.
- [ ] **7.2** Add JSON-Schema files under `docs/event-schema/issue_noise/` if that's the project convention; otherwise inline in registry.
- [ ] **7.3** Run `make lint-events`; expect green.

## Task 8 — Integration test

- [ ] **8.1** `integration_test.go` with testcontainers Postgres + a fake Vespa search returning fixed candidates + a Redis container for reporter-rate.
- [ ] **8.2** Cases:
  - Normal issue → `verdict=normal`, issue visible, outbox row present.
  - Spam issue (high link density + low reputation) → `verdict=spam`, issue auto-closed, outbox row present.
  - Held issue (low quality) → `verdict=low_quality`, issue state=held, hold workflow started.
  - Duplicate (Vespa returns 0.93) → `verdict=duplicate`, issue state=held, decision links parent.
  - Release → state moves to normal, second decision row, release outbox event.
  - Tx failure mid-route (forced via injected error) → no issue row, no decision row, no outbox row.
- [ ] **8.3** Build tag `integration` matching project convention.

## Task 9 — Dark-launch + threshold tuning gate

- [ ] **9.1** Add config flag `issue_noise.enforce` (default **false** for the first 14 days post-merge). When false, `Router.Route` computes verdict, writes decision row + outbox, but always inserts the issue with `state=normal`.
- [ ] **9.2** Document the dark-launch protocol in spec risks; add a metrics dashboard query for verdict distribution.
- [ ] **9.3** Follow-up issue: "Flip `issue_noise.enforce=true` after threshold review."

## Task 10 — ADR + plane-boundary checks

- [ ] **10.1** Run `gitscale-adr-guard` — verify no contradiction with ADR-016 (Vespa for search; Qdrant excluded), ADR-008 (outbox), ADR-019 (no workflow-plane DB access; activities call back via gRPC).
- [ ] **10.2** Run `gitscale-plane-boundary` — verify no `plane/git` imports; verify `plane/workflow/issuehold` does not import `plane/data/store` directly.
- [ ] **10.3** Run `gitscale-event-schema` — both new event types registered.
- [ ] **10.4** Run `gitscale-outbox-check` — `Router.Route` and `Router.Release` write source row + outbox row in the same Tx.
- [ ] **10.5** Run `gitscale-go-conventions`.

## Task 11 — Self-review battery + PR

- [ ] **11.1** Pre-push gates:
  ```bash
  go build ./...
  go vet ./...
  golangci-lint run ./...
  go test -race ./plane/application/issuenoise/... \
      ./plane/workflow/issuehold/... \
      ./plane/data/store/postgres/... \
      ./plane/data/store/stub/...
  make lint-events
  make lint-determinism
  ```
- [ ] **11.2** Dispatch self-review battery in parallel: `pr-review-toolkit:code-reviewer`, `pr-review-toolkit:silent-failure-hunter` (especially the fail-open path in §4.5), `adr-historian`, `pr-review-toolkit:type-design-analyzer` (Score / Verdict public types), `pr-review-toolkit:pr-test-analyzer`. Resolve findings.
- [ ] **11.3** Commit using Conventional Commits: `feat(application): issue noise filtering — rule-based scorer + maintainer queue routing (#117)`. Identity flags: `-c user.email=neeraj.mittal@gridverse.in -c user.name="Neeraj Mittal"`.
- [ ] **11.4** `gh pr create` with title `[Application] Issue noise filtering — spam detection + maintainer queue routing`, body containing summary + acceptance-criteria checklist + dark-launch note + self-review block + `Closes #117`.

---

## Acceptance criteria (mirror issue body)

- [ ] Spam classifier holds issues from agents below the reputation floor (rule + test).
- [ ] Duplicate detector flags issues with similarity > threshold against open issues in the same repo via Vespa.
- [ ] Maintainer queue lists only `held` issues; standard queue lists `normal` only.
- [ ] Held issues auto-close after configured TTL (Temporal workflow + integration test).
- [ ] All routing decisions appear in `outbox` with required audit fields and a `decisions` row.
- [ ] `make test` and pre-push gates green.
- [ ] Self-review battery clean.
- [ ] PR closes #117 and links the dark-launch flip-the-flag follow-up + (if needed) GraphQL wiring follow-up.
