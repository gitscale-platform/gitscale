# Issue #113 GraphQL API — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a field-stable GitHub-compat GraphQL surface mounted at `/graphql` and `/graphql/persisted/{hash}`, with pre-execution cost analysis (depth + complexity + agent-class multiplier), persisted-query path with cost discount, follower-read default, and SLO-backed GA gating. Mirrors `plane/application/restapi/` patterns.

**Architecture:** `graph-gophers/graphql-go` schema-first runtime, single `schema.graphql` SDL source of truth, custom AST-walking cost analyzer, two-pool follower-read dispatch (`Primary` + `Reader` `MetadataStore` instances per ADR-017), persisted-query store backed by Postgres + `CacheStore` read-through, `ratelimit.RateLimiter` cost-as-tokens metering against `surface="graphql"`.

**Tech Stack:** Go 1.22, `github.com/graph-gophers/graphql-go`, stdlib `net/http`, pgx/v5, testcontainers-go, google/uuid.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-113-graphql-api-design.md`

**Branch:** `feat/application-graphql-api` (worktree: `../gitscale.worktrees/feat-application-graphql-api`)

**ADR-impact:** conforming (ADR-017 swap surface, ADR-008 outbox by composition, ADR-019 plane boundary, ADR-010 SVID re-verify on admin mutations). No new ADR.

---

## File map

### Create

- `plane/application/graphql/doc.go` — package doc, ADR-017 quote, plane boundary statement
- `plane/application/graphql/schema/schema.graphql` — SDL, single source of truth
- `plane/application/graphql/schema/schema_test.go` — SDL parse + smoke
- `plane/application/graphql/schema/github_subset_snapshot.graphql` — vendored named-subset snapshot from GitHub for diff
- `plane/application/graphql/schema/compat_test.go` — diff named subset vs GitHub snapshot
- `plane/application/graphql/cost/analyzer.go` — `Analyzer`, `Cost`, `Analyze(...)`
- `plane/application/graphql/cost/analyzer_test.go` — table-driven
- `plane/application/graphql/cost/directives.go` — `@cost`, `@liveRead` directive helpers
- `plane/application/graphql/persisted/store.go` — `Store` interface
- `plane/application/graphql/persisted/postgres_store.go` — PG-backed `Store`
- `plane/application/graphql/persisted/postgres_store_test.go` — testcontainers
- `plane/application/graphql/persisted/cached_store.go` — `CacheStore` read-through wrapper
- `plane/application/graphql/persisted/cached_store_test.go`
- `plane/application/graphql/resolvers/root.go` — `Query`, `Mutation` root resolvers
- `plane/application/graphql/resolvers/repository.go`
- `plane/application/graphql/resolvers/user.go`
- `plane/application/graphql/resolvers/mutation.go`
- `plane/application/graphql/resolvers/*_test.go`
- `plane/application/graphql/middleware/auth.go` — adapter onto `restapi/middleware.PrincipalResolver`
- `plane/application/graphql/middleware/auth_test.go`
- `plane/application/graphql/middleware/cost_meter.go` — bucket charge, parse-cost on reject
- `plane/application/graphql/middleware/cost_meter_test.go`
- `plane/application/graphql/middleware/follower_read.go` — Primary vs Reader dispatch
- `plane/application/graphql/middleware/follower_read_test.go`
- `plane/application/graphql/errors.go` — `ErrorCode` enum, `mapErr`, gqlError envelope
- `plane/application/graphql/errors_test.go`
- `plane/application/graphql/router.go` — `NewHandler(Deps) http.Handler`, `Deps` struct
- `plane/application/graphql/router_test.go`
- `plane/application/graphql/integration_test.go` — testcontainers PG end-to-end
- `cmd/graphql-api/main.go` — wires Reader + Primary pools, persisted store, deps
- `cmd/graphql-api/integration_test.go` — binary smoke test
- `plane/data/migrations/NNN_graphql_persisted_queries.sql` — `graphql.persisted_queries` table

### Modify

- `plane/data/store/metadata.go` — no method changes; verify `MetadataStore` exposes a connection knob suitable for replica-routing OR confirm it's already pool-agnostic and we wire two instances at `cmd/graphql-api/main.go` (recommended path; no interface change)
- `plane/data/compliance/` — add `graphql.persisted_queries` to schema-existence assertions if the compliance suite enumerates tables
- `Makefile` — add `lint-graphql` target invoking `graphql-schema-linter` and the GitHub-subset diff
- `.github/workflows/ci.yml` — invoke `make lint-graphql` in the lint job
- `go.mod` / `go.sum` — add `github.com/graph-gophers/graphql-go`

### Untouched (out of scope)

- REST surface (`plane/application/restapi/`) internals — only `middleware.PrincipalResolver` interface is reused
- gRPC servers
- MCP wiring (#112)
- Webhook delivery
- `pullrequests.Service` implementation (separate dep; resolver returns `NOT_IMPLEMENTED` until landed)
- Subscriptions / WebSocket transport
- Federation

---

## Pre-flight (do once before Task 1)

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
mkdir -p /home/mitta/clients/gitscale/repos/gitscale.worktrees
git worktree add -b feat/application-graphql-api \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-graphql-api \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-graphql-api
git status --porcelain
```

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
go test ./plane/application/restapi/... -count=1
```

If anything fails, stop — baseline is broken.

- [ ] **Step P.3: Confirm Go toolchain ≥ 1.22 and #111 merged**

```bash
go version
git log --oneline origin/main | grep -E "(REST API HTTP layer|#111)" | head -3
```

- [ ] **Step P.4: Vendor GitHub GraphQL schema snapshot**

Download the public GitHub GraphQL schema (https://docs.github.com/en/graphql/overview/public-schema) and trim to the named subset (Query roots: `repository`, `user`, `agent`-stand-in, `pullRequest`, `organization` and the connection types they reach). Commit as `plane/application/graphql/schema/github_subset_snapshot.graphql`. Note: GitHub has no `agent` root — our `agent` is GitScale-specific, so the diff check excludes it.

---

## Task 1 — SDL + schema package

- [ ] **1.1** Author `schema/schema.graphql` with the types listed in spec §Schema. Include `@cost`, `@liveRead`, `@deprecated(reason, removalDate)` directive declarations.
- [ ] **1.2** Add `schema_test.go`: parse via `graphql.MustParseSchema` (graph-gophers); fail on parse error.
- [ ] **1.3** Add `compat_test.go`: load `github_subset_snapshot.graphql`, walk the named subset (`Repository`, `User`, `PullRequest`, `Organization`, plus their connection types), assert every field name in the snapshot exists with the same name in our schema. Excludes `Agent` (GitScale-specific) and any field tagged `@gitscale:extension` in our schema.
- [ ] **1.4** Add `Makefile` target `lint-graphql` running `graphql-schema-linter` (config in `.graphql-schema-linter.json`) and `go test ./plane/application/graphql/schema/...`.
- [ ] **1.5** Wire `lint-graphql` into CI lint job.
- [ ] **1.6** Acceptance: `make lint-graphql` green; deliberate field rename in the SDL fails the compat test.

## Task 2 — Cost analyzer

- [ ] **2.1** `cost/analyzer.go`:
  ```go
  type Cost struct{ Depth, Complexity int }
  type PrincipalKind int

  type Limits struct {
      MaxDepth          map[PrincipalKind]int  // human=10, agent=8
      MaxComplexity     map[PrincipalKind]int  // human=1000, agent=5000
      PersistedDiscount float64                // 0.5
      DefaultFirst      int                    // 20
      MaxFirst          int                    // 100
  }

  type Analyzer struct {
      schema *graphql.Schema
      lim    Limits
  }

  func (a *Analyzer) Analyze(query string, op string, vars map[string]any,
      kind PrincipalKind, persisted bool) (Cost, error)
  ```
- [ ] **2.2** Implementation walks parsed AST, tracks max depth, sums field weights × multiplier-arg values (capped at `MaxFirst`). Persisted: `Complexity = ⌈Complexity × PersistedDiscount⌉`. Returns typed `ErrDepthExceeded` / `ErrCostBudgetExceeded` carrying the cost details.
- [ ] **2.3** `analyzer_test.go`: 30+ table-driven cases covering simple lookup (cost ~1), connection w/ first=50 (cost 100), nested 3-deep agent query (depth=3 ok), 11-deep human (depth-rejected), persisted discount path, missing `first` defaults to 20.
- [ ] **2.4** Acceptance: `go test ./plane/application/graphql/cost/...` green.

## Task 3 — Persisted-query store

- [ ] **3.1** Migration `NNN_graphql_persisted_queries.sql`:
  ```sql
  CREATE SCHEMA IF NOT EXISTS graphql;

  CREATE TABLE graphql.persisted_queries (
    hash           TEXT PRIMARY KEY,            -- "sha256:" prefix + 64 hex
    query          TEXT NOT NULL,
    registered_by  UUID NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  ```
- [ ] **3.2** `persisted/store.go` interface:
  ```go
  type Store interface {
      Get(ctx context.Context, hash string) (string, error) // ErrNotFound on miss
      Put(ctx context.Context, hash, query string, registeredBy uuid.UUID) error // ErrHashConflict on body mismatch
  }
  ```
- [ ] **3.3** `postgres_store.go`: `Get` is `SELECT query FROM graphql.persisted_queries WHERE hash = $1`. `Put` is `INSERT … ON CONFLICT (hash) DO NOTHING`; if conflict, `SELECT query`, compare; mismatch → `ErrHashConflict`.
- [ ] **3.4** `cached_store.go`: read-through wrapping `Store` + `CacheStore`. Put writes through to underlying then `CacheStore.Set(hash, query, 24h)`.
- [ ] **3.5** Tests: testcontainers PG for `postgres_store_test.go`; stub-cache for `cached_store_test.go`.
- [ ] **3.6** Acceptance: `go test ./plane/application/graphql/persisted/... -tags integration` green.

## Task 4 — Errors + middleware

- [ ] **4.1** `errors.go`: closed `ErrorCode` enum from spec; `mapErr(error) (gqlError)` exhaustive on cost-analyzer sentinels, identity sentinels (`ErrAgentNotFound` → `NOT_FOUND` etc.), persisted `ErrNotFound` / `ErrHashConflict`, ratelimit, default `INTERNAL`.
- [ ] **4.2** `middleware/auth.go`: thin adapter that calls `restapi/middleware.PrincipalResolver.Resolve` and injects `Principal` into `context.Context` under a `graphql`-package context key. Skip path: `GET /graphql` introspection (`__schema` only) is permitted unauthenticated for tooling discovery — explicit allowlist of `__schema`/`__type` operations.
- [ ] **4.3** `middleware/cost_meter.go`:
  ```go
  type CostMeter struct { Limiter ratelimit.RateLimiter }
  func (m *CostMeter) Charge(ctx context.Context, p Principal, c cost.Cost, accepted bool) error
  ```
  Surface key `"graphql"`. Accepted: charge `Complexity` tokens. Rejected: charge `parseCost = max(20, ⌈Complexity/10⌉)`. Bucket exhausted → `RATE_LIMITED` with `Retry-After`.
- [ ] **4.4** `middleware/follower_read.go`:
  ```go
  type Pools struct { Reader, Primary store.MetadataStore }
  func StoreFor(ctx context.Context, pools Pools, op ast.OperationType, fieldHasLiveRead bool) store.MetadataStore
  ```
  Mutation → Primary. Query with `@liveRead` anywhere on the operation → Primary. Else Reader.
- [ ] **4.5** Tests for each middleware. Two-distinct-stub-stores test for follower_read confirming the right pool was hit.

## Task 5 — Resolvers

- [ ] **5.1** `resolvers/root.go`: `RootResolver` struct holding `Deps`; methods `Repository`, `User`, `Agent`, `PullRequest`, `Organization` for queries; field methods on each child resolver type.
- [ ] **5.2** `resolvers/repository.go` etc.: each resolver pulls `MetadataStore` from context via `follower_read.StoreFor`, never imports `pgx` or `redis`.
- [ ] **5.3** `resolvers/mutation.go`: `CreatePullRequest` returns `NOT_IMPLEMENTED` if `Deps.PullRequests` is nil; otherwise calls into the gRPC client. `CreateAgent` and `UpdateAgentPermissions` call `identity.Service` and require SVID re-verify (via `Deps.SVIDVerifier.ReVerify(ctx)`; ADR-010). On re-verify failure → `FORBIDDEN`.
- [ ] **5.4** Per-resolver unit tests with `StubMetadataStore`, `StubIdentity`, fixed principal.

## Task 6 — Router + Deps

- [ ] **6.1** `router.go`:
  ```go
  type Deps struct {
      Schema       *graphql.Schema
      Pools        middleware.Pools          // Reader + Primary
      Identity     identity.Service
      PullRequests pullrequests.Service       // may be nil → resolver 501s
      SVID         svid.ReVerifier
      Persisted    persisted.Store
      Resolver     restapi_mw.PrincipalResolver
      Limiter      ratelimit.RateLimiter
      Analyzer     *cost.Analyzer
      Logger       *slog.Logger
  }
  func NewHandler(d Deps) http.Handler
  ```
- [ ] **6.2** Routes: `POST /graphql`, `POST /graphql/persisted/register`, `POST /graphql/persisted/{hash}`. Middleware order outer→inner: `RequestID → Auth → ParseQuery → CostAnalyze → CostMeter(charge) → ResolverDispatch`. Document why: ParseQuery before CostAnalyze (need AST); CostMeter charge after analyze so rejected queries pay parse_cost; auth before parse so unauthenticated never reaches the parser.
- [ ] **6.3** `router_test.go` asserts middleware order via probe handlers.

## Task 7 — Integration test

- [ ] **7.1** `integration_test.go` boots testcontainers Postgres, runs migrations, constructs:
  - real `identity.PostgresService`
  - one `MetadataStore` pool (use as both Reader and Primary in tests; production wires distinct pools)
  - real `MemoryLimiter`
  - real `PostgresStore` for persisted
  - fixed-resolver mapping bearer tokens → real principals
- [ ] **7.2** Cases:
  - Simple query: `{ user(login: "alice") { id name } }` → 200 with data.
  - Cost rejection: 11-deep query → `extensions.code = "DEPTH_EXCEEDED"`, parse_cost charged.
  - Persisted register + execute round-trip; second execute hits cache (assert via cache-instrumentation counter).
  - Mutation: `createAgent` from human principal succeeds; `createAgent` from agent principal forbidden.
  - Rate-limit: capacity=100 refill=0; query with cost=80 succeeds, second cost=80 query gets `RATE_LIMITED`.
  - `@liveRead` directive routes to Primary (verified via follower-read middleware test).
  - Schema-compat: introspection returns `__schema { queryType { fields { name } } }` matching the named subset.
- [ ] **7.3** Build tag `integration`.

## Task 8 — `cmd/graphql-api`

- [ ] **8.1** `main.go`: parse env (`GRAPHQL_LISTEN`, `DATABASE_URL_PRIMARY`, `DATABASE_URL_READER`, `RATELIMIT_AGENT_CAPACITY`, `GRAPHQL_PREVIEW=true`); construct two `pgxpool` instances → two `MetadataStore` instances; build `Deps`; serve.
- [ ] **8.2** Production resolver = `identity.Service.LookupIdentityForCache` adapter (re-used from `cmd/rest-api/main.go`).
- [ ] **8.3** `integration_test.go`: build binary, listen on `:0`, `POST /graphql {"query":"{ __schema { queryType { name } } }"}` → 200.

## Task 9 — Deprecation policy CI

- [ ] **9.1** Extend `make lint-graphql` to assert: every `@deprecated` field carries a `removalDate: "YYYY-MM-DD"`; if `removalDate < today` and field still present, fail.
- [ ] **9.2** Add a deliberately-deprecated test field `Repository.legacyName` with future `removalDate` to exercise the path; remove before final commit OR keep as a documented regression-test artefact.

## Task 10 — SLO doc + GA gate

- [ ] **10.1** Add `docs/slo/graphql.md` capturing the five SLOs verbatim from spec §SLO gating with measurement queries / Prometheus expressions. (This is a separate doc commit; not part of this issue's mandatory file map but the issue is GA-gated and the doc is the gate's contract — include in this PR.)
- [ ] **10.2** Phase 1 ships with `GRAPHQL_PREVIEW=true` default and `GRAPHQL_DEFAULT=off`. Phase 2 GA flip is a follow-up issue (`chore/application-graphql-ga-flip`) opened before merge, not part of this PR.

## Task 11 — ADR + plane-boundary checks

- [ ] **11.1** Run `gitscale-adr-guard` — verify ADR-017 swap-surface invariant: no `pgx`/`redis` imports inside `plane/application/graphql/` (only via `Deps`).
- [ ] **11.2** Run `gitscale-plane-boundary` — no `plane/git`/`plane/workflow`/`plane/edge` imports.
- [ ] **11.3** Run `gitscale-go-conventions`.
- [ ] **11.4** Run `gitscale-outbox-check` — no direct outbox writes; mutations forward to backing services.
- [ ] **11.5** Run `gitscale-event-schema` — no new event types.
- [ ] **11.6** Run `gitscale-agent-quota-check` — every accepted query and every rejected-pre-execution query is metered against `surface="graphql"`. Confirmed via `cost_meter` middleware.

## Task 12 — Self-review battery + PR

- [ ] **12.1** Open follow-up issues before PR:
  - `feat(application): pullrequests.Service for GraphQL createPullRequest mutation`
  - `chore(application): GraphQL Phase 2 GA flag flip + SLO sign-off`
  - `chore(meta): GitHub GraphQL schema snapshot refresh cron`
- [ ] **12.2** Pre-push gates:
  ```bash
  go build ./...
  go vet ./...
  golangci-lint run ./...
  go test -race ./plane/application/graphql/... ./cmd/graphql-api/...
  go test -tags integration ./plane/application/graphql/... ./cmd/graphql-api/...
  make lint-graphql
  make lint-md
  make lint-events
  make lint-determinism
  ```
- [ ] **12.3** Dispatch self-review battery in parallel: `pr-review-toolkit:code-reviewer`, `pr-review-toolkit:silent-failure-hunter`, `adr-historian`, `pr-review-toolkit:type-design-analyzer`, `pr-review-toolkit:pr-test-analyzer`. Resolve findings.
- [ ] **12.4** Commit using Conventional Commits: `feat(application): GraphQL API — field-stable subset + cost analysis (#113)`.
- [ ] **12.5** `gh pr create` with title `[Application] GraphQL API — field-stable GitHub-compat subset + cost analysis`, body referencing ADR-017, listing the three follow-up issues, including self-review block, co-author trailer, `Closes #113`.

---

## Acceptance criteria (mirror issue body)

- [ ] Query cost analysis rejects an over-budget query with structured `extensions.code = "COST_BUDGET_EXCEEDED"` (or `DEPTH_EXCEEDED`) **before** any resolver executes; rejected query charges parse_cost.
- [ ] Persisted-query path returns `extensions.persistedDiscount = 0.5` and `extensions.cost` reflecting the discount.
- [ ] Follower reads execute against `Reader` `MetadataStore`; queries with `@liveRead` and all mutations execute against `Primary`. Verified by integration test asserting which pool was hit.
- [ ] Schema field names match GitHub GraphQL for the named subset; CI compat-diff check is green and fails on deliberate rename.
- [ ] Every `@deprecated` field carries `removalDate`; lint fails when a past-date deprecated field is still present.
- [ ] Phase 1 ships behind `GRAPHQL_PREVIEW=true`; SLO doc committed defining the five GA gates.
- [ ] Integration tests run against testcontainers PG; no mock-DB tests in `graphql` package.
- [ ] `make test` and pre-push gates all green.
- [ ] Self-review battery clean.
- [ ] PR closes #113 and links the three follow-up issues.
