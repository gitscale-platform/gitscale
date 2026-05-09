# Issue #112 MCP server — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `plane/application/mcp` exposing seven MCP tools
(`git_clone`, `pr_create`, `ci_trigger`, `quota_status`, `agents_md_get`,
`agents_md_validate`, `agents_md_evaluate`) over an HTTP+JSON transport,
reusing the REST router from #111 and the AGENTS.md parser/evaluator
from #114. Every tool call traverses the same auth + rate-limit
middleware (new `mcp` surface). MCP protocol version is plumbed via
config; the **decision is deferred** to the July 2026 ADR.

**Architecture:** stdlib `net/http` HTTP transport layered on the
existing `restapi` middleware chain. Stateless session via signed token.
In-process REST loopback via `httptest.NewRecorder` against
`restapi.NewRouter`'s handler. Per-tool dispatch through a `Registry`.

**Tech Stack:** Go 1.22, stdlib `net/http`, pgx/v5, Temporal Go SDK
(client only, no workflow-definition imports), testcontainers-go,
google/uuid.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-112-mcp-server-design.md`

**Branch:** `feat/application-mcp-server` (worktree:
`../gitscale.worktrees/feat-application-mcp-server`)

**ADR-impact:** conforming (ADR-008 outbox; ADR-009 cache; ADR-010
SVID; ADR-012 metering; ADR-017 swap surfaces; ADR-019 plane boundary).
**Open arch question** (MCP protocol version, July 2026) — implementation
plumbs a config field; no ADR proposed by this PR.

**Depends on (merged):** #111 (REST API surface), #114 (AGENTS.md
surfacing + Never enforcement).

---

## File map

### Create

- `plane/application/mcp/doc.go` — package doc, ADR refs, plane
  boundary, deferred-protocol-version note
- `plane/application/mcp/config.go` — `Config{ProtocolVersion, RateConfig, ServerName, ServerVersion}`,
  `DeferredDefaultProtocolVersion` const + `// TODO(adr,jul-2026)`
- `plane/application/mcp/errors.go` — JSON-RPC error code enum, `mapErr`
- `plane/application/mcp/errors_test.go`
- `plane/application/mcp/registry.go` — `Registry`, `ToolHandler`, `RegisterDefaults`
- `plane/application/mcp/registry_test.go`
- `plane/application/mcp/session.go` — `Mint`, `Verify`, signed tuple
- `plane/application/mcp/session_test.go`
- `plane/application/mcp/server.go` — `NewServer(Deps) http.Handler`,
  `initialize` / `tools/list` / `tools/call` dispatch
- `plane/application/mcp/server_test.go`
- `plane/application/mcp/internal/restclient/client.go` — in-process
  loopback against `restapi` handler
- `plane/application/mcp/internal/restclient/client_test.go`
- `plane/application/mcp/tools/git_clone.go` + `_test.go`
- `plane/application/mcp/tools/pr_create.go` + `_test.go`
- `plane/application/mcp/tools/ci_trigger.go` + `_test.go`
- `plane/application/mcp/tools/quota_status.go` + `_test.go`
- `plane/application/mcp/tools/agents_md_get.go` + `_test.go`
- `plane/application/mcp/tools/agents_md_validate.go` + `_test.go`
- `plane/application/mcp/tools/agents_md_evaluate.go` + `_test.go`
- `plane/application/mcp/cirunclient/client.go` — Temporal-backed
  `StartCIRun`; nil-safe stub
- `plane/application/mcp/cirunclient/client_test.go`
- `plane/application/mcp/integration_test.go` — testcontainers Postgres,
  full chain across all 7 tools
- `cmd/mcp-server/main.go` — wires `Deps` from env
- `cmd/mcp-server/integration_test.go` — boots binary, hits
  `/mcp/v1/initialize` + `tools/call(quota_status)`

### Modify

- `plane/data/ratelimit/namespace.go` — add `const SurfaceMCP = "mcp"`
- `plane/data/ratelimit/limiter.go` — extend `RateLimiter` interface
  with `Inspect(ctx, key) (BucketState, error)`; document additive
  swap-surface change (ADR-009 / ADR-017)
- `plane/data/ratelimit/limiter_memory.go` — implement `Inspect`
- `plane/data/ratelimit/limiter_redis.go` — implement `Inspect`
  (new Lua snippet under `plane/data/ratelimit/lua/`)
- `plane/data/ratelimit/limiter_test.go` — both impls covered
- `plane/application/restapi/middleware_ratelimit.go` — honour the
  in-process `X-MCP-Internal` sentinel via context value (set by MCP
  server's `http.Server.ConnContext`); skip double-billing
- `plane/application/restapi/router.go` — `Deps` exported / reused by
  `mcp.NewServer`; document: re-using REST middleware chain is
  intentional (ADR-019)
- `plane/application/identity/service.go` (or wherever the identity
  Service interface lives) — add `MintCloneToken(ctx, principalID,
  repoID) (CloneToken, error)` plus stub + Postgres impl. Outbox event
  `clone_token_minted` written in the same Tx (ADR-008)
- `plane/application/identity/events.go` — register
  `clone_token_minted` event type
- `docs/architecture.md` — none in this PR (no ADR change)

### Untouched (out of scope)

- Workflow-definition packages (Temporal client only)
- `plane/git/...` (AGENTS.md hook handler from #114 already covers
  blob reads)
- `repositories.Service.CreatePullRequest` — `pr_create` returns
  `not_implemented` until that service ships

---

## Pre-flight (do once before Task 1)

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
mkdir -p /home/mitta/clients/gitscale/repos/gitscale.worktrees
git worktree add -b feat/application-mcp-server \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-mcp-server \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-application-mcp-server
git status --porcelain
```

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
go test ./plane/application/restapi/... ./plane/application/agentsmd/... -count=1
```

If anything fails, stop — baseline is broken.

- [ ] **Step P.3: Confirm Go toolchain ≥ 1.22**

```bash
go version
```

- [ ] **Step P.4: Confirm Temporal SDK is already a module dep**

```bash
go list -m go.temporal.io/sdk 2>&1 | head -1
```

If absent, add it; do not pin a beta version.

---

## Task 1 — `ratelimit.RateLimiter.Inspect` (swap surface)

- [ ] **1.1** Define `BucketState{Capacity, Remaining, RefillPerSec float64; Surface string}`.
- [ ] **1.2** Add `Inspect(ctx context.Context, key string) (BucketState, error)` to the interface. Document as additive swap-surface change (ADR-009 / ADR-017): existing callers compile unchanged.
- [ ] **1.3** Implement in `limiter_memory.go` (read snapshot under the same lock as `Take`).
- [ ] **1.4** Implement in `limiter_redis.go` via new Lua script `ratelimit_inspect.lua` under `plane/data/ratelimit/lua/` (returns `{capacity, tokens, refill}` without decrement).
- [ ] **1.5** Add `SurfaceMCP = "mcp"` constant.
- [ ] **1.6** Tests: both impls — capacity, refill rate, post-decrement remaining are visible.
- [ ] **1.7** Acceptance: `go test -race ./plane/data/ratelimit/...` green.

## Task 2 — `identity.Service.MintCloneToken` + outbox

- [ ] **2.1** Add `MintCloneToken(ctx, principalID, repoID uuid.UUID) (CloneToken, error)` to the identity Service interface. `CloneToken{Token string; ExpiresAt time.Time; CloneURL string}`.
- [ ] **2.2** Postgres impl: `BeginTx` → insert `clone_tokens` row (new migration `008_clone_tokens.sql`) → insert `outbox(event_id, type=clone_token_minted, payload={principal_id, repo_id, expires_at, token_id})` → `Commit`. Same Tx (ADR-008). Token TTL 15 min from config.
- [ ] **2.3** Stub impl: in-memory map; same outbox-row stub pattern as existing identity stub.
- [ ] **2.4** Register `clone_token_minted` in `events.go`.
- [ ] **2.5** Tests:
  - Unit: stub returns token + records "outbox" entry.
  - Integration (testcontainers PG): real outbox row visible after commit; `event_id` UUIDv7; payload schema.
- [ ] **2.6** Run `gitscale-outbox-check` skill — single Tx, single ack on commit.
- [ ] **2.7** Run `gitscale-event-schema` — new event type registered.

## Task 3 — `mcp` package skeleton (config, errors, session)

- [ ] **3.1** `doc.go` with ADR refs and **explicit deferred-protocol-version note** (`// MCP protocol version is an open architecture question (target July 2026); see CLAUDE.md.`).
- [ ] **3.2** `config.go`:
  ```go
  type Config struct {
      ProtocolVersion string
      RateConfig      middleware.RateConfig
      ServerName      string
      ServerVersion   string
  }
  // TODO(adr,jul-2026): pin via ADR once protocol-version policy lands.
  const DeferredDefaultProtocolVersion = "<latest-published-draft-at-implement-time>"
  ```
  At boot, log `WARN mcp.protocol_version_deferred version=<v>` if the value equals the default.
- [ ] **3.3** `errors.go` — closed enum:
  ```go
  type Code int
  const (
      CodeMethodNotFound  Code = -32601
      CodeInvalidParams   Code = -32602
      CodeInternal        Code = -32603
      CodeUnauthenticated Code = -32001
      CodeForbidden       Code = -32002
      CodeNotFound        Code = -32003
      CodeNotImplemented  Code = -32004
      CodeConflict        Code = -32005
      CodeRateLimited     Code = -32006
  )
  func mapErr(error) (Code, string)
  ```
  Exhaustive switch over `restapi.ErrorCode`, `identity.Err*`, `agentsmd` errors. `errors_test.go` exhaustive.
- [ ] **3.4** `session.go` — sign `(principal_id, protocol_version, expires_at)` with HMAC keyed by an env-supplied secret (rotated by ops). Verify on every call. `session_test.go`: round-trip, expired, tampered.

## Task 4 — Registry

- [ ] **4.1** `registry.go`:
  ```go
  type ToolHandler func(ctx context.Context, p restapi.Principal, params json.RawMessage) (any, error)
  type Registry struct { tools map[string]Tool }
  type Tool struct { Name, Description string; InputSchema json.RawMessage; Handler ToolHandler }
  func RegisterDefaults(r *Registry, deps Deps)
  ```
- [ ] **4.2** `RegisterDefaults` registers exactly seven names — assert in test.
- [ ] **4.3** Unknown name → `CodeMethodNotFound`.
- [ ] **4.4** Manifest stability test: golden file of `tools/list` JSON.

## Task 5 — In-process REST loopback (`internal/restclient`)

- [ ] **5.1** `client.go`: `Do(req *http.Request) *httptest.ResponseRecorder`. Sets `X-MCP-Internal: 1` header AND attaches a context value `mcpInternalKey{}` via `r = r.WithContext(...)`.
- [ ] **5.2** Modify `restapi/middleware_ratelimit.go` to check the context value (NOT the header alone — header alone is spoofable) and skip the bucket draw if set. Add `restapi/router.go` plumbing so the context value type is exported only via an internal helper (`restapi.WithInternalCall(ctx)`).
- [ ] **5.3** Tests: external HTTP request with `X-MCP-Internal: 1` is **NOT** trusted; only in-process loopback through the helper is.

## Task 6 — HTTP server (`server.go`)

- [ ] **6.1** Reuse `restapi.middleware.RequestID`, `Auth`, `RateLimit(SurfaceMCP)` chain. Document middleware order matches REST: RequestID → Auth → RateLimit → MCP dispatch.
- [ ] **6.2** Routes:
  - `POST /mcp/v1/initialize` — accept client `protocol_version`, return server `protocol_version` (verbatim from config), `capabilities`, `server_info`, `session_id`.
  - `POST /mcp/v1/tools/list` — return registry manifest. Requires valid session.
  - `POST /mcp/v1/tools/call` — body `{name, arguments}` → registry dispatch.
- [ ] **6.3** JSON-RPC envelope: every response carries `jsonrpc=2.0`, `id`. Error path uses `mapErr`.
- [ ] **6.4** Observability: counter `mcp_tool_invoked_total{tool, principal_kind, outcome}`; histogram `mcp_tool_duration_seconds{tool}`. Outcome is closed enum (see spec).
- [ ] **6.5** `server_test.go`:
  - initialize handshake: protocol_version round-trip.
  - tools/list shape.
  - bearer missing → -32001.
  - rate-limited (cap=1, refill=0) → -32006.
  - unknown tool → -32601.

## Task 7 — Tools

For each tool, write the handler + unit test. All handlers receive the
resolved `Principal` from context — never re-parse the bearer.

- [ ] **7.1 `quota_status`** — `ratelimit.Limiter.Inspect(ctx, ratelimit.TokenBucketKey(principal.ID(), "mcp"))` → return `{capacity, remaining, refill_per_sec, surface}`. Test with `MemoryLimiter`.
- [ ] **7.2 `agents_md_validate`** — call `agentsmd.Lint(content)` on caller-supplied bytes. Test all five #114 diagnostic codes surface verbatim.
- [ ] **7.3 `agents_md_evaluate`** — build in-memory `FileResolver` from caller-supplied `changed_paths` + `file_sizes`; call `agentsmd.Evaluate`. Tests cover each predicate kind (force-push, delete, push-to, modify-path, push-binary-over-size).
- [ ] **7.4 `agents_md_get`** — resolve repo via `repositories.Service.GetRepo`; load org policy via `policystore.ResolveOrgPolicy`; load repo `AGENTS.md` via the same `BlobReader` interface used by #114 hook (test impl in-memory). Return `agentsmd.Merge(org, repo)` JSON. Missing files → empty policy.
- [ ] **7.5 `git_clone`** — call `identity.Service.MintCloneToken(ctx, principal.ID(), repoID)`. Validate repo accessibility via `repositories.Service.GetRepo` first. Return `{clone_url, token, expires_at}`. Outbox covered by Task 2. Forbidden if principal lacks access → -32002.
- [ ] **7.6 `pr_create`** — if `Deps.Repositories.CreatePullRequest` is non-nil, dispatch via `internal/restclient.Do(POST /v1/repos/{id}/prs)`; else return `CodeNotImplemented`. Document the not-implemented path in tests AND in `tools/list` description text.
- [ ] **7.7 `ci_trigger`** — call `cirunclient.StartCIRun(ctx, repoID, ref, principal.ID())`. Nil client → `CodeNotImplemented`. Test with stub Temporal client (interface in `cirunclient/client.go`).

## Task 8 — `cirunclient`

- [ ] **8.1** Define typed Temporal client wrapper:
  ```go
  type Client interface {
      StartCIRun(ctx context.Context, repoID uuid.UUID, ref string, principalID uuid.UUID) (RunHandle, error)
  }
  type RunHandle struct { WorkflowID, RunID string }
  ```
- [ ] **8.2** Production impl wraps `client.Client.ExecuteWorkflow` with workflow type name `"CIRunWorkflow"` (registered by workflow plane). **Do not import the workflow-definition package** (ADR-019).
- [ ] **8.3** `Stub` impl returns a fixed handle; `Nil` impl returns `not_implemented`.
- [ ] **8.4** Tests: stub path; nil path returns sentinel that maps to -32004.

## Task 9 — Integration test

- [ ] **9.1** `integration_test.go` boots:
  - testcontainers Postgres, run migrations.
  - real `identity.PostgresService`, `repositories` service, `MemoryLimiter`, in-memory `BlobReader` with seeded AGENTS.md, stub `cirunclient`.
  - real `restapi.NewRouter(...)` as the underlying HTTP handler.
  - real `mcp.NewServer(...)` over an `httptest.Server`.
  - fixed-token `PrincipalResolver` mapping `tok-agent-1` → seeded agent row, `tok-human-1` → seeded user row.
- [ ] **9.2** Cases (one per tool, plus cross-cutting):
  - `initialize` → `tools/list` returns 7 tools, ProtocolVersion = configured value.
  - `quota_status` reflects `mcp` bucket with one prior call counted.
  - `agents_md_validate` returns expected diagnostics.
  - `agents_md_evaluate` predicate-match path.
  - `agents_md_get` returns merged org+repo policy.
  - `git_clone` returns token; `clone_token_minted` outbox row visible in PG.
  - `pr_create` returns `-32004 not_implemented` (or 200 if service wired).
  - `ci_trigger` returns workflow handle from stub.
  - Rate-limit: cap=2, refill=0; third tool call → -32006.
  - Unauthenticated: missing bearer → -32001.
  - Unknown tool name → -32601.

## Task 10 — `cmd/mcp-server`

- [ ] **10.1** `main.go`: parse env (`MCP_LISTEN`, `MCP_PROTOCOL_VERSION`, `MCP_RATELIMIT_AGENT_CAPACITY`, `MCP_RATELIMIT_HUMAN_CAPACITY`, `MCP_SESSION_HMAC_SECRET`, …); construct deps; serve `http.ListenAndServe`.
- [ ] **10.2** Wire production resolver: `identity.Service.LookupIdentityForCache` adapter (same as `cmd/rest-api/main.go`).
- [ ] **10.3** Boot WARN log if `MCP_PROTOCOL_VERSION` defaults to `DeferredDefaultProtocolVersion`.
- [ ] **10.4** `integration_test.go`: build binary, listen on `:0`, POST `/mcp/v1/initialize`, POST `/mcp/v1/tools/call` with `quota_status`, assert 200 + JSON shape.

## Task 11 — ADR + plane-boundary + skill checks

- [ ] **11.1** `gitscale-adr-guard`: confirm no ADR contradiction. Note the deferred protocol-version question in the PR body.
- [ ] **11.2** `gitscale-plane-boundary`: no `plane/git` or `plane/workflow` (definition) imports in `plane/application/mcp/...`. `cirunclient` may import `go.temporal.io/sdk/client` only.
- [ ] **11.3** `gitscale-go-conventions`: stdlib-first, no panics outside `main`, `context.Context` first param.
- [ ] **11.4** `gitscale-outbox-check`: only `git_clone` writes outbox, single-Tx pattern verified.
- [ ] **11.5** `gitscale-agent-quota-check`: every tool name appears under the rate-limit-protected dispatcher; **no path bypasses the limiter**. `quota_status` itself counts.
- [ ] **11.6** `gitscale-event-schema`: `clone_token_minted` registered.

## Task 12 — Self-review battery + PR

- [ ] **12.1** Open follow-up issues (link in PR body):
  - Pin MCP protocol version (post July 2026 ADR).
  - Implement `repositories.Service.CreatePullRequest` to graduate `pr_create` from `not_implemented`.
  - Stdio transport.
  - `resources/*` and `prompts/*` MCP methods.
  - PR dedup (Qdrant) hook on `pr_create` (ADR-016).
- [ ] **12.2** Run pre-push gates:
  ```bash
  go build ./...
  go vet ./...
  golangci-lint run ./...
  go test -race ./plane/application/mcp/... ./cmd/mcp-server/... \
                 ./plane/application/restapi/... ./plane/data/ratelimit/... \
                 ./plane/application/identity/... ./plane/application/agentsmd/...
  make lint-events
  make lint-determinism
  ```
- [ ] **12.3** Dispatch self-review battery in parallel: `pr-review-toolkit:code-reviewer`, `pr-review-toolkit:silent-failure-hunter`, `adr-historian`, `pr-review-toolkit:type-design-analyzer` (new public types in `mcp/`, `cirunclient/`, `ratelimit.BucketState`), `pr-review-toolkit:pr-test-analyzer`. Resolve findings.
- [ ] **12.4** Commit using Conventional Commits: `feat(application): MCP server — git_clone, pr_create, ci_trigger, quota_status, agents_md_* (#112)`.
- [ ] **12.5** `gh pr create` with title `[Application] MCP server — git_clone, pr_create, ci_trigger, quota_status, agents_md_*`. Body sections: summary, ADR conformance (with deferred-protocol-version callout), `Closes #112`, follow-up issue links, self-review block, co-author trailer.

---

## Acceptance criteria (mirror issue body)

- [ ] All seven MCP tools register and dispatch through the registry.
- [ ] `tools/list` returns exactly the seven tool names with stable
      `inputSchema` JSON.
- [ ] Auth middleware rejects missing/invalid bearer with JSON-RPC
      `-32001` and stable error envelope.
- [ ] Rate-limit middleware on the `mcp` surface returns `-32006` when
      bucket exhausted; `quota_status` reports the bucket state.
- [ ] `agents_md_get` returns merged (org + repo) policy via the #114
      parser; missing files → empty policy without error.
- [ ] `agents_md_validate` surfaces all five #114 diagnostic codes.
- [ ] `agents_md_evaluate` runs the Never evaluator on caller-supplied
      input and returns violations without touching Gitaly.
- [ ] `git_clone` mints a 15-minute clone token; `clone_token_minted`
      outbox row visible in same Tx (ADR-008).
- [ ] `pr_create` returns `-32004 not_implemented` until the underlying
      `CreatePullRequest` service ships (follow-up issue tracked).
- [ ] `ci_trigger` returns a workflow handle via `cirunclient`; nil
      client returns `-32004`.
- [ ] Integration tests run against testcontainers PG; no mock-DB tests
      in `plane/application/mcp/...`.
- [ ] `make test` and the pre-push gates list all green.
- [ ] Self-review battery clean.
- [ ] PR closes #112; follow-up issues linked; deferred-protocol-version
      decision flagged in PR body.
