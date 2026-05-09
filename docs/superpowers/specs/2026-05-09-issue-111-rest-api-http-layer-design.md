# Spec — Issue #111 REST API HTTP layer (identity + repository)

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/111
Plane: application
Priority: p1 (Wave 0)
ADR-impact: conforming (ADR-017 GraphQL/REST cost analysis; ADR-008 outbox; ADR-019 plane boundary)

## Problem

The application plane already exposes the identity domain over gRPC
(`plane/application/identity/grpc_server.go`) and has functioning
`Service` and `RepositoryReader` / `RepositoryWriter` interfaces in the
metadata store. There is no HTTP/JSON edge for tooling, MCP server (#112),
GraphQL resolvers (#113), or CI runners to call. Phase 2 cannot land
until REST is the externally callable surface.

## Goals

1. Add a `plane/application/restapi` package owning the HTTP router,
   middleware, and per-domain handlers, mounted at `/v1/`.
2. Identity endpoints — `POST /v1/users`, `GET /v1/users/{id}`,
   `POST /v1/agents`, `GET /v1/agents/{id}`, `DELETE /v1/agents/{id}`,
   `PATCH /v1/agents/{id}/permissions`.
3. Repository endpoints — `POST /v1/repos`, `GET /v1/repos/{id}`,
   `DELETE /v1/repos/{id}`, `GET /v1/orgs/{org}/repos`.
4. Auth middleware that resolves `Principal` (`HumanUser` |
   `AgentIdentity`) from a bearer token via the identity service and
   injects it into the request context.
5. Rate-limit middleware enforcing a per-principal token-bucket via
   `plane/data/ratelimit.RateLimiter`; `429` includes `Retry-After`.
6. JSON error envelope with a closed enum of stable codes.
7. New binary `cmd/rest-api` boots the router against the real services.
8. Integration tests use testcontainers Postgres + `httptest.Server`,
   mirroring `plane/application/identity/integration_test.go`. No mock-DB
   tests in the new package.

## Non-goals

- GraphQL surface — issue #113 (Wave 1).
- MCP server — issue #112 (Wave 1).
- Webhook delivery endpoints — out of scope for this issue.
- CI / pipeline endpoints — out of scope; CI runner integration in #110.
- Repository state mutation beyond `Insert` / `Delete` — `Update*` apart
  from `UpdatePermissions` (already on the writer) is out of scope.
- Cross-plane Gitaly RPC — repo creation here only writes the metadata
  row; physical Git repo provisioning is the git plane's job (#107).
- Pagination over agents-by-parent-user (small bounded set; simple list).

## Design decisions (defaults selected by supervisor)

| Question | Choice | Rationale |
|---|---|---|
| HTTP router | **stdlib `net/http` `ServeMux`** (Go 1.22 enhanced patterns: `GET /v1/users/{id}`) | Zero-dep, sufficient for path params, matches go-conventions skill (avoid third-party where stdlib suffices). |
| Principal contract | `Principal` interface with `Kind() PrincipalKind`, `ID() uuid.UUID`; concrete `HumanPrincipal{*HumanUser}` and `AgentPrincipal{*AgentIdentity}`. Context key is unexported typed sentinel. | Closed sum-type via interface; type-switch at handler call site is explicit and lintable. |
| Rate-limit scope | Per-principal token-bucket on a single `rest_api` surface enum. Capacity / refill from config. | Simplest correct enforcement; per-route bucketing deferred until measured demand. The MCP server (#112) and GraphQL (#113) will introduce their own surfaces. |
| Error envelope codes | Closed enum: `unauthenticated`, `forbidden`, `not_found`, `validation_failed`, `conflict`, `rate_limited`, `internal`. Mapped from service sentinel errors via `mapErr` in the package. | Stable contract for clients; exhaustive switch on the server side. |
| Integration test boundary | `httptest.Server` over the real handler, real `PostgresService` from testcontainers Postgres, real `MemoryLimiter`. Auth bypass for tests via injected `PrincipalResolver` interface returning a fixed principal for a known token. | Mirrors identity package pattern; no gRPC bufconn here because the REST layer talks to the in-process `identity.Service`, not over gRPC. |
| Pagination | Opaque base64-encoded cursor (`{"after_id": "<uuid>", "after_created_at": "<rfc3339>"}`) with `limit` query param (default 20, max 100). Used only on `GET /v1/orgs/{org}/repos`. | Stable across schema changes; future-compatible with keyset pagination on (created_at, id). |

## Architecture

### Package layout

```
plane/application/restapi/
  doc.go                    package doc, ADR refs, plane boundary
  errors.go                 ErrorCode enum, JSON envelope, mapErr
  principal.go              Principal interface, context helpers
  middleware/
    auth.go                 PrincipalResolver, AuthMiddleware
    auth_test.go
    ratelimit.go            RateLimitMiddleware
    ratelimit_test.go
    request_id.go           inject X-Request-Id (correlate w/ structured logs)
  identity_handlers.go      handlers backed by identity.Service
  identity_handlers_test.go (httptest, MemoryLimiter, Stub identity)
  repos_handlers.go         handlers backed by store.RepositoryWriter via app service
  repos_handlers_test.go
  router.go                 NewRouter(deps) *http.ServeMux + chain composer
  pagination.go             cursor encode/decode helpers
  pagination_test.go
  integration_test.go       testcontainers PG + real PostgresService
cmd/rest-api/
  main.go                   wires ratelimit, identity service, store, listener
  integration_test.go       boots binary, http.Get on /healthz + /v1/users
```

### Router composition

```go
func NewRouter(deps Deps) http.Handler {
    mux := http.NewServeMux()
    // identity
    mux.HandleFunc("POST /v1/users",             h.createUser)
    mux.HandleFunc("GET /v1/users/{id}",         h.getUser)
    mux.HandleFunc("POST /v1/agents",            h.createAgent)
    mux.HandleFunc("GET /v1/agents/{id}",        h.getAgent)
    mux.HandleFunc("DELETE /v1/agents/{id}",     h.revokeAgent)
    mux.HandleFunc("PATCH /v1/agents/{id}/permissions", h.updatePerms)
    // repos
    mux.HandleFunc("POST /v1/repos",             h.createRepo)
    mux.HandleFunc("GET /v1/repos/{id}",         h.getRepo)
    mux.HandleFunc("DELETE /v1/repos/{id}",      h.deleteRepo)
    mux.HandleFunc("GET /v1/orgs/{org}/repos",   h.listOrgRepos)
    // health
    mux.HandleFunc("GET /healthz",               h.health)

    return chain(mux,
        middleware.RequestID,
        middleware.Auth(deps.Resolver, h.unauthenticated),
        middleware.RateLimit(deps.Limiter, h.rateLimited, deps.RateConfig),
    )
}
```

`/healthz` skips auth and rate-limit (mux-level branch in middleware).

### Principal + auth

```go
type PrincipalKind int
const ( PrincipalUnknown PrincipalKind = iota; PrincipalHuman; PrincipalAgent )

type Principal interface {
    Kind() PrincipalKind
    ID()   uuid.UUID
}

type PrincipalResolver interface {
    Resolve(ctx context.Context, bearer string) (Principal, error)
}
```

Production resolver wraps `identity.Service.LookupIdentityForCache` plus
a cache.IdentityCacheEntry → Principal adapter.

### Error envelope

```json
{ "error": { "code": "validation_failed",
             "message": "email: missing @",
             "request_id": "01JKW..." } }
```

`code` ∈ closed enum above. Mapping table from `identity.Err*` to code is
exhaustive and unit-tested.

### Rate-limit

```go
key := fmt.Sprintf(ratelimit.TokenBucketKey, principal.ID(), "rest_api")
granted, remaining, err := limiter.Take(ctx, key, capacity, refillPerSec, 1)
if !granted {
    w.Header().Set("Retry-After", retryAfter(refillPerSec))
    w.Header().Set("X-RateLimit-Remaining", strconv.FormatFloat(remaining, 'f', 0, 64))
    writeError(w, http.StatusTooManyRequests, ErrRateLimited, "")
    return
}
```

Capacity / refill come from config keyed on principal kind:
`agent_capacity=200/s`, `human_capacity=20/s`, configurable via env.

### Pagination

```go
type cursor struct {
    AfterID        uuid.UUID `json:"after_id"`
    AfterCreatedAt time.Time `json:"after_created_at"`
}

func encodeCursor(c cursor) string  // base64(json)
func decodeCursor(s string) (cursor, error)
```

Response includes `next_cursor` (omitted when no further rows). Backed
by a new repository reader method `ListByOrg(ctx, orgID, after Cursor,
limit int) ([]Repository, error)` added to `RepositoryReader` (extending
the interface in `plane/data/store/metadata.go` and both impls).

### Service-error mapping

| Sentinel | HTTP | Code |
|---|---|---|
| `identity.ErrInvalidEmail` | 400 | `validation_failed` |
| `identity.ErrEmptyDisplayName` | 400 | `validation_failed` |
| `identity.ErrAgentNotFound` | 404 | `not_found` |
| `identity.ErrUserNotFound` | 404 | `not_found` |
| `identity.ErrAgentRevoked` | 409 | `conflict` |
| `identity.ErrNotImplemented` | 501 | `internal` (logged) |
| store unique-violation | 409 | `conflict` |
| context.DeadlineExceeded | 504 | `internal` |
| anything else | 500 | `internal` |

## Plane boundary (ADR-019)

REST API is purely application-plane. It calls only:

- `identity.Service` (in-process, application-plane)
- `store.RepositoryReader` / `RepositoryWriter` (data-plane interface)
- `ratelimit.RateLimiter` (data-plane interface)

No direct gRPC calls into git or workflow planes from this PR.

## Outbox

This PR writes no new outbox events directly — all mutations go through
existing `identity.Service` methods that already write source + outbox
in the same Tx (ADR-008). Repository creation `Insert` is in scope; if
no `repository.repository_created` event currently exists, this PR
**adds it via the existing Tx pattern**, with a new repositories-domain
event constant in `plane/application/identity/events.go`'s repositories
sibling (or a new `plane/application/repositories/events.go` package if
identity is the wrong home — defer to plane/application directory norms).

## Testing strategy

Mirrors `plane/application/identity/`:

1. **Unit tests** per handler with `httptest.NewRecorder`, `StubService`,
   `MemoryLimiter`, fixed-token resolver.
2. **Integration test** with `testcontainers` Postgres + `PostgresService`
   exercising the full handler chain end-to-end through `httptest.Server`.
3. **Binary integration test** in `cmd/rest-api/integration_test.go`
   that boots `main` against PG, hits `/healthz` and one identity route,
   tears down.
4. **Auth middleware test** for token-missing → 401, token-invalid → 401,
   token-valid → handler called with principal in context.
5. **Rate-limit test** with `MemoryLimiter` capacity=1, refill=0; first
   request 200, second 429 with `Retry-After`.
6. **Pagination test** asserts `next_cursor` round-trip across page
   boundaries with 25 rows in PG, limit=10.

## Risks / unknowns

- **Bearer-token format**: not pinned by issue body. Defaulting to
  `Authorization: Bearer <opaque>` resolved against
  `identity.LookupIdentityForCache(principalID=parse(token))`. If a
  signed-token format (JWT-SVID per ADR-010) is required at this layer,
  follow-up issue.
- **Repository creation requires org existence**: org membership
  enforcement deferred to identity-service `AddOrgMember` (#15-revocation,
  out of scope). For this PR `POST /v1/repos` validates `org_id` parses
  but does not verify membership; tracked as follow-up.
- **`UpdatePermissions` semantic for repos** intentionally out of scope;
  `PATCH /v1/repos/{id}/permissions` is **not** in the endpoint list.
