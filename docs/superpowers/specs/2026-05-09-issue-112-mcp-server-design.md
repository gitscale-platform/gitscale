# Spec — Issue #112 MCP server (`git_clone`, `pr_create`, `ci_trigger`, `quota_status`, `agents_md_*`)

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/112
Plane: application
Priority: p1 (Wave 1)
ADR-impact: conforming (ADR-008 outbox; ADR-009 cache; ADR-010 SVID; ADR-012 two-tier metering; ADR-017 swap surfaces; ADR-019 plane boundary)
Open arch question: **MCP protocol version policy (July 2026)** — see "Protocol version" below.
Depends on: #111 (REST API surface) merged d89a628; #114 (AGENTS.md surfacing + Never enforcement) merged de282fe.

## Problem

The application plane exposes identity + repository state via gRPC (#15)
and HTTP/JSON (#111). Agents — the primary traffic class — need a
**Model Context Protocol** surface that is callable from Claude, Cursor,
ChatGPT, Continue, etc. without each client reimplementing the GitScale
API. Issue #112 ships an MCP server hosted by the application plane that
exposes seven canonical tools backed by the existing REST handlers and
the AGENTS.md API from #114.

The MCP protocol version is an **open architecture question** until
July 2026 (per `CLAUDE.md`). This spec ships the tools without pinning
a protocol version into code: a single `ProtocolVersion` config field is
plumbed end-to-end and surfaced in the MCP `initialize` handshake so the
choice can be flipped without code change once the question resolves.

## Goals

1. Add `plane/application/mcp` package owning the MCP transport, session
   lifecycle, tool registry, and per-tool handlers. No duplication of
   business logic — every tool calls the in-process REST handler chain
   from `plane/application/restapi` (or, where appropriate, the
   underlying `identity.Service` / `agentsmd` / `ratelimit` packages
   directly via the same `Deps` struct), going through the same auth +
   rate-limit middleware.
2. Expose the seven tools required by the issue:
   - `git_clone` — returns clone URL + short-lived credential for a
     `repo_id` the principal can access.
   - `pr_create` — creates a pull request (or its v1 stub if
     pull-request domain is not yet wired) with title / body / source
     ref / target ref.
   - `ci_trigger` — enqueues a CI run for a `repo_id` + ref via
     workflow plane (Temporal `StartWorkflow` from a thin app-plane
     client; no in-process workflow code).
   - `quota_status` — returns the principal's current bucket state
     (`capacity`, `remaining`, `refill_per_sec`, `surface`) by reading
     from `ratelimit.RateLimiter`.
   - `agents_md_get` — returns the merged (org + repo) AGENTS.md policy
     for a `repo_id` via `plane/application/agentsmd` parser +
     `policystore.ResolveOrgPolicy`.
   - `agents_md_validate` — lints raw bytes via `agentsmd.Lint`. Stable
     `Diagnostic.Code` set from #114.
   - `agents_md_evaluate` — runs the `Never` evaluator over a synthetic
     `EvaluationInput` supplied by the agent (dry-run "would this push
     be rejected").
3. Plumb a `ProtocolVersion string` config field; default to the latest
   published draft known at draft time but emit a `WARN` log on boot
   stating the choice is **deferred pending the July 2026 decision**.
4. Every tool invocation is metered: each call decrements the
   per-principal token bucket on a new `mcp` surface (separate from
   `rest_api`), and emits `mcp_tool_invoked_total{tool, principal_kind, outcome}`
   metric. **No tool bypasses the limiter** — `gitscale-agent-quota-check`
   skill must pass.
5. Authentication is via a JWT-SVID bearer (ADR-010) carried on the
   transport (HTTP transport — see "Transport" below), resolved through
   the same `restapi.PrincipalResolver` interface as #111 — **no new
   identity path**.
6. Integration tests cover all seven tools end-to-end through MCP
   transport against testcontainers Postgres + the in-memory rate
   limiter, mirroring `plane/application/restapi/integration_test.go`.
7. New binary `cmd/mcp-server` boots the registry against the real
   services and listens on a configurable port.

## Non-goals

- **MCP server protocol version pinning** — deferred (July 2026). We
  ship a configurable field; we do **not** decide.
- **Streaming tool results** — `pr_create` and `ci_trigger` return
  immediately with an opaque task handle; long-poll / streaming
  follow-up is out of scope.
- **Resource subscriptions / prompts** — only the `tools/*` MCP methods
  are implemented in this issue. `resources/*` and `prompts/*` are
  follow-ups.
- **Cross-org PR dedup hook** — `pr_create` does not call the Qdrant
  pipeline (ADR-016) in this PR; that wiring is its own issue.
- **Workflow plane code** — `ci_trigger` calls Temporal via the
  application-plane workflow client wrapper; no workflow-definition
  code lives in this package.
- **MCP server auth flow other than bearer JWT-SVID** — OAuth2 device
  flow is a follow-up.
- **stdio transport** — HTTP-only in v1. Stdio (single-tenant local
  agents) is deferred.

## Design decisions (defaults selected by supervisor — auto-mode)

| Question | Choice | Rationale |
|---|---|---|
| Transport | **HTTP+JSON** with one POST endpoint per MCP method (`POST /mcp/v1/initialize`, `POST /mcp/v1/tools/list`, `POST /mcp/v1/tools/call`). No SSE or websockets in v1. | Matches the existing REST infrastructure (auth, rate-limit, request-id middleware reused). Stdio + SSE deferred. |
| Protocol version handling | `Config.ProtocolVersion string` (default e.g. `"2025-06-18"` — replace with the latest draft string at implement-time and tag with `// TODO(adr,jul-2026): pin via ADR`). Returned verbatim from `initialize`. Mismatch with client request logged but **not** rejected; the server replies with the configured version. | Keeps the unresolved arch question out of the code path. Lenient negotiation lets the protocol-version ADR flip behaviour later without client breakage. |
| Tool registry shape | A single `Registry` map `map[string]ToolHandler` keyed by tool name, populated at boot. Each `ToolHandler` is `func(ctx, *restapi.Principal, json.RawMessage) (any, error)`. Closed enum of names; unknown tool → MCP error `-32601 Method not found`. | Eliminates dispatch ambiguity; keeps adding a tool to a single registration site. |
| In-process REST reuse | Tools that map 1:1 to a REST handler (`git_clone` → `GET /v1/repos/{id}/clone`, `pr_create` → `POST /v1/repos/{id}/prs`, etc.) **call the REST handler via an internal in-process client** that builds an `httptest.NewRecorder` against the same `http.Handler` returned by `restapi.NewRouter`. No HTTP loopback. | Single source of truth for auth + rate-limit + error-mapping; satisfies "do not duplicate handlers" constraint from issue. Internal client is a thin adapter not exposed outside the mcp package. |
| Auth | Reuse `restapi.PrincipalResolver` via a constructor `Deps.Resolver`. MCP `initialize` extracts bearer from the `Authorization` header, resolves principal once, stores it in session. Subsequent `tools/call` requests on the same session reuse the principal **but** re-validate the bearer (cheap cache hit) on each call to support revocation. | ADR-010 wants per-request verification; cache-aware resolver in #111 already absorbs the cost. |
| Rate-limit surface | New surface enum `"mcp"` registered in `plane/data/ratelimit/namespace.go`. Per-principal bucket. Capacity/refill from config (default `agent_capacity=200/s`, `human_capacity=20/s` matching REST). | Separate surface keeps MCP traffic from starving REST clients. |
| `quota_status` semantics | Reads from the `ratelimit` `mcp` surface for the calling principal: returns capacity, remaining (current bucket level), refill rate, surface name. **Does not** decrement the bucket itself (MCP-side; we still meter the `quota_status` call against the bucket like every other tool — bucket capacity is large enough to absorb status polls). | Exposes the limiter contract honestly; agents need to budget. |
| `agents_md_evaluate` input shape | Accepts `{repo_id, ref_updates: [{ref_name, old_oid, new_oid}], changed_paths, principal_id}` — the same shape as `agentsmd.EvaluationInput` but with the `FileResolver` replaced by an explicit `changed_paths []string` and `file_sizes map[string]int64` map supplied by the caller. The server constructs an in-memory `FileResolver` from those maps. | Lets agents do hypothetical evaluation without touching Gitaly; production push enforcement still uses the Gitaly-backed resolver from #114. Keeps the evaluator dependency-free in the MCP path. |
| `git_clone` credential | Mints a short-lived (15-min TTL) clone token via `identity.Service.MintCloneToken(ctx, principalID, repoID)`. **If that method does not yet exist on the service, this PR adds it as a thin wrapper around an existing token-mint pathway**. The token is opaque, scoped to the repo, and recorded in the outbox as `clone_token_minted` with `(principal_id, repo_id, expires_at)` so audit and revocation work. | Outbox-or-it-didn't-happen (ADR-008). Token TTL keeps the blast radius bounded. |
| `pr_create` backing | Calls existing `repositories.Service.CreatePullRequest(ctx, principal, ...)` if available. If not, this PR adds a minimal `pull_requests` row + `pull_request_created` outbox event in the same Tx, gated by a feature flag — **but** the canonical path is to add the missing route/method in a follow-up; the MCP tool then degrades gracefully to `code=not_implemented` (`-32004 Tool unavailable`) until the underlying method exists. **Default behaviour: ship MCP tool returning `not_implemented` if no PR service exists yet**; do not invent the schema in this issue. | Avoids schema work outside #112's scope; surfaces the tool to clients with a clear "coming soon" signal so contracts settle early. |
| `ci_trigger` backing | Thin wrapper that calls `workflow.Client.StartCIRun(ctx, repoID, ref, principalID)`. The wrapper is implemented in `plane/application/mcp/cirunclient` as a typed Temporal client (no workflow-definition imports). If the Temporal client is unconfigured, returns `not_implemented`. | Plane boundary (ADR-019): MCP does not import workflow-definition packages, only the typed client. |
| Tool error mapping | Map app-plane errors to MCP JSON-RPC error codes: `unauthenticated → -32001`, `forbidden → -32002`, `not_found → -32003`, `not_implemented → -32004`, `validation_failed → -32602` (Invalid params), `conflict → -32005`, `rate_limited → -32006`, `internal → -32603`. Stable, closed enum. | Closed enum mirrors the REST envelope codes from #111 and lines up with JSON-RPC 2.0 reserved space (server errors `-32000…-32099`). |
| Session model | Stateless — no server-side session table. `initialize` returns a session_id that is a serialised `(principal_id, capabilities, protocol_version, expires_at)` tuple signed by the same JWT-SVID key material used by the resolver. Subsequent calls validate that session token. | Aligns with stateless-handler invariant for application plane (no in-process session memory; ADR-008-adjacent loose-coupling). |
| Logging / observability | One log line per tool call: `mcp_tool_call{tool, principal_id, outcome, duration_ms}`. One Prometheus counter `mcp_tool_invoked_total{tool, principal_kind, outcome}`, one histogram `mcp_tool_duration_seconds{tool}`. Surface `mcp` added to the existing rate-limit metric label set. | Mirrors REST observability shape. |
| Testing backend | Same as #111: `httptest.Server` over the MCP handler, real `restapi.NewRouter` underneath, `MemoryLimiter`, fixed-token resolver, real `agentsmd` parser. Integration test runs full chain against testcontainers Postgres. **No mock-DB tests in `mcp` package.** | Plane invariant. |

## Architecture

### Package layout

```
plane/application/mcp/
  doc.go                       package doc, ADR refs, plane boundary, deferred protocol version note
  config.go                    Config{ProtocolVersion, RateConfig, ServerName, ServerVersion}
  errors.go                    MCP JSON-RPC error code enum + mapErr from app-plane sentinels
  errors_test.go
  registry.go                  Registry, ToolHandler, RegisterDefaults
  registry_test.go
  session.go                   Stateless session token mint/verify
  session_test.go
  server.go                    HTTP handler for /mcp/v1/initialize, /tools/list, /tools/call; reuses restapi middleware chain
  server_test.go               full chain via httptest, MemoryLimiter, StubResolver
  internal/restclient/         in-process http.Handler client (httptest.NewRecorder against restapi router)
    client.go
    client_test.go
  tools/
    git_clone.go               + _test
    pr_create.go               + _test
    ci_trigger.go              + _test
    quota_status.go            + _test
    agents_md_get.go           + _test
    agents_md_validate.go      + _test
    agents_md_evaluate.go      + _test
  cirunclient/
    client.go                  Temporal-backed StartCIRun; nil-safe stub for tests
    client_test.go
  integration_test.go          testcontainers PG, real restapi.NewRouter, exercises all 7 tools
cmd/mcp-server/
  main.go                      wires Deps from env, listens
  integration_test.go          boots binary, hits /mcp/v1/initialize + one tools/call

plane/data/ratelimit/
  namespace.go                 ADD: const SurfaceMCP = "mcp"
```

### Request flow

```
HTTP request → restapi.middleware.RequestID
            → restapi.middleware.Auth(resolver)             # principal resolved, JWT-SVID validated
            → restapi.middleware.RateLimit(limiter, "mcp")  # MCP surface bucket
            → mcp.server.dispatch(method)
               ├─ initialize   → return {protocol_version, capabilities, server_info, session_id}
               ├─ tools/list   → return registry manifest
               └─ tools/call   → registry[name](ctx, principal, params)
                                  └─ restclient.Do(req) for REST-backed tools
                                     OR direct call into agentsmd / ratelimit / cirunclient
```

### Tool manifest (returned by `tools/list`)

```json
{
  "tools": [
    {"name": "git_clone",         "description": "Return clone URL + short-lived credential", "inputSchema": {...}},
    {"name": "pr_create",         "description": "Create a pull request", "inputSchema": {...}},
    {"name": "ci_trigger",        "description": "Trigger a CI run", "inputSchema": {...}},
    {"name": "quota_status",      "description": "Return the calling principal's MCP rate-limit bucket state", "inputSchema": {...}},
    {"name": "agents_md_get",     "description": "Return merged AGENTS.md policy for a repo", "inputSchema": {...}},
    {"name": "agents_md_validate","description": "Lint AGENTS.md content", "inputSchema": {...}},
    {"name": "agents_md_evaluate","description": "Dry-run Never evaluator", "inputSchema": {...}}
  ]
}
```

`inputSchema` is a JSON-Schema fragment; per-tool schemas live next to
each tool's source file as a Go-embedded JSON literal.

### Protocol-version plumbing

```go
// plane/application/mcp/config.go

type Config struct {
    // ProtocolVersion is the MCP protocol version returned by initialize.
    //
    // Deferred decision: GitScale's MCP protocol-version policy is an
    // open architecture question (target July 2026). Until then, this
    // field is set from env (MCP_PROTOCOL_VERSION) with a default of the
    // latest published draft at implement time. A WARN log is emitted at
    // boot if the value is the deferred default.
    ProtocolVersion string

    RateConfig    middleware.RateConfig
    ServerName    string
    ServerVersion string
}

const DeferredDefaultProtocolVersion = "2025-06-18" // pin at implement time; do not commit a year-old default
```

`server.go` `initialize` handler:

```go
return InitializeResult{
    ProtocolVersion: cfg.ProtocolVersion,           // verbatim
    Capabilities:    Capabilities{Tools: ToolsCap{ListChanged: false}},
    ServerInfo:      ServerInfo{Name: cfg.ServerName, Version: cfg.ServerVersion},
    SessionID:       session.Mint(principal, cfg.ProtocolVersion, ttl),
}, nil
```

### REST-backed tool dispatch (`internal/restclient`)

```go
// internal/restclient/client.go
type Client struct {
    handler http.Handler   // the *http.ServeMux returned by restapi.NewRouter
}

// Do executes the request against the in-process REST handler and
// returns the recorded response. Identity propagates via Authorization
// header derived from the principal token; ratelimit at the REST layer
// is bypassed by setting a sentinel header X-MCP-Internal that the REST
// rate-limit middleware short-circuits on (so we do not double-bill the
// principal — MCP surface bucket already counted).
func (c *Client) Do(req *http.Request) *httptest.ResponseRecorder
```

The X-MCP-Internal sentinel is validated by checking that the request
arrived via the in-process loopback only (server.go zeroes the header
on any inbound request, then sets it before forwarding to restclient).
Documented as a plane-internal mechanism; **not** a public contract.

### Per-tool wiring summary

| Tool | Backing call | Mutates? | Outbox event |
|---|---|---|---|
| `git_clone` | `identity.Service.MintCloneToken` (new method, thin wrapper) | yes | `clone_token_minted` |
| `pr_create` | `repositories.Service.CreatePullRequest` if exists, else `not_implemented` | yes (when impl) | `pull_request_created` (when impl) |
| `ci_trigger` | `cirunclient.StartCIRun` (Temporal client wrapper) | yes (workflow start) | none in app plane (workflow plane owns its outbox) |
| `quota_status` | `ratelimit.RateLimiter.Inspect(ctx, key)` (new read-only method on the interface; in-memory + Redis impls add it) | no | none |
| `agents_md_get` | `agentsmd.Parse` over blob from `policystore.ResolveOrgPolicy` + repo `AGENTS.md` blob via `BlobReader` (#114) | no | none |
| `agents_md_validate` | `agentsmd.Lint` over caller-supplied bytes | no | none |
| `agents_md_evaluate` | `agentsmd.Evaluate` with caller-supplied `FileResolver` (in-memory) | no | none |

### Outbox impact (ADR-008)

- `git_clone` adds a `clone_token_minted` outbox event in the same Tx
  as the `clone_tokens` row insert (or whichever underlying mutation
  the new `MintCloneToken` performs).
- `pr_create` writes `pull_request_created` only if the underlying
  `repositories.Service.CreatePullRequest` is wired in this PR;
  otherwise the tool returns `not_implemented` and **writes no outbox
  rows**. (Decision: do not invent the PR schema in #112; that lives in
  its own issue.)
- `ci_trigger` does not write an outbox row in the application plane;
  the workflow plane owns the CI-run lifecycle outbox (ADR-019).
- All read-only tools write no outbox rows.

`gitscale-outbox-check` skill: applicable only to `git_clone` (and to
`pr_create` if the PR service ships with this issue).

### Plane boundary (ADR-019)

| MCP imports | Allowed |
|---|---|
| `plane/application/restapi` | yes — middleware + router for in-process dispatch |
| `plane/application/identity` | yes — `Service.MintCloneToken` |
| `plane/application/agentsmd` | yes — parser, evaluator, lint |
| `plane/application/agentsmd/policystore` | yes — org-policy resolution |
| `plane/data/ratelimit` | yes — `RateLimiter.Inspect`, surface const |
| `plane/data/store/...` | yes — only via the application services above |
| `plane/git/...` | **no** — file-changed list and blob reads go through agentsmd's `BlobReader` interface, which #114 already routes to Gitaly |
| `plane/workflow/...` | **no** — `cirunclient` uses the Temporal SDK client only, not workflow-definition packages |
| `plane/edge/...` | **no** |

`gitscale-plane-boundary` skill must pass.

### Identity / SVID (ADR-010)

Auth middleware reuses `restapi.PrincipalResolver`; resolver impl in
`cmd/mcp-server/main.go` wraps `identity.Service.LookupIdentityForCache`
exactly as `cmd/rest-api/main.go` does. JWT-SVID is validated at edge in
production; MCP server re-validates principal claims on every call (the
resolver caches; expiry is checked on each call to support revocation).

### Quota / metering (ADR-012)

| Concern | Implementation |
|---|---|
| Per-call metering | `restapi.middleware.RateLimit` with `surface=mcp`, runs **before** dispatch. No tool can bypass. |
| Per-call accounting | One `mcp_tool_invoked_total{tool, principal_kind, outcome}` increment after dispatch. `outcome ∈ {ok, error, rate_limited, unauthenticated, forbidden, not_found, validation_failed, conflict, internal, not_implemented}` — closed set. |
| `quota_status` reflects MCP surface | Yes — reads `ratelimit:mcp:<principal_id>` key. |
| Two-tier metering | Edge pre-meters at the WASM filter; MCP server post-meters (token-bucket draw). Both are recorded; analytics reconcile (pattern from #121 / ADR-015). |
| `gitscale-agent-quota-check` skill | Must pass. Every tool dispatched only after the rate-limit middleware succeeded. |

## Error envelope

MCP JSON-RPC error response shape:

```json
{ "jsonrpc": "2.0",
  "id": "<request-id>",
  "error": {
    "code": -32002,
    "message": "forbidden: agent cannot revoke another agent",
    "data": { "request_id": "01JKW...", "tool": "pr_create" }
  }
}
```

Closed enum (`errors.go`):

| Sentinel | JSON-RPC code |
|---|---|
| unauthenticated | -32001 |
| forbidden       | -32002 |
| not_found       | -32003 |
| not_implemented | -32004 |
| conflict        | -32005 |
| rate_limited    | -32006 |
| validation_failed | -32602 (Invalid params) |
| method not found | -32601 |
| internal        | -32603 |

## Testing strategy

| Test file | Backend | Coverage |
|---|---|---|
| `mcp/registry_test.go` | none | Tool name uniqueness; manifest stability; unknown name → -32601. |
| `mcp/session_test.go` | none | Mint → Verify round-trip; expiry; tampered token → reject. |
| `mcp/errors_test.go` | none | Exhaustive sentinel → JSON-RPC code mapping. |
| `mcp/server_test.go` | httptest | initialize handshake; tools/list shape; bearer missing → -32001; rate-limited (cap=1, refill=0) → -32006. |
| `mcp/tools/git_clone_test.go` | StubIdentity | Token mint path returns clone URL + token; outbox row asserted via stub. |
| `mcp/tools/pr_create_test.go` | StubRepositories (returns ErrNotImplemented when service nil) | not_implemented path; impl path if wired. |
| `mcp/tools/ci_trigger_test.go` | Stub `cirunclient` | Workflow ID returned; nil client → not_implemented. |
| `mcp/tools/quota_status_test.go` | MemoryLimiter | Returns inspected bucket state. |
| `mcp/tools/agents_md_get_test.go` | in-memory BlobReader + StubPolicyStore | merged policy returned; missing AGENTS.md → empty policy. |
| `mcp/tools/agents_md_validate_test.go` | none | All five #114 diagnostic codes surfaced. |
| `mcp/tools/agents_md_evaluate_test.go` | in-memory FileResolver | Each predicate kind reachable; no-violation case clean. |
| `mcp/integration_test.go` | testcontainers Postgres | Full server boot; one call per tool through MCP transport; bucket draw observed; outbox row visible for `git_clone`. |
| `cmd/mcp-server/integration_test.go` | binary | initialize → tools/list → tools/call(quota_status) → 200. |

GA gate: `make test` passes; no mock-DB tests in `plane/application/mcp`.

## Risks / unknowns

- **MCP protocol version**: deferred (July 2026). Risk: clients pinned
  to a different draft fail handshake. Mitigation: server is lenient on
  `initialize` (returns its configured version regardless of client
  request); operator can override via env without redeploy.
- **`pr_create` shipping as `not_implemented`**: cosmetic regression
  vs. agents who expect a working tool. Mitigation: documented in
  `tools/list` description; follow-up issue tracked.
- **`ratelimit.RateLimiter.Inspect` is a new method on the interface**
  (ADR-009 / ADR-017 swap surface). Both impls (`limiter_memory.go`,
  `limiter_redis.go`) must add it; the existing `MemoryLimiter` test
  matrix extends. Adding to the interface is a swap-surface change but
  not a breaking one for callers (additive).
- **In-process REST loopback via `httptest.NewRecorder`**: not the
  standard pattern; risk that handler-level assumptions about the
  request lifecycle (e.g. `Hijacker`) leak. Mitigation: tools call only
  JSON endpoints; no streaming/upgrade endpoints reachable from MCP.
- **`X-MCP-Internal` sentinel** to skip double-billing at REST layer:
  bug-class is "external client spoofs the header". Mitigation: REST
  middleware checks the connection's local-loopback property (the MCP
  server attaches a context value via `http.Server.ConnContext`; the
  REST middleware only honours the sentinel when that context value is
  set — server.go zeroes it on inbound, sets it for restclient calls).

## Out of scope follow-ups (file as new issues)

- Pin the MCP protocol version in an ADR after the July 2026 decision.
- Implement `repositories.Service.CreatePullRequest` so `pr_create`
  graduates from `not_implemented`.
- Stdio transport for single-tenant local agents.
- `resources/*` and `prompts/*` MCP methods.
- Cross-org PR dedup hook on `pr_create` (ADR-016 Qdrant pipeline).
- OAuth2 device-flow auth for human MCP clients.

## Cross-references

- Issue: https://github.com/gitscale-platform/gitscale/issues/112
- Spec (REST API #111): `docs/superpowers/specs/2026-05-09-issue-111-rest-api-http-layer-design.md`
- Plan (REST API #111): `docs/superpowers/plans/2026-05-09-issue-111-rest-api-http-layer-plan.md`
- Spec (AGENTS.md #114): `docs/superpowers/specs/2026-05-09-issue-114-agents-md-never-enforcement-design.md`
- Plan (AGENTS.md #114): `docs/superpowers/plans/2026-05-09-issue-114-agents-md-never-enforcement-plan.md`
- ADR-008 outbox; ADR-009 cache; ADR-010 SVID; ADR-012 two-tier metering;
  ADR-017 swap surfaces; ADR-019 plane boundary — `docs/architecture.md §8`
- Open arch question (MCP protocol version): `CLAUDE.md` §"Open architecture questions"
