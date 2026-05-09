# Git Plane Foundation — Sub-spec 1 of Phase 2
**Issues:** #107 (Gitaly proxy bootstrap), #109 (metering reconciliation)
**Date:** 2026-05-09
**Status:** Approved

---

## 1. Scope

Bootstrap `plane/git` from its current doc.go stub into a working Gitaly RPC proxy with:

- Gitaly gRPC connection pool
- Repo-location lookup (CacheStore-first, MetadataStore fallback)
- `GitRPC` interface: `InfoRefs`, `UploadPack`, `ReceivePack`
- In-process `HookHandler` interface (pre-receive hook invocation point)
- Two-tier metering counter (Redis enforcement + outbox reconciliation)
- `AnalyticsSink` interface + in-memory stub
- docker-compose Gitaly dev server entry

**Out of scope:** Real Gitaly custom hook binary (Phase 3), ClickHouse integration (Phase 3), AGENTS.md enforcement in the hook (issue #114, Sub-spec 3), cold-tier storage routing (open architecture question, June 2026).

---

## 2. Package layout

```
plane/git/
  doc.go             (existing)
  client/
    pool.go          GitalyPool — gRPC connection pool, keyed by file-server address; Conn(addr) returns existing conn or dials new one
    pool_test.go
  locator/
    locator.go       RepoLocator interface
    cache.go         CacheStore-backed impl; key: git:loc:{repo_id}, TTL 5min
    postgres.go      MetadataStore fallback + write-through on cache miss
    locator_test.go
  rpc/
    rpc.go           GitRPC interface
    proxy.go         GitalyProxy — pool + locator + hook injected at construction
    proxy_test.go
  hook/
    hook.go          HookHandler interface
    noop.go          NoopHookHandler (default; AGENTS.md impl wires in via #114)
  metering/
    counter.go       TwoTierCounter — Redis INCRBY + outbox INSERT
    events.go        MeteringEvent type (bytes_transferred, pack_objects, ref_updates)
    counter_test.go
  sink/
    sink.go          AnalyticsSink interface
    stub.go          in-memory stub (append-only slice, queryable in tests)
```

---

## 3. Interfaces

```go
// GitRPC is the public surface of plane/git. Callers never import Gitaly proto directly.
type GitRPC interface {
    InfoRefs(ctx context.Context, repo RepoRef, service string) (io.ReadCloser, error)
    UploadPack(ctx context.Context, repo RepoRef, r io.Reader) (io.ReadCloser, error)
    ReceivePack(ctx context.Context, repo RepoRef, r io.Reader) (io.ReadCloser, error)
}

// RepoRef identifies a repository for a Git operation.
type RepoRef struct {
    RepoID    string
    AgentID   string // empty for human pushes
}

// RefUpdate is a single ref change within a push.
type RefUpdate struct {
    RefName    string
    OldOID     string
    NewOID     string
}

// HookHandler is called synchronously before ReceivePack is forwarded to Gitaly.
// A non-nil error rejects the push; the error message is returned to the client.
type HookHandler interface {
    PreReceive(ctx context.Context, repo RepoRef, updates []RefUpdate) error
}

// AnalyticsSink receives metering events drained from the outbox.
type AnalyticsSink interface {
    Record(ctx context.Context, e MeteringEvent) error
}

// MeteringEvent carries per-RPC metering data for the reconciliation path.
type MeteringEvent struct {
    EventID         string
    AgentID         string
    RepoID          string
    Operation       string // "receive_pack" | "upload_pack" | "info_refs"
    BytesTransferred int64
    PackObjects      int64
    RefUpdates       int
    OccurredAt      time.Time
}
```

---

## 4. Data flow

### 4.1 Push (ReceivePack)

```
Client push
  → GitRPC.ReceivePack(ctx, repo, body)
  → RepoLocator.Resolve(repo.RepoID)
      cache hit  → return file-server address
      cache miss → MetadataStore.GetRepoFileServer(repo_id)
                 → write-through to CacheStore (TTL 5min)
                 → not-found → codes.NotFound
  → HookHandler.PreReceive(ctx, repo, updates)
      error → codes.PermissionDenied, message from hook
  → GitalyPool.Conn(file_server_addr) → gRPC conn
  → stream-forward to Gitaly ReceivePack
  → on stream close (success):
      TwoTierCounter.Record(ctx, repo, bytes, pack_objects, ref_updates)
        → Redis INCRBY  git:meter:{agent_id}:{billing_window}
        → MetadataStore.WriteOutbox(metering event)
            failure → propagate error (metering is load-bearing per ADR-012)
```

### 4.2 Fetch (UploadPack / InfoRefs)

Same locator + pool path. No hook call. Metering records bytes transferred only (no ref updates, no pack object count).

### 4.3 Reconciliation path

The existing outbox consumer (`plane/data/outbox/`) drains metering outbox rows and calls `AnalyticsSink.Record()`. No new consumer process — metering events wire into the existing outbox wiring via a new topic entry in `plane/data/kafka/topics.go`.

---

## 5. Two-tier metering counter

| Tier | Store | Key pattern | Purpose | On write failure |
|------|-------|-------------|---------|-----------------|
| Enforcement | Redis | `git:meter:{agent_id}:{billing_window}` | 429 / throttle decisions | Log + continue (best-effort) |
| Reconciliation | outbox table | — | Analytics accuracy, billing | Propagate error, push rejected |

The enforcement counter is best-effort; a Redis blip does not reject a push. The reconciliation outbox write is mandatory; a failure rejects the push to preserve metering integrity (ADR-012).

---

## 6. Error handling

| Condition | gRPC code | Notes |
|-----------|-----------|-------|
| All Gitaly pool connections fail | `Unavailable` | No silent fallback |
| RepoLocator: repo not in cache or DB | `NotFound` | |
| HookHandler rejects push | `PermissionDenied` | Message from hook surfaced to client |
| Outbox INSERT fails | propagate | Push rejected; metering is load-bearing |
| Redis enforcement write fails | — | Log, continue; not push-blocking |

---

## 7. Testing strategy

| Test file | Backend | What it covers |
|-----------|---------|----------------|
| `rpc/proxy_test.go` | in-process stub gRPC Gitaly, stub locator, stub hook | Normal push/fetch, hook rejection, not-found |
| `locator/locator_test.go` | `store_memory.go` (CacheStore), stub MetadataStore | Cache hit, cache miss + write-through, not-found |
| `metering/counter_test.go` | in-memory Redis-alike (pattern from `limiter_memory.go`) | Both tiers written; Redis fail does not block; outbox fail propagates |
| `integration_test.go` | Real Postgres + real Redis (pattern from `plane/application/identity/integration_test.go`) | End-to-end push flow with real stores |

No mock-DB tests (GA gate: `make test` passes with no mock-DB tests in git plane packages).

---

## 8. docker-compose

Add to `docker-compose.yml`:

```yaml
gitaly:
  image: gitlab/gitaly:latest
  volumes:
    - gitaly-data:/home/git/repositories
    - ./config/gitaly:/etc/gitaly
  ports:
    - "8075:8075"

volumes:
  gitaly-data:
```

Add `config/gitaly/config.toml` in the same commit (CI linter rule: config ships with the service entry).

---

## 9. ADR conformance

| ADR | Requirement | How this design satisfies it |
|-----|-------------|------------------------------|
| ADR-008 | Outbox in same transaction as state change | ADR-008 applies to application-plane state mutations. Git metering has no co-located PG state change; outbox is written standalone. Failure rejects push to preserve metering integrity (ADR-012). |
| ADR-009 | Redis behind CacheStore interface | Repo-location uses `plane/data/cache.CacheStore`, no direct Redis calls |
| ADR-011 | Repo-location cache | CacheStore key `git:loc:{repo_id}`, TTL 5min, write-through on miss |
| ADR-012 | Two-tier metering at hook layer | Enforcement counter in Redis; reconciliation via outbox consumer |

---

## 10. Open questions not resolved by this spec

- Cold-tier routing in `locator/`: file-server may be hot-NVMe or cold-S3; routing logic deferred to June 2026 erasure coding decision.
- `HookHandler` predicate vocabulary for AGENTS.md: defined in Sub-spec 3 (#114).
- MCP server integration with `GitRPC.ReceivePack`: defined in Sub-spec 4 (#112).
