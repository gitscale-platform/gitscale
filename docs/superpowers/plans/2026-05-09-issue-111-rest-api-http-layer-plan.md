# Issue #111 REST API HTTP layer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add HTTP/JSON edge over the existing identity service and repository store, mounted at `/v1/`, gated by auth + rate-limit middleware, with closed-enum JSON error envelope. Mirrors `plane/application/identity/` patterns.

**Architecture:** stdlib `net/http.ServeMux` (Go 1.22 patterns), `Principal` interface in context, `PrincipalResolver` injected for tests, per-principal token-bucket via `plane/data/ratelimit`, integration tests with testcontainers Postgres + `httptest.Server`.

**Tech Stack:** Go 1.22, stdlib `net/http`, pgx/v5, testcontainers-go, google/uuid.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-111-rest-api-http-layer-design.md`

**Branch:** `feat/application-rest-api-http-layer` (worktree: `../gitscale.worktrees/feat-application-rest-api-http-layer`)

**ADR-impact:** conforming (ADR-008, ADR-017, ADR-019). No new ADR.

---

## File map

### Create

- `plane/application/restapi/doc.go` — package doc, ADR refs, plane boundary statement
- `plane/application/restapi/errors.go` — `ErrorCode` enum, `errorEnvelope`, `writeError`, `mapErr`
- `plane/application/restapi/errors_test.go` — exhaustive sentinel→code mapping
- `plane/application/restapi/principal.go` — `Principal` interface, kinds, context helpers
- `plane/application/restapi/principal_test.go`
- `plane/application/restapi/middleware/auth.go` — `PrincipalResolver`, `Auth`
- `plane/application/restapi/middleware/auth_test.go`
- `plane/application/restapi/middleware/ratelimit.go` — `RateLimit`, config
- `plane/application/restapi/middleware/ratelimit_test.go`
- `plane/application/restapi/middleware/request_id.go` — `RequestID` (ULID)
- `plane/application/restapi/middleware/request_id_test.go`
- `plane/application/restapi/identity_handlers.go`
- `plane/application/restapi/identity_handlers_test.go`
- `plane/application/restapi/repos_handlers.go`
- `plane/application/restapi/repos_handlers_test.go`
- `plane/application/restapi/router.go` — `NewRouter(Deps) http.Handler`, `Deps` struct
- `plane/application/restapi/router_test.go` — middleware ordering test
- `plane/application/restapi/pagination.go` — base64-cursor encode/decode
- `plane/application/restapi/pagination_test.go`
- `plane/application/restapi/integration_test.go` — testcontainers PG, full chain
- `cmd/rest-api/main.go` — wires real services
- `cmd/rest-api/integration_test.go` — boots binary, healthz + one route

### Modify

- `plane/data/store/metadata.go` — extend `RepositoryReader` with `ListByOrg(ctx, orgID, after Cursor, limit int) ([]Repository, error)`; add a `Cursor` value type (or use `(uuid.UUID, time.Time)` pair to avoid leaking cursor concept into store; recommended: pass `afterID *uuid.UUID, afterCreatedAt *time.Time` and let restapi own opaque encoding)
- `plane/data/store/postgres/metadata.go` (or wherever the postgres `RepositoryReader` impl lives) — implement `ListByOrg` keyset query `WHERE org_id = $1 AND (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4`
- `plane/data/store/stub/metadata.go` — implement stub `ListByOrg` (sort + filter in-memory)
- `plane/data/store/postgres/compliance_test.go` — verify the existing migration covers the index needed (`(org_id, created_at, id)` keyset index); if missing, add migration `007_repositories_keyset_index.sql`

### Untouched (out of scope)

- `plane/application/identity/` internals
- gRPC servers
- GraphQL / MCP wiring (issues #112, #113)
- Webhook delivery
- Repository `UpdatePermissions` HTTP surface

---

## Pre-flight (do once before Task 1)

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
mkdir -p /home/mitta/clients/gitscale/repos/gitscale.worktrees
git worktree add -b feat/application-rest-api-http-layer \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-rest-api-http-layer \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-rest-api-http-layer
git status --porcelain
```

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
go test ./plane/application/identity/... -count=1
```

If anything fails, stop — baseline is broken.

- [ ] **Step P.3: Confirm Go toolchain ≥ 1.22**

```bash
go version  # expect go1.22 or higher; required for ServeMux pattern syntax
```

---

## Task 1 — `RepositoryReader.ListByOrg` keyset query

- [ ] **1.1** Add to `RepositoryReader`:
  ```go
  ListByOrg(ctx context.Context, orgID uuid.UUID,
      afterID *uuid.UUID, afterCreatedAt *time.Time,
      limit int) ([]Repository, error)
  ```
  Document: `limit` capped at 100 by the caller; `nil` cursors mean start.
- [ ] **1.2** Postgres impl: keyset SQL. Use `pgx.Rows` scan, no `ORDER BY` index hint.
- [ ] **1.3** Stub impl: copy → sort → filter → slice. Stable order on `(created_at, id)`.
- [ ] **1.4** Tests for both impls (write 25 rows, page through with limit=10, assert union = full set, no dupes, no gaps).
- [ ] **1.5** If keyset index missing, add migration `007_repositories_keyset_index.sql`:
  ```sql
  CREATE INDEX IF NOT EXISTS repositories_org_keyset_idx
    ON repositories.repositories (org_id, created_at, id);
  ```
  And extend `compliance_test.go` migrations list.
- [ ] **1.6** Acceptance: `go test ./plane/data/store/...` green.

## Task 2 — `restapi` package skeleton

- [ ] **2.1** Create `doc.go`, `errors.go`, `errors_test.go`. Closed enum:
  ```go
  type ErrorCode string
  const (
    CodeUnauthenticated  ErrorCode = "unauthenticated"
    CodeForbidden        ErrorCode = "forbidden"
    CodeNotFound         ErrorCode = "not_found"
    CodeValidationFailed ErrorCode = "validation_failed"
    CodeConflict         ErrorCode = "conflict"
    CodeRateLimited      ErrorCode = "rate_limited"
    CodeInternal         ErrorCode = "internal"
  )
  ```
- [ ] **2.2** `mapErr(error) (httpStatus int, code ErrorCode, msg string)` — exhaustive switch on identity sentinels + store unique-violation + `context.DeadlineExceeded` + default `internal`.
- [ ] **2.3** Test: every sentinel listed in spec maps correctly; default case logs and returns 500.
- [ ] **2.4** `principal.go`: interface, `PrincipalKind` const, context get/set with unexported typed key. `principal_test.go`.

## Task 3 — Middleware

- [ ] **3.1** `middleware/request_id.go`: extract `X-Request-Id` if present (validate ULID-or-UUID), else generate ULID; inject into ctx and response header.
- [ ] **3.2** `middleware/auth.go`:
  ```go
  type PrincipalResolver interface {
    Resolve(ctx context.Context, bearer string) (Principal, error)
  }
  func Auth(r PrincipalResolver, onUnauth http.HandlerFunc) func(http.Handler) http.Handler
  ```
  Skip path: `/healthz`. Strip `Bearer ` prefix. Empty/missing → 401.
- [ ] **3.3** `middleware/ratelimit.go`:
  ```go
  type RateConfig struct {
    AgentCapacity, AgentRefillPerSec float64
    HumanCapacity, HumanRefillPerSec float64
  }
  func RateLimit(l ratelimit.RateLimiter, onLimited http.HandlerFunc, c RateConfig) func(http.Handler) http.Handler
  ```
  Surface enum constant `"rest_api"`. Skip `/healthz`. Set `Retry-After` header from refill rate.
- [ ] **3.4** Tests for each: missing token, invalid token, valid human, valid agent, capacity-exhausted.

## Task 4 — Identity handlers

- [ ] **4.1** `identity_handlers.go`:
  - `createUser` — JSON body `{email, credential}`; calls `Service.CreateUser`; 201 + Location header.
  - `getUser` — `r.PathValue("id")`; 404 on nil; 200 with user JSON otherwise.
  - `createAgent` — body `{parent_user_id, display_name, scope[]}`; principal must be human and own `parent_user_id` OR be `parent_user_id` itself (forbidden otherwise).
  - `getAgent` — like getUser.
  - `revokeAgent` — body `{reason}`; calls `RevokeAgent`; 204.
  - `updatePerms` — `PATCH` body `{scope[]}`; 204.
- [ ] **4.2** Authorization rules table (forbidden → 403):
  - Agent creating user: forbidden.
  - Agent revoking another agent: forbidden unless its parent.
  - Anyone reading any user/agent: allowed (this PR; tighten later).
- [ ] **4.3** Tests with `StubService`, `httptest.NewRecorder`, fixed-resolver injecting principals.

## Task 5 — Repository handlers

- [ ] **5.1** `pagination.go`:
  ```go
  type Cursor struct { AfterID uuid.UUID; AfterCreatedAt time.Time }
  func EncodeCursor(c Cursor) string
  func DecodeCursor(s string) (Cursor, error)  // empty string → zero, no error
  ```
  base64-url + json. Tests: round-trip, malformed, empty.
- [ ] **5.2** `repos_handlers.go`:
  - `createRepo` — body `{slug, org_id}`; principal must be human (agents create repos via #112 MCP path, not direct REST in this PR — return 403 for agent principal).
  - `getRepo` — by id; 404 on nil.
  - `deleteRepo` — calls a yet-to-exist `Service.DeleteRepository`. **Out of scope**: implementing repo deletion. This PR returns 501 with `code=internal` and a TODO comment + follow-up issue link. Confirmed not in acceptance list as a 2xx; the issue body lists the route, so we surface it returning 501 documented as deferred. **Decision:** ship the route returning 501 to keep the URL space reserved. **Open follow-up issue before merge** to track real implementation.
  - `listOrgRepos` — `?cursor=...&limit=N`; calls `ListByOrg`; returns `{items: [...], next_cursor: "..."}` (omit `next_cursor` when fewer than `limit` rows returned).
- [ ] **5.3** Tests: createRepo human/agent matrix, listOrgRepos pagination round-trip across 25 rows.

## Task 6 — Router + Deps

- [ ] **6.1** `router.go`:
  ```go
  type Deps struct {
    Identity   identity.Service
    Repos      store.RepositoryWriter
    Resolver   middleware.PrincipalResolver
    Limiter    ratelimit.RateLimiter
    RateConfig middleware.RateConfig
    Logger     *slog.Logger
  }
  func NewRouter(d Deps) http.Handler
  ```
- [ ] **6.2** Middleware ordering: outer→inner = `RequestID → Auth → RateLimit → mux`. Document why (request ID needed for auth-failure logs; auth before rate-limit so a missing-token doesn't burn quota).
- [ ] **6.3** `router_test.go` asserts middleware order (request_id present in 401 body, no token consumed on 401).

## Task 7 — Integration test

- [ ] **7.1** `integration_test.go` boots testcontainers Postgres, runs migrations, constructs real `identity.PostgresService`, real `RepositoryWriter`, `MemoryLimiter`, fixed-resolver mapping `tok-human-1`→ a real user row.
- [ ] **7.2** Cases:
  - Full identity flow: createUser → getUser → createAgent (agent token) → getAgent → revoke → getAgent returns revoked state.
  - Full repo flow: createRepo → getRepo → listOrgRepos paginates.
  - Rate-limit: 5 reqs at capacity=2 refill=0 produces 2× 200, 3× 429 with `Retry-After`.
  - 401 path: no header → unauthenticated.
- [ ] **7.3** Build tag `integration` if the package convention is to gate. Match identity package convention exactly.

## Task 8 — `cmd/rest-api`

- [ ] **8.1** `main.go`: parse env (`REST_API_LISTEN`, `RATELIMIT_AGENT_CAPACITY`, ...); construct deps; serve `http.ListenAndServe`.
- [ ] **8.2** Wire production resolver: `identity.Service.LookupIdentityForCache` adapter.
- [ ] **8.3** `integration_test.go`: build binary via `go test -tags integration` helper, listen on `:0`, GET /healthz → 200, GET /v1/users/{id} with bearer → 200/404.

## Task 9 — ADR + plane-boundary checks

- [ ] **9.1** Run `gitscale-adr-guard` skill — verify no contradiction with ADR-017 (REST cost analysis) or ADR-019 (no workflow-plane calls).
- [ ] **9.2** Run `gitscale-plane-boundary` skill — verify no `plane/git` or `plane/workflow` imports in the new package.
- [ ] **9.3** Run `gitscale-go-conventions`.
- [ ] **9.4** Run `gitscale-outbox-check` — no new outbox writes from REST itself; identity service writes already covered.
- [ ] **9.5** Run `gitscale-event-schema` — no new event types.

## Task 10 — Self-review battery + PR

- [ ] **10.1** Open follow-up issue: "Implement repository deletion service + un-501 the DELETE /v1/repos/{id} route."
- [ ] **10.2** Run pre-push gates:
  ```bash
  go build ./...
  go vet ./...
  golangci-lint run ./...
  go test -race ./plane/application/restapi/... ./cmd/rest-api/... ./plane/data/store/...
  make lint-events
  make lint-determinism
  ```
- [ ] **10.3** Dispatch self-review battery in parallel: `pr-review-toolkit:code-reviewer`, `pr-review-toolkit:silent-failure-hunter`, `adr-historian`, `pr-review-toolkit:type-design-analyzer` (new public types in restapi pkg), `pr-review-toolkit:pr-test-analyzer`. Resolve findings.
- [ ] **10.4** Commit using Conventional Commits: `feat(application): REST API HTTP layer for identity + repositories (#111)`.
- [ ] **10.5** `gh pr create` with title `[Application] REST API HTTP layer — identity + repository endpoints`, body containing all sections from plan §pr-quality-bar, `Closes #111`, follow-up issue cross-link, self-review block, co-author trailer.

---

## Acceptance criteria (mirror issue body)

- [ ] All 10 listed endpoints respond with documented 2xx / 4xx / 5xx codes (DELETE /v1/repos returns documented 501 with follow-up issue tracked).
- [ ] Auth middleware rejects missing/invalid bearer with 401 + envelope.
- [ ] Rate-limit middleware returns 429 with `Retry-After`.
- [ ] Integration tests run against testcontainers PG; no mock-DB tests in `restapi` package.
- [ ] `make test` and the pre-push gates list all green.
- [ ] Self-review battery clean.
- [ ] PR closes #111 and links the deletion follow-up issue.
