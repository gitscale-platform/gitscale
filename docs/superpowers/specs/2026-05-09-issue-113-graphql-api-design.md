# Spec — Issue #113 GraphQL API (field-stable GitHub-compat subset, cost analysis, SLO-backed GA)

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/113
Plane: application
Priority: p2 (Wave 1)
ADR-impact: conforming (ADR-017 swap-surface interfaces; ADR-008 outbox; ADR-019 plane boundary; ADR-010 SVID re-verify at high-risk boundaries)

## Problem

The application plane now exposes identity + repository over REST (`plane/application/restapi/`, #111). Agents and ecosystem tooling expect a GitHub-shaped GraphQL surface — `repository`, `user`, `agent`, `pullRequest`, `organization` — to read aggregated data in one round-trip. A naive GraphQL endpoint is the canonical hot-path DoS vector: an unbounded query `repository { pullRequests(first: 100) { commits(first: 100) { author { followers(first: 100) { ... } } } } }` will fan out into thousands of resolver calls and crater the metadata store. Without a cost budget enforced *before* resolution, agent traffic class breaks the cluster.

## Goals

1. Add a `plane/application/graphql/` package owning the schema, executor, cost analyser, persisted-query store, and resolvers, mounted at `/graphql` and `/graphql/persisted/{hash}`.
2. Field-stable subset of GitHub's GraphQL schema: top-level roots (`repository`, `user`, `agent`, `pullRequest`, `organization`) and the most-used connection shapes (`pullRequests`, `issues`, `commits`, `members`). Field *names* match GitHub; field *semantics* are GitScale's.
3. Pre-execution cost analysis: depth limit, complexity score, agent-class multiplier. Reject before any resolver runs when budget is exceeded.
4. Persisted-query path: clients POST query text once with `sha256` to register; subsequent calls send the hash and receive a cost-multiplier discount documented in response `extensions`.
5. Follower-read default: read resolvers route to a `MetadataStore` configured against the read replica unless the operation carries `@liveRead`.
6. Mutations as thin wrappers over existing gRPC services: `createPullRequest`, `createAgent`, `updateAgentPermissions`. No outbox writes from GraphQL itself — every mutation forwards to an application-plane service that is already Tx + outbox correct (ADR-008).
7. Deprecation contract: every `@deprecated` field carries an ISO-8601 `removalDate`; CI fails any schema change that removes a deprecated field before its date.
8. Phase 1 ships behind a `graphql_preview` feature flag (no SLO). Phase 2 GA gating defined as concrete SLOs — see §SLO gating.
9. Every resolver hop is metered through the same `ratelimit.RateLimiter` surface used by REST, with a separate surface key `graphql` and a per-query cost-as-tokens accounting model.

## Non-goals

- Subscriptions / WebSocket transport — out of scope for this issue. GraphQL over HTTP `POST` only.
- File uploads (`Upload` scalar) — deferred.
- Federation / Apollo Gateway — explicit non-goal; GitScale GraphQL is a single graph.
- Full GitHub GraphQL parity — only the named subset. Unrecognised top-level fields return a stable `FIELD_NOT_SUPPORTED` error code in `extensions.code`.
- Caching at the response level — `extensions.cacheControl` headers may be returned, but GitScale operates no GraphQL response cache in this PR.
- Webhook delivery / CI pipeline mutations — out of scope.
- Custom directives beyond `@liveRead` and the schema-builtin `@deprecated`.

## ADR-017 fragments quoted

> "Three Go interfaces are defined in `plane/data/`: `MetadataStore` (all SQL operations across the five schema domains), `CacheStore` (key-value, pub/sub, and TTL semantics needed by the edge and Git proxy), and `EventQueue` (outbox-to-Kafka publishing). Application code never imports a concrete driver; it receives a concrete implementation injected at startup."
>
> "Passing the compliance suite is the production-readiness bar for any implementation."

GraphQL resolvers obey this verbatim: every resolver receives `MetadataStore` and `CacheStore` via the package `Deps` struct and never imports `pgx` or `redis` directly. The follower-read default is implemented by injecting **two** `MetadataStore` instances — `Reader` (replica) and `Primary` — chosen per-field based on the `@liveRead` directive and per-mutation operation. Both must pass the same `plane/data/compliance/` suite; the swap surface is preserved.

The cost-analysis policy is layered on top of the swap surface, not embedded in it. Cost computation is pure (depends only on the parsed query AST + a static schema cost map), so it cannot be sidestepped by an alternative `MetadataStore` implementation.

## Design decisions (defaults selected by supervisor)

| Question | Choice | Rationale |
|---|---|---|
| GraphQL Go library | **`github.com/graph-gophers/graphql-go`** | Schema-first, no codegen step in build pipeline, AST is publicly inspectable for cost analysis, MIT-licensed. `gqlgen` requires a codegen step that complicates the agent-driven workflow; `99designs/gqlgen` deferred unless a measured perf gap appears. |
| Schema source of truth | **Single `schema.graphql` SDL file** in `plane/application/graphql/schema/` checked into the repo | Reviewable diff for every field add / deprecation. CI lints SDL via `graphql-schema-linter`. |
| Cost analysis algorithm | **Static + dynamic two-pass:** (1) parse, depth-check (max 10 for human, 8 for agent), (2) walk AST summing `@cost(weight: N)` directive values per field; connection fields cost `weight × first/last argument` (capped at 100). Reject before resolution if total > budget. | Mirrors GitHub's published model; small enough to implement in-house; AST walk is O(query size). |
| Cost budget | Per-principal-kind: human=1000, agent=5000, persisted-query gets ×0.5 multiplier. Configurable via env. | Agents are primary traffic class — they need higher headroom; persisted queries are pre-vetted shapes so a discount aligns incentives. |
| Persisted-query store | **PostgreSQL table `graphql.persisted_queries(hash PRIMARY KEY, query TEXT, registered_by UUID, created_at TIMESTAMPTZ)`** with Redis read-through cache via `CacheStore` | hash → text lookup on every persisted call; cache hit is the hot path. New schema domain `graphql` (or fold into `repositories` if domain count is capped — see Open questions). |
| Follower-read implementation | `Deps` struct carries `Primary MetadataStore` and `Reader MetadataStore`. Resolver dispatcher inspects the operation: any field path containing `@liveRead` and all mutations route to `Primary`; everything else routes to `Reader`. | Two pgxpool connections, one per replica role. Re-uses the swap surface (ADR-017) — no new interface. |
| Auth / principal | Re-uses `restapi/middleware.Auth` with a thin adapter; `Principal` injected into `context.Context` for resolvers. SVID re-verification is required only for `createAgent` and `updateAgentPermissions` (ADR-010 high-risk admin boundary). | Loose coupling — auth lives in one place; GraphQL imports the resolver-friendly bits, not the HTTP plumbing. |
| Mutation routing | Thin resolver functions that call existing gRPC clients (`identity.Service`, future `pullrequests.Service` for `createPullRequest`). No direct DB writes from resolvers. | ADR-019 spirit applied within the application plane: each domain owns its writes; GraphQL composes. |
| Error envelope | GraphQL standard `errors[]` array with `extensions.code` from a closed enum (`COST_BUDGET_EXCEEDED`, `DEPTH_EXCEEDED`, `PERSISTED_QUERY_NOT_FOUND`, `UNAUTHENTICATED`, `FORBIDDEN`, `RATE_LIMITED`, `FIELD_NOT_SUPPORTED`, `VALIDATION_FAILED`, `INTERNAL`). | Stable contract; mirrors GitHub's `extensions.code` shape. |
| Metering | Cost-as-tokens: every accepted query consumes `cost` tokens from the per-principal `graphql` bucket; rejected-pre-execution queries consume the cheaper `parse_cost = max(20, ⌈cost/10⌉)` to deter probe-floods. | Aligns billing with capacity; pre-execution rejection is not free. |

## Architecture

### Package layout

```
plane/application/graphql/
  doc.go                       package doc, ADR-017 quote, plane boundary
  schema/
    schema.graphql             SDL — single source of truth
    schema_test.go             SDL parse + lint smoke test
  cost/
    analyzer.go                AST → Cost int; depth + complexity walk
    analyzer_test.go           table-driven query → expected cost
    directives.go              @cost, @liveRead directive defs
  persisted/
    store.go                   Store interface (Get, Put)
    postgres_store.go          PG-backed impl
    cached_store.go            CacheStore-backed read-through wrapper
    integration_test.go
  resolvers/
    root.go                    Query, Mutation root resolvers
    repository.go              Repository, PullRequest, Issue, Commit
    user.go                    User, Agent, Organization
    mutation.go                createPullRequest, createAgent, updateAgentPermissions
    *_test.go                  per-resolver unit tests (StubMetadataStore)
  middleware/
    auth.go                    adapter onto restapi.PrincipalResolver
    cost_meter.go              charges tokens against ratelimit.RateLimiter, surface="graphql"
    follower_read.go           selects Primary vs Reader based on directive presence
  errors.go                    ErrorCode enum, mapErr, gqlError envelope
  router.go                    NewHandler(deps Deps) http.Handler
  router_test.go
  integration_test.go          testcontainers PG + httptest end-to-end
cmd/graphql-api/
  main.go                      wires Reader + Primary pools, persisted store, deps
  integration_test.go          binary smoke test
plane/data/migrations/
  NNN_graphql_persisted_queries.sql
```

### Schema (SDL excerpt)

```graphql
directive @cost(weight: Int! = 1, multipliers: [String!]) on FIELD_DEFINITION
directive @liveRead on FIELD | QUERY
directive @deprecated(reason: String!, removalDate: String!) on FIELD_DEFINITION | ENUM_VALUE

type Query {
  repository(owner: String!, name: String!): Repository @cost(weight: 1)
  user(login: String!):                      User       @cost(weight: 1)
  agent(id: ID!):                            Agent      @cost(weight: 1)
  pullRequest(id: ID!):                      PullRequest @cost(weight: 2)
  organization(login: String!):              Organization @cost(weight: 1)
}

type Repository {
  id: ID!
  name: String!
  owner: User!                                                  @cost(weight: 1)
  pullRequests(first: Int, after: String, states: [PRState!]):
      PullRequestConnection!                                    @cost(weight: 2, multipliers: ["first"])
  issues(first: Int, after: String): IssueConnection!           @cost(weight: 2, multipliers: ["first"])
  defaultBranch: Ref
  createdAt: DateTime!
}

type Mutation {
  createPullRequest(input: CreatePullRequestInput!): CreatePullRequestPayload!  @cost(weight: 10)
  createAgent(input: CreateAgentInput!):             CreateAgentPayload!        @cost(weight: 10)
  updateAgentPermissions(input: UpdateAgentPermissionsInput!):
                                                     UpdateAgentPermissionsPayload! @cost(weight: 10)
}
```

Field-name compatibility with GitHub GraphQL is asserted by a CI check that diffs a snapshot of the GitHub public schema against ours for the named subset; mismatches fail the build.

### Cost analyzer

```go
type Cost struct {
    Depth      int  // max query depth observed
    Complexity int  // sum of weights × multipliers
}

type Analyzer struct {
    schema       *graphql.Schema
    maxDepth     map[PrincipalKind]int       // human=10, agent=8
    maxComplexity map[PrincipalKind]int      // human=1000, agent=5000
    persistedDiscount float64                // 0.5
}

func (a *Analyzer) Analyze(doc *ast.Document, op string, vars map[string]any, p Principal, persisted bool) (Cost, error)
```

- Depth limit fires first (cheap traversal). Returns `extensions.code = "DEPTH_EXCEEDED"`.
- Complexity limit fires second. Returns `extensions.code = "COST_BUDGET_EXCEEDED"` with `extensions.cost` and `extensions.budget` populated.
- `multipliers: ["first"]` means `weight × min(first_arg_value, 100)`. Missing `first` defaults to 20.
- Persisted queries get `complexity *= persistedDiscount`. The discount is reflected in `extensions.persistedDiscount: 0.5`.

### Persisted-query path

`POST /graphql/persisted/register` body `{"query": "..."}` → returns `{"hash": "sha256:..."}`. Stores `(hash, query, registered_by=principal.ID, created_at)`. Idempotent — re-registering the same `(hash, query)` is a no-op; hash collision with a *different* query body is a 409 (impossible at SHA-256 unless byte-mismatch — return error to make the contract explicit).

`POST /graphql/persisted/{hash}` body `{"variables": {...}, "operationName": "..."}` → executes the stored query.

Read path: `CacheStore.Get(hash)` first; on miss, `PostgresStore.Get`, populate cache. TTL = 24h, invalidation on `Put`.

### Follower-read directive

`@liveRead` on a query field forces the dispatcher to swap the resolver context's `MetadataStore` from `Reader` to `Primary` for that field's subtree. The default policy is: read = `Reader`, mutation = `Primary`.

The directive is enforced via a context-key swap performed during the field-resolution phase, not at AST level — it must respect operation type (`mutation` always primary, regardless of directive).

### Metering (ADR-008 spirit + agent-quota)

`cost_meter` middleware:

1. Pre-execution: calls `Analyzer.Analyze`. On reject, charges `parse_cost = max(20, ⌈cost/10⌉)` against the `graphql` bucket.
2. On accept: charges full `cost` against the bucket. If the bucket is exhausted, returns `extensions.code = "RATE_LIMITED"` with `Retry-After` (in `extensions.retryAfterSeconds`).
3. Post-execution: writes a structured log entry with `request_id, principal_id, cost, depth, persisted, op_name, status` for the metering pipeline (#15-revocation reconciliation feeds this; webhook/billing consumers downstream are out of scope here).

No outbox row is written by GraphQL directly. Mutations forward to gRPC services that already write source + outbox in the same Tx (ADR-008 conforming).

### Mutation routing

| Mutation | Backing service | High-risk SVID re-verify? |
|---|---|---|
| `createPullRequest` | `pullrequests.Service.CreatePullRequest` (gRPC) | no |
| `createAgent` | `identity.Service.CreateAgent` | yes (ADR-010 §admin actions) |
| `updateAgentPermissions` | `identity.Service.UpdateAgentPermissions` | yes |

For `createPullRequest`: if the `pullrequests.Service` does not yet exist, this PR is **blocked** on it. Currently the spec assumes #111 ships and a thin `pullrequests.Service` ships in a separate dependency issue; if that issue is not landed, `createPullRequest` resolver returns `extensions.code = "NOT_IMPLEMENTED"` (501-equivalent at the GraphQL layer) with a follow-up issue link, mirroring the `DELETE /v1/repos/{id}` pattern from #111.

### Error envelope

```json
{
  "errors": [{
    "message": "query exceeds cost budget",
    "extensions": {
      "code": "COST_BUDGET_EXCEEDED",
      "cost": 12345,
      "budget": 5000,
      "request_id": "01JKW..."
    }
  }],
  "data": null
}
```

Closed `code` enum, exhaustive switch in `mapErr`, unit-tested.

### SLO gating (Phase 2 GA)

Phase 1 ships behind feature flag `graphql_preview=true`. Phase 2 GA flips the flag to default-on.

GA blockers — measured continuously over a rolling 7-day window in pre-prod and 14-day window in prod-pilot:

| SLO | Target | Measurement |
|---|---|---|
| **Availability** | 99.9% successful (`200` HTTP, `data` non-null OR `errors[].extensions.code` ∈ {VALIDATION_FAILED, FORBIDDEN, NOT_FOUND, COST_BUDGET_EXCEEDED, DEPTH_EXCEEDED, RATE_LIMITED}) | Prom counter `graphql_requests_total{result}` |
| **Latency P95** | ≤ 250ms for persisted queries, ≤ 600ms for ad-hoc | Histogram `graphql_request_duration_seconds_bucket{persisted}` |
| **Cost-rejection accuracy** | False-positive rate < 0.1% (rejected queries that would have completed under budget if executed) | Shadow-execute on 1% sample; counter `graphql_cost_false_positive_total` |
| **Persisted-query hit rate** | ≥ 60% of agent traffic | `graphql_requests_total{persisted="true",principal_kind="agent"} / total{principal_kind="agent"}` |
| **Schema-compat drift** | 0 unexpected GitHub-schema diffs in named subset | CI check; alert on first drift |

GA decision: all five green for two consecutive measurement windows. The flip is a single env var change (`GRAPHQL_DEFAULT=on`); no schema migration. SLO definitions live in `docs/slo/graphql.md` (out of scope for this issue — created as a separate doc commit before flag flip).

### Plane boundary (ADR-019, gitscale-plane-boundary)

GraphQL is purely application-plane. It imports:

- `plane/data/store.MetadataStore` (Reader + Primary, swap-surface per ADR-017)
- `plane/data/cache.CacheStore` (persisted-query read-through)
- `plane/data/ratelimit.RateLimiter` (cost metering)
- `plane/application/identity.Service` (mutation backing)
- `plane/application/restapi/middleware` (auth adapter, request-id) — sibling import inside application plane is permitted

It does **not** import:

- `plane/git/*`, `plane/workflow/*`, `plane/edge/*`
- `pgx`, `go-redis`, or any concrete driver
- Gitaly clients

### Outbox

GraphQL writes no outbox rows directly. Every mutation forwards to a gRPC service whose backing implementation writes source + outbox in the same Tx (ADR-008). `gitscale-outbox-check` therefore passes by composition, not by direct conformance.

## Test plan

| Layer | Test |
|---|---|
| Schema | `schema_test.go` parses SDL, lints with `graphql-schema-linter` (CI), diffs the named-subset against a vendored snapshot of GitHub's GraphQL schema |
| Cost analyzer | Table-driven: 30+ queries → expected `(depth, complexity)`; depth-limit firing; multiplier on `first`; agent vs human budget |
| Persisted store | Stub-cache tests for hit / miss / collision; testcontainers PG test for round-trip |
| Resolvers (unit) | StubMetadataStore + StubIdentity; field-by-field correctness |
| Cost meter middleware | Rejected query charges parse_cost; accepted query charges full cost; bucket exhausted returns RATE_LIMITED |
| Follower read | Resolver routed to `Reader` by default; `@liveRead` flips to `Primary`; mutation always `Primary` (test with two distinct stub stores asserting which one was hit) |
| Auth | Unauthenticated → `UNAUTHENTICATED`; agent attempting `createAgent` for someone else's parent → `FORBIDDEN` |
| Integration | testcontainers PG + httptest. End-to-end query, persisted register + execute, mutation, cost-rejection. Mirrors `plane/application/restapi/integration_test.go` |
| Binary | `cmd/graphql-api/integration_test.go` boots `main`, `POST /graphql {"query": "{ __schema { queryType { name } } }"}` → 200 |
| Schema-compat | CI step diffs named-subset vs GitHub snapshot; deliberate breaking change (rename a field) must fail CI |
| Deprecation | CI step parses SDL, asserts every `@deprecated` carries `removalDate`, fails if removalDate < today and the field still exists |

All testcontainer tests gated by `//go:build integration`.

## Risks / unknowns

- **graph-gophers/graphql-go AST surface stability.** The library exposes `internal/query` AST types; if those are not stably exported, the cost analyzer must be implemented against a re-parsed AST via the public schema document. Mitigation: the cost analyzer parses the query string a second time using `graphql-go-tools` parser (zero-dep, stable AST) — small CPU overhead, isolates us from upstream churn.
- **GitHub-schema snapshot freshness.** The compat-diff check uses a vendored snapshot. If GitHub renames a field, our check still passes against the old snapshot. Refresh cadence: monthly cron PR opened by a workflow (out of scope for this PR; tracked separately).
- **Persisted-query schema-domain placement.** New `graphql` domain or fold into existing? Defer to data-plane review — see Open questions.
- **`pullrequests.Service` non-existence at merge time.** `createPullRequest` resolver returns `NOT_IMPLEMENTED` until the dependency lands. Acceptance criteria for this issue lists `createPullRequest` as a route, not as a 2xx — the route is reserved.
- **SLO measurement infrastructure.** Prometheus + histogram registration assumed already in place via existing `cmd/rest-api` patterns. If not, wire-up follows the same pattern; small risk.
- **Cost-budget calibration drift.** Initial `human=1000, agent=5000` figures are best-guess. Phase 1 preview window is the calibration window; SLO §cost-rejection-accuracy gates the values before GA.

## Open questions

1. **`graphql` schema domain or fold into `repositories`?** ADR-006's five domains are identity / repositories / collaboration / ci / billing. Persisted queries are cross-domain by nature. Recommend: new `graphql` schema with one table; raises domain count to six. Decide with `data-plane`.
2. **Schema diff CI step location.** Standalone CI job vs `make lint-graphql`. Recommend latter — keeps lint surface unified.
3. **`@cost` directive on input types?** Out of scope for v1; revisit if a measured input-explosion attack surface appears.

## References

- ADR-017 (interface swap surface) — `docs/architecture.md §8`
- ADR-008 (outbox) — `docs/architecture.md §8`
- ADR-019 (plane boundary) — `docs/architecture.md §8`
- ADR-010 (SVID re-verify at admin boundary) — `docs/architecture.md §8`
- Sibling pattern: `plane/application/restapi/` (#111)
- Backing services: `plane/application/identity/` (#15)
- GitHub GraphQL public schema (snapshot in `vendor/github-graphql-schema/`)
- `graph-gophers/graphql-go` — schema-first GraphQL Go runtime
