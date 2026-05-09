# Spec — Issue #117 Issue noise filtering (spam detection + maintainer queue routing)

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/117
Plane: application
Priority: p2
ADR-impact: conforming (ADR-008 outbox; ADR-016 Vespa-for-search; ADR-017 swap surfaces; ADR-019 plane boundary)

## Problem

Phase 2 GA assumes agents will submit issues at rates that swamp human
maintainer review queues. The platform currently has no filter between
"agent calls `POST /v1/issues`" and "maintainer sees it." Without
classification, a single misbehaving agent fleet can drown every repo's
issue tracker in seconds. Per design_document.md §3.4.2–§3.4.4 this is a
ship-blocker for Phase 2.

The PR engine has analogous noise-filter machinery (#116, in flight) —
its rule shapes (link density, length, reporter-rate) are the same
shapes we need here. We will reuse the rule vocabulary; we will not
share runtime objects across PR / issue domains.

## Goals

1. Add a `plane/application/issuenoise` package owning issue
   classification, routing, and the maintainer-queue lifecycle.
2. Define an `IssueScorer` interface so the rule-based scorer shipping
   today can be swapped for an ML scorer in July 2026 without touching
   call sites (parallel to #116; same open-question deferral).
3. Ship a rule-based `IssueScorer` covering: link density, body length
   floor / ceiling, language detection, repeated-reporter rate, agent
   reputation floor, AGENTS.md violation count, and duplicate-similarity
   (Vespa semantic search, threshold configurable).
4. Define a `Router` that maps `(Score, Verdict) → Action`:
   - `spam` → drop with auto-close + canned explanation (audit row).
   - `low_quality` → maintainer review queue (held).
   - `normal` → standard issue queue (visible).
   - `duplicate` → held with link to candidate parent.
5. Held-issue TTL: configurable per-repo, default 14 days. Expired held
   issues auto-close with `expired_in_review_queue` reason.
6. Manual release path: maintainer calls `POST /v1/issues/{id}/release`
   to move a held issue to the standard queue.
7. Every routing decision writes an `issue_noise.routing_decided` outbox
   row in the same Tx as the issue insert/update (ADR-008).
8. Integration tests use testcontainers Postgres + a Vespa stub; no
   mock-DB tests in the new package.

## Non-goals

- ML-based scorer — deferred to July 2026 (open arch question; #116
  shares the resolution).
- PR noise filter — issue #116 (parallel work, do not import its
  package).
- New customer-facing similarity API — Vespa semantic-search is called
  internally by the duplicate detector only.
- Cross-repo duplicate detection — scoped to the same repo per issue
  body.
- Maintainer-queue UI — surfaced by REST/GraphQL/MCP via existing
  list/get endpoints filtered by status; no new UI in this PR.
- Reputation-score computation — already owned by identity (existing
  agent reputation field); this PR only reads it.
- Rewriting issue submission paths in #111 (REST), #112 (MCP), #113
  (GraphQL) — they call `issuenoise.Router.Route` as a hook on insert.

## Design decisions (defaults selected by supervisor)

| Question | Choice | Rationale |
|---|---|---|
| Scorer abstraction | `IssueScorer` interface returning `Score{Spam, LowQuality, Duplicate float64; Signals []Signal}`. Concrete `RuleScorer`. ML scorer slot is empty. | Mirrors #116 PR-noise abstraction; lets July-2026 ML decision land without API breakage. ADR-017 swap-surface pattern. |
| Verdict mapping | Pure function `Decide(Score, Thresholds) Verdict` separate from `Scorer`. | Thresholds are per-repo policy, not per-scorer state. Lets ops tune without scorer redeploy. |
| Duplicate search | **Vespa** semantic search over `issues` content field, filtered to `repo_id = X AND state = open`, top-3, cosine-equivalent score normalized to [0,1]. Threshold default 0.85. | ADR-016: Vespa is the customer-facing search. Qdrant is **explicitly excluded** — it is for PR dedup only. |
| Storage | New `repositories.issue_noise_decisions` table — append-only audit, FK to `issues.id`. Issue `state` column gains values `held` and `auto_closed_spam` (migration). | Audit trail required by acceptance criteria; status enum extension keeps the issue lifecycle in one place. |
| Outbox topic | `issue_noise.routing_decided` (event_type), payload includes `issue_id`, `repo_id`, `verdict`, `signals[]`, `scorer_version`, `decided_at`. | Lets webhook delivery + analytics consume routing decisions idempotently on `event_id` (ADR-008). |
| TTL enforcement | Temporal workflow `IssueHoldExpiryWorkflow` started on `held` insertion, sleeps until TTL, then auto-closes if still held. | Matches plan-approval / archival pattern (ADR-019); no in-process timers in app plane. |
| Threshold config | Per-repo row in `repositories.issue_noise_config` (spam_floor, low_quality_floor, duplicate_floor, hold_ttl). Defaults from env. | Customers tune thresholds without redeploy; defaults safe for cold-start. |
| Reputation floor | `agent_reputation < 0.3 → low_quality`. `< 0.1 → spam`. Read from identity.LookupIdentity for the issue reporter. | Conservative; matches PR-noise (#116) sibling defaults. |
| AGENTS.md violations | Count taken from agentsmd plane (existing `agentsmd.ViolationCount(ctx, agentID, repoID, since)`); ≥3 in 24h adds 0.4 to spam score. | Reuses #114 enforcement signal; no new plane interaction. |

## Architecture

### Package layout

```
plane/application/issuenoise/
  doc.go                      package doc, ADR refs, plane boundary
  scorer.go                   IssueScorer interface, Score, Signal
  rule_scorer.go              RuleScorer impl + rule registry
  rule_scorer_test.go
  rules/
    link_density.go
    length.go
    language.go
    reporter_rate.go
    reputation.go
    agentsmd_violations.go
    duplicate.go              Vespa client wrapper
    *_test.go
  decide.go                   Verdict, Thresholds, Decide()
  decide_test.go
  router.go                   Router.Route(ctx, IssueDraft) (Verdict, error)
  router_test.go
  events.go                   event_type constants, payload structs
  store.go                    DecisionStore interface (writer)
  hold_workflow.go            Temporal workflow signature (definition lives in plane/workflow)
  integration_test.go         testcontainers PG + Vespa stub
plane/data/store/postgres/
  metadata_issue_noise.go     DecisionStore impl + thresholds reader
  migrations/008_issue_noise.sql
plane/data/store/stub/
  metadata_issue_noise.go     in-memory impl
plane/workflow/issuehold/
  workflow.go                 IssueHoldExpiryWorkflow (sleeps TTL, signals release)
  workflow_test.go
```

### Scorer interface

```go
type Signal struct {
    Name   string  // "link_density", "duplicate_vespa", ...
    Weight float64 // contribution to category
    Detail string  // human-readable for audit
}

type Score struct {
    Spam       float64 // [0,1]
    LowQuality float64 // [0,1]
    Duplicate  float64 // [0,1]; if >0, DuplicateOf is set
    DuplicateOf *uuid.UUID
    Signals    []Signal
    ScorerVersion string // "rule-v1"
}

type IssueDraft struct {
    ID         uuid.UUID
    RepoID     uuid.UUID
    ReporterID uuid.UUID
    Title, Body string
    CreatedAt  time.Time
}

type IssueScorer interface {
    Score(ctx context.Context, d IssueDraft) (Score, error)
}
```

### Verdict + decision

```go
type Verdict int
const (
    VerdictUnknown Verdict = iota
    VerdictNormal
    VerdictLowQuality
    VerdictDuplicate
    VerdictSpam
)

type Thresholds struct {
    SpamFloor       float64 // default 0.7
    LowQualityFloor float64 // default 0.4
    DuplicateFloor  float64 // default 0.85
    HoldTTL         time.Duration // default 14 * 24h
}

func Decide(s Score, t Thresholds) Verdict
```

Precedence: spam → duplicate → low_quality → normal. Spam wins because
spam is "drop"; duplicate wins over low-quality because the parent issue
already exists and merging signal is more useful than holding twice.

### Router

```go
type Router struct {
    Scorer    IssueScorer
    Store     DecisionStore
    Issues    store.IssueWriter         // existing data-plane interface
    Thresh    ThresholdsProvider        // per-repo lookup
    Workflows WorkflowStarter           // Temporal client wrapper (plane/workflow client)
    Clock     func() time.Time
}

func (r *Router) Route(ctx context.Context, d IssueDraft) (Verdict, error)
```

`Route` runs in **one Tx**:
1. Score the draft (read-only, no Tx).
2. `BeginTx`.
3. Insert / update `issues` row with `state = normal | held | auto_closed_spam`.
4. Insert `issue_noise_decisions` audit row.
5. Insert `outbox(event_type='issue_noise.routing_decided', payload=...)`.
6. `Commit`.
7. **After commit**, if held, signal Temporal `IssueHoldExpiryWorkflow`
   (start is idempotent on `issue_id`; failure here is logged + retried
   by an outbox-driven reconciler — never blocks ack).

ADR-008: caller is acked on commit, not on workflow start. Temporal
start is a **post-commit** side effect, but is durable because the
workflow ID is `issue-hold-{issue_id}` (idempotent) and a reconciler
sweeps `held` issues missing a running workflow every 5 min.

### Outbox event

```json
{
  "event_id": "01JKW...",
  "event_type": "issue_noise.routing_decided",
  "payload": {
    "issue_id": "...",
    "repo_id": "...",
    "reporter_id": "...",
    "verdict": "low_quality",
    "scorer_version": "rule-v1",
    "score": {"spam": 0.12, "low_quality": 0.55, "duplicate": 0.0},
    "signals": [
      {"name": "link_density", "weight": 0.3, "detail": "8 links / 412 chars"},
      {"name": "reputation",   "weight": 0.25, "detail": "agent_reputation=0.28"}
    ],
    "duplicate_of": null,
    "decided_at": "2026-05-09T..."
  }
}
```

Schema check: `make lint-events` must include this event_type in the
registry.

### Vespa duplicate-detection wrapper

```go
type vespaIssueSearcher struct{ c vespa.Client }

func (v *vespaIssueSearcher) FindCandidates(ctx context.Context,
    repoID uuid.UUID, body string, k int) ([]Candidate, error)
```

Calls Vespa's `issues` content cluster with a YQL similarity query,
filtered by `repo_id` and `state = "open"`. Returns top-k. The
`duplicate.go` rule normalizes the top result's score to [0,1]; threshold
configurable.

ADR-016 conformance: this is **Vespa**, not Qdrant. The Qdrant client is
not imported in this package.

### Migration `008_issue_noise.sql`

```sql
CREATE TABLE repositories.issue_noise_decisions (
    decision_id    UUID PRIMARY KEY,
    issue_id       UUID NOT NULL REFERENCES repositories.issues(id),
    repo_id        UUID NOT NULL,
    reporter_id    UUID NOT NULL,
    verdict        TEXT NOT NULL CHECK (verdict IN ('normal','low_quality','duplicate','spam')),
    scorer_version TEXT NOT NULL,
    score_spam     NUMERIC(4,3) NOT NULL,
    score_lq       NUMERIC(4,3) NOT NULL,
    score_dup      NUMERIC(4,3) NOT NULL,
    duplicate_of   UUID,
    signals        JSONB NOT NULL,
    decided_at     TIMESTAMPTZ NOT NULL,
    decided_by     TEXT NOT NULL  -- 'auto' | 'maintainer:<id>'
);

CREATE INDEX issue_noise_decisions_issue_idx
    ON repositories.issue_noise_decisions(issue_id, decided_at DESC);

CREATE TABLE repositories.issue_noise_config (
    repo_id           UUID PRIMARY KEY,
    spam_floor        NUMERIC(4,3) NOT NULL DEFAULT 0.700,
    low_quality_floor NUMERIC(4,3) NOT NULL DEFAULT 0.400,
    duplicate_floor   NUMERIC(4,3) NOT NULL DEFAULT 0.850,
    hold_ttl_seconds  INT NOT NULL DEFAULT 1209600  -- 14d
);

ALTER TYPE repositories.issue_state ADD VALUE IF NOT EXISTS 'held';
ALTER TYPE repositories.issue_state ADD VALUE IF NOT EXISTS 'auto_closed_spam';
```

## Plane boundary (ADR-019)

`plane/application/issuenoise` calls only:

- `plane/data/store` interfaces (DecisionStore, IssueWriter, ThresholdsProvider).
- `plane/data/cache` for reporter-rate counters.
- `plane/application/identity` Service (in-process) for reputation read.
- `plane/application/agentsmd` Service (in-process) for violation count.
- A Vespa client wrapper (data-plane interface; Vespa runtime lives outside the app plane).
- A Temporal client wrapper to start `IssueHoldExpiryWorkflow` (workflow-plane SDK; ADR-019 boundary).

Forbidden: direct `plane/git` or `plane/workflow` runtime imports;
direct Kafka publish; direct Qdrant client; in-process timers for hold
TTL.

## Outbox conformance (ADR-008)

Every state change writes:
- one `issues` row (insert or status update),
- one `issue_noise_decisions` row,
- one `outbox` row,

in a **single** PG transaction. Caller is acked on commit. Temporal
workflow start is post-commit, idempotent, reconciled.

## Testing strategy

1. **Unit tests** per rule with table-driven inputs covering low/high/edge
   cases. Each rule is a pure function over an `IssueDraft`.
2. **Scorer test** asserts rule registry totals match expected
   `Score{spam, low_quality, duplicate}` for fixed inputs.
3. **Decide test** matrix: every threshold boundary, precedence
   (spam beats duplicate beats low-quality).
4. **Router integration** with testcontainers Postgres: insert →
   `Route` → assert issue state, decision row, outbox row all present
   on commit; failure mid-Tx leaves none.
5. **Duplicate detector** with a Vespa stub returning fixed candidates;
   threshold round-trip.
6. **Hold-expiry workflow** test under `plane/workflow/issuehold/`
   using Temporal testsuite — sleeps replaced with virtual time, asserts
   auto-close on expiry, no-op on prior release.
7. **Release path** — `Router.Release(ctx, issueID, maintainerID)` flips
   state, writes a second decision row with `decided_by=maintainer:<id>`,
   writes outbox `issue_noise.released`. Test asserts workflow is
   signaled to abort.
8. **Reconciler test** — held issues with no running workflow get one
   started (idempotent).

## Risks / unknowns

- **Vespa schema for issues** is owned by data-plane infra; if the
  `issues` content cluster does not yet expose the body field with
  semantic embedding, this PR adds a follow-up issue and gates duplicate
  detection behind a feature flag (`issue_noise.duplicate_detection`)
  defaulting **off** until the field lands.
- **Reputation source-of-truth**: identity-plane reputation field exists
  but is currently constant (no scoring loop yet). Today's scorer reads
  the field; if it is still default for all agents, the reputation rule
  contributes 0. Acceptable for v1.
- **Threshold defaults** are guesses. Plan includes a 2-week dark-launch
  where verdicts are computed and recorded but **not enforced** —
  enforcement gate flips on after threshold tuning. Tracked in plan.
- **AGENTS.md violation lookup** is per-issue I/O; cache the count under
  `(agent_id, repo_id, hour_bucket)` in Redis to keep p99 acceptable.
