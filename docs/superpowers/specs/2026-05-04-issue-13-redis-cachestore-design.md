# Design: Redis key conventions + CacheStore + RateLimiter (Issue #13)

**Status:** Draft for review
**Date:** 2026-05-04
**Owner:** Data plane
**ADRs:** ADR-009 (Redis), ADR-017 (interface seams)
**GitHub issue:** [#13](https://github.com/gitscale-platform/gitscale/issues/13)

## 1. Summary

Two interfaces, two concerns:

- **`CacheStore`** — generic key/value cache with TTLs, atomic increment, and CAS. Used for the repo-location cache and identity cache (ADR-009 role 1).
- **`RateLimiter`** — token-bucket rate limiter, separate interface, separate impls. Used for enforcement counters (ADR-009 role 2).

Both have a Redis impl (prod) and an in-memory impl (tests). Concrete code is wired at startup; only interfaces cross plane boundaries.

> **Why two interfaces, not one:** the issue puts `IncrBy` on `CacheStore` and treats the rate-limit counter as a generic key. That conflates concerns. Rate-limit semantics (capacity, refill rate, take-or-reject atomicity) are very different from cache semantics (transient lookups). Separate interfaces give us a clean test surface and let either backend be swapped independently.

## 2. Decisions locked

| ID | Decision | Rationale |
|---|---|---|
| D1 | **Token bucket via Lua** for rate limiting; separate `RateLimiter` interface | Agent traffic legitimately bursts; fixed-window 429s legitimate work |
| D2 | **Redis Cluster in prod, single Redis in dev** | Sharded HA for hot rate-limit counters; same code path for both |
| D3 | **Go singleflight in typed helpers** for cache stampede | Kills in-process duplication for free; cross-process XFetch deferred |
| D4 | Env namespace `gitscale:{env}:` auto-prefixed at `CacheStore` construction | Multi-env Redis safety |
| D5 | Negative caching in typed helpers, 30s TTL for not-found sentinel | Prevents infinite re-query of deleted entities |
| D6 | Typed payloads carry `Version int` field; mismatch on deserialize = treat as miss | Schema evolution without manual cache flush |
| D7 | `CompareAndSwap` impl: Redis Lua script (one round-trip). Sole rich-state mutation primitive on `CacheStore`; `IncrBy` removed. | Cluster-safe; agent-session quota uses CAS on JSON blob (§11) |
| D8 | `MGet` added to `CacheStore`; pipelined per-shard via go-redis/v9 | Hot-path batch fetches |
| D9 | TLS via `rediss://` connection string in prod; key-level encryption deferred | Standard Redis posture |
| D10 | Identity cache 60s TTL; mutations on `gitscale.identity.events` trigger Delete | 60s revocation window documented as accepted risk |
| D11 | Memory impl uses injectable `clock.Clock` | Deterministic TTL tests, no real sleeps |

## 3. Scope

In:
- `CacheStore` interface + Redis impl + memory impl
- `RateLimiter` interface + Redis-Lua impl + memory impl
- Key conventions register
- Typed helpers for `RepoLocation` + `IdentityCacheEntry`
- Token bucket Lua script (committed alongside Go code)
- Connection config: Cluster (prod) + single (dev)

Out:
- Identity invalidation consumer (subscribes to `gitscale.identity.events`, calls `Delete` on mutation) — separate issue, downstream of #11/#12
- XFetch / probabilistic early refresh (future)
- Per-key encryption at rest (future, if compliance demands it)
- Sentinel topology — explicitly skipped

## 4. Package layout

```
plane/data/cache/
  store.go                 # CacheStore interface
  store_redis.go           # go-redis/v9 impl (Cluster + single, behind same interface)
  store_memory.go          # in-process map, thread-safe, injectable clock
  store_namespace.go       # env-prefix wrapper
  keys.go                  # key templates + TTL constants
  repo_location.go         # typed helper: GetRepoLocation, SetRepoLocation
  identity.go              # typed helper: GetIdentity, SetIdentity, InvalidateIdentity
  store_test.go            # CacheStore compliance suite (runs against both impls)

plane/data/cache/lua/
  cas.lua                  # CompareAndSwap script

plane/data/ratelimit/
  limiter.go               # RateLimiter interface
  limiter_redis.go         # Redis-Lua impl
  limiter_memory.go        # token bucket struct + sync.Mutex
  namespace.go             # env-prefix wrapper (mirrors cache/store_namespace.go)
  keys.go                  # TokenBucketKey template
  lua/token_bucket.lua     # take-or-reject script
  limiter_test.go          # compliance suite
```

## 5. `CacheStore` interface

```go
// plane/data/cache/store.go

type CacheStore interface {
    // Get returns the cached value, or (nil, ErrNotFound) on miss.
    Get(ctx context.Context, key string) ([]byte, error)

    // MGet returns one slot per requested key, in order. nil entries are misses.
    MGet(ctx context.Context, keys []string) ([][]byte, error)

    // Set stores value with TTL (single round-trip — SET … EX).
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

    // Delete is a no-op on a missing key.
    Delete(ctx context.Context, key string) error

    // CompareAndSwap sets key=replacement only if its current value equals expected.
    // Returns true on swap, false on mismatch. ttl is applied on success.
    // Single round-trip via Lua.
    CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error)

    // Ping verifies connectivity. Returns nil on success.
    Ping(ctx context.Context) error
}

var ErrNotFound = errors.New("cache: key not found")
```

> **Differences from issue #13's interface:**
> - **`IncrBy` removed** from `CacheStore`. The issue's only justification for it was rate-limit counters; those moved to `RateLimiter` (§9). Agent-session quota uses `CompareAndSwap` on a JSON blob (§11). No remaining `CacheStore` consumer needs an atomic counter.
> - `MGet` added (issue gap).
> - `Get` returns `ErrNotFound` instead of `nil, nil`. Avoids the always-fragile is-nil-a-miss-or-empty-bytes check.

## 6. Env namespace wrapper

```go
// plane/data/cache/store_namespace.go

type namespacedStore struct {
    inner  CacheStore
    prefix string  // e.g. "gitscale:prod:"
}

func WithNamespace(inner CacheStore, env string) CacheStore {
    return &namespacedStore{inner: inner, prefix: "gitscale:" + env + ":"}
}

// every method prepends prefix to keys before delegating
```

Construction in main:
```go
raw := cache.NewRedisStore(cfg.RedisURL)
store := cache.WithNamespace(raw, cfg.Env)  // "gitscale:prod:repo:loc:abc-123"
```

The `keys.go` templates **do not** include the env prefix — the wrapper is the single place it's applied.

## 7. Key conventions

```go
// plane/data/cache/keys.go

const (
    // Repo location cache. TTL 600s. On miss: query repositories.repositories
    // (replica_set_id, home_region, acl_fingerprint), cache result.
    RepoLocationKey = "repo:loc:%s"  // %s = repo UUID

    // Identity cache. TTL 60s. On miss: query identity domain. Invalidated
    // by the identity-cache-invalidator consumer (separate issue) on
    // gitscale.identity.events mutations.
    IdentityKey = "identity:%s"  // %s = principal UUID

    // Agent session quota. Stored as JSON. CompareAndSwap'd on each
    // child-agent admission. TTL = session lifetime.
    // (Atomic-counter pattern lives on RateLimiter, not here.)
    AgentSessionQuotaKey = "quota:session:%s"  // %s = session UUID
)
// Token bucket key template lives in plane/data/ratelimit/keys.go,
// not here — it's consumed by RateLimiter, not CacheStore.

const (
    RepoLocationTTL    = 600 * time.Second
    RepoLocationNotFoundTTL = 30 * time.Second   // negative cache
    IdentityTTL        = 60 * time.Second
    IdentityNotFoundTTL = 30 * time.Second
)
```

## 8. Typed helpers — `RepoLocation`

```go
// plane/data/cache/repo_location.go

type RepoLocation struct {
    Version        int    `json:"v"`             // bump on schema change
    ReplicaSetID   string `json:"replica_set_id"`
    HomeRegion     string `json:"home_region"`
    ACLFingerprint string `json:"acl_fingerprint"`
}

const repoLocationVersion = 1

// not-found sentinel
var repoLocationMissBytes = []byte(`{"v":1,"_miss":true}`)

var repoLocationGroup singleflight.Group  // collapses concurrent misses per process

// GetRepoLocation: cache → loader → cache. Caches misses too (negative cache).
// Returns (nil, ErrNotFound) on cached or fresh miss.
func GetRepoLocation(
    ctx context.Context,
    c CacheStore,
    repoID uuid.UUID,
    loader func(ctx context.Context, id uuid.UUID) (*RepoLocation, error),
) (*RepoLocation, error) {
    key := fmt.Sprintf(RepoLocationKey, repoID)
    b, err := c.Get(ctx, key)
    if err == nil {
        loc, miss, decErr := decodeRepoLocation(b)
        if decErr == nil {
            if miss { return nil, ErrNotFound }
            return loc, nil
        }
        // decode error (version mismatch, corruption) → treat as miss, fall through
    }
    if err != nil && !errors.Is(err, ErrNotFound) {
        return nil, err  // Redis is broken — bubble up; caller decides whether to bypass cache
    }

    v, err, _ := repoLocationGroup.Do(key, func() (any, error) {
        return loader(ctx, repoID)
    })
    if err != nil { return nil, err }
    loc, _ := v.(*RepoLocation)
    if loc == nil {
        _ = c.Set(ctx, key, repoLocationMissBytes, RepoLocationNotFoundTTL)
        return nil, ErrNotFound
    }
    loc.Version = repoLocationVersion
    payload, _ := json.Marshal(loc)
    _ = c.Set(ctx, key, payload, RepoLocationTTL)
    return loc, nil
}

func SetRepoLocation(ctx context.Context, c CacheStore, repoID uuid.UUID, loc RepoLocation) error {
    loc.Version = repoLocationVersion
    payload, err := json.Marshal(loc)
    if err != nil { return err }
    return c.Set(ctx, fmt.Sprintf(RepoLocationKey, repoID), payload, RepoLocationTTL)
}

func decodeRepoLocation(b []byte) (loc *RepoLocation, miss bool, err error) {
    var raw struct {
        Version int  `json:"v"`
        Miss    bool `json:"_miss"`
        // remaining fields decoded into *RepoLocation only if version matches
    }
    if err := json.Unmarshal(b, &raw); err != nil { return nil, false, err }
    if raw.Version != repoLocationVersion { return nil, false, errVersionMismatch }
    if raw.Miss { return nil, true, nil }
    var out RepoLocation
    if err := json.Unmarshal(b, &out); err != nil { return nil, false, err }
    return &out, false, nil
}
```

`Identity` typed helper follows the same shape and is omitted here for brevity — same Version + miss-sentinel + singleflight pattern.

> **On singleflight (D3):** scope is per-process. With ~10 pods × per-key TTL expiry, worst case is 10 concurrent loader calls instead of N goroutines × 10 pods. Tolerable for v1; revisit if measured.

## 9. `RateLimiter` interface

Keys: `plane/data/ratelimit/keys.go` declares `TokenBucketKey = "rl:bucket:%s:%s"` (`%s` = principal UUID, `%s` = surface enum like `git_push`, `pr_open` — must not contain `:`). State is stored as a Redis HASH with fields `tokens` (float) and `last_ms` (int) — see Lua below. **Not** a JSON blob.

Namespacing: `RateLimiter` impls apply the same `gitscale:{env}:` prefix as `CacheStore`. A `WithNamespace(inner RateLimiter, env string) RateLimiter` wrapper lives in `plane/data/ratelimit/namespace.go`. Mirrors §6's `CacheStore` wrapper. Construction at startup applies it once; rest of code passes raw keys.

```go
// plane/data/ratelimit/limiter.go

type RateLimiter interface {
    // Take attempts to consume `n` tokens from the bucket identified by key.
    // Returns true if granted, false if denied (insufficient tokens).
    // Atomicity: take-or-reject decision happens inside Redis (Lua) or
    // inside a sync.Mutex (memory). No race window.
    //
    // capacity: max bucket size (legitimate burst budget)
    // refillPerSec: tokens added per second up to capacity
    // n: tokens to take (typically 1, but agent batch ops may take >1)
    Take(ctx context.Context, key string, capacity float64, refillPerSec float64, n float64) (granted bool, remaining float64, err error)
}
```

### Token bucket Lua

```lua
-- plane/data/ratelimit/lua/token_bucket.lua
-- KEYS[1] = bucket key
-- ARGV[1] = capacity
-- ARGV[2] = refill_per_sec
-- ARGV[3] = now_unix_ms
-- ARGV[4] = take_n
-- ARGV[5] = ttl_ms (2× window typical)

local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local now_ms   = tonumber(ARGV[3])
local n        = tonumber(ARGV[4])
local ttl_ms   = tonumber(ARGV[5])

local state = redis.call('HMGET', KEYS[1], 'tokens', 'last_ms')
local tokens   = tonumber(state[1]) or capacity
local last_ms  = tonumber(state[2]) or now_ms

local elapsed_s = (now_ms - last_ms) / 1000
tokens = math.min(capacity, tokens + elapsed_s * refill)

local granted = 0
if tokens >= n then
  tokens = tokens - n
  granted = 1
end

redis.call('HMSET', KEYS[1], 'tokens', tokens, 'last_ms', now_ms)
redis.call('PEXPIRE', KEYS[1], ttl_ms)

return {granted, tostring(tokens)}
```

### Memory impl

`limiter_memory.go` implements the same algorithm in Go with `sync.Mutex` per key and an injectable clock. Used in unit tests.

## 10. Connection config

```go
// plane/data/cache/store_redis.go

type RedisConfig struct {
    URL       string  // "rediss://...?cluster=true" or single "redis://localhost:6379"
    UseCluster bool   // explicit; URL parsing alone is fragile
    PoolSize   int    // default 10
    DialTimeout time.Duration
}

func NewRedisStore(cfg RedisConfig) (CacheStore, error) {
    if cfg.UseCluster {
        return newClusterStore(cfg)
    }
    return newSingleStore(cfg)
}
```

Both branches return the same `CacheStore`. go-redis/v9 handles transparent routing for `MGet` across cluster shards — but be aware: under Cluster, an `MGet` over N keys spread across S shards is up to S round-trips (pipelined per-shard), not one. Hot-path callers batching across many random UUIDs should expect this. Single-Redis dev path is one round-trip.

| Env | Topology | Connection |
|---|---|---|
| dev (`make dev-up`) | Single Redis 7 | `redis://localhost:6379` |
| staging | Cluster, 3 shards × 2 replicas | `rediss://...` |
| prod | Cluster, 6 shards × 2 replicas | `rediss://...` |

TLS (`rediss://`) on staging + prod. Shard count tuned to write rate of `gitscale:prod:rl:bucket:*` keys (the hottest namespace).

## 11. Agent session quota mechanics

The session quota (`AgentSessionQuotaKey`) is a JSON blob mutated via `CompareAndSwap`, not via an atomic counter. Pattern:

```go
type SessionQuota struct {
    Version    int    `json:"v"`
    SessionID  string `json:"session_id"`
    Remaining  int64  `json:"remaining"`
    Capacity   int64  `json:"capacity"`
    UpdatedAt  time.Time `json:"updated_at"`
}

// Admit charges `cost` against the parent session's remaining quota.
// Returns ErrQuotaExceeded if Remaining < cost; otherwise persists the
// decremented value via CAS. Retries on CAS mismatch up to maxRetries.
func Admit(ctx context.Context, c CacheStore, sessionID uuid.UUID, cost int64) error {
    key := fmt.Sprintf(AgentSessionQuotaKey, sessionID)
    for attempt := 0; attempt < maxRetries; attempt++ {
        cur, err := c.Get(ctx, key)
        if err != nil { return err }              // miss = session unknown
        var q SessionQuota
        if err := json.Unmarshal(cur, &q); err != nil || q.Version != quotaVersion {
            return ErrQuotaCorrupt
        }
        if q.Remaining < cost { return ErrQuotaExceeded }
        q.Remaining -= cost
        q.UpdatedAt = time.Now().UTC()
        next, _ := json.Marshal(q)
        ok, err := c.CompareAndSwap(ctx, key, cur, next, sessionTTL)
        if err != nil { return err }
        if ok { return nil }
        // CAS lost → another admission landed first. Retry.
    }
    return ErrQuotaContended
}
```

**Why CAS not atomic counter:** child-agent admission is bounded per session (≤low hundreds, not millions). CAS contention is rare. Trade-off: lose-and-retry on contention vs the operational simplicity of one mutation primitive (CAS) covering all rich-state mutations on `CacheStore`.

`maxRetries` defaults to 3. Beyond that, returns `ErrQuotaContended` and the caller decides whether to retry at a higher level.

## 12. `CompareAndSwap` semantics

```lua
-- plane/data/cache/lua/cas.lua
-- KEYS[1] = key
-- ARGV[1] = expected (bytes; "" = key absent)
-- ARGV[2] = replacement
-- ARGV[3] = ttl_ms
local cur = redis.call('GET', KEYS[1])
if cur == false then cur = "" end
if cur ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', tonumber(ARGV[3]))
return 1
```

Single key, cluster-safe. Memory impl: `sync.Mutex` around a map lookup-compare-set.

## 13. Compliance test suite

`store_test.go` runs the **same test cases** against both `store_redis.go` (testcontainers Redis) and `store_memory.go`. Cases:

- Get on missing → ErrNotFound
- Set + Get round-trip
- TTL expiry (memory: tick clock; Redis: real wait or `DEBUG SLEEP` — prefer tick + Redis for cluster)
- MGet with mix of present/absent keys → matching slots are nil
- CAS happy path: expected matches, swap succeeds
- CAS mismatch: returns false, value unchanged
- CAS on absent key: expected="" matches, swap succeeds
- Concurrent CAS from N goroutines: exactly one succeeds per round

Identical suite for `RateLimiter`:
- Empty bucket — first take returns granted=true, remaining=capacity-n
- Exhausted bucket — next take returns granted=false
- Refill — advance clock, take succeeds again
- Capacity ceiling — refill never exceeds capacity
- Concurrent takes — total granted across goroutines = floor(initial_tokens / n)

## 14. Plane boundaries

- `plane/data/cache/store.go` (interface) — imported by application plane, edge plane, workflow plane.
- `plane/data/cache/store_redis.go` (concrete) — imported only by `cmd/*` startup binaries.
- `plane/data/ratelimit/limiter.go` — same pattern.
- The env-namespace wrapper is constructed once at startup; rest of code never sees the prefix.

## 15. Failure modes

| Scenario | CacheStore behavior | Caller behavior |
|---|---|---|
| Redis unreachable | All ops return wrapped error | Caller fallback: query source-of-truth (PostgreSQL) directly. Repo-location helper does this; rate limiter denies the request (fail-closed). Note: ADR-009's "~1h degraded-mode" survival of a metadata-DB outage assumes Redis is up. Loss of *both* Redis and PostgreSQL is unrecoverable in v1 — Cluster HA is the mitigation |
| Cluster split / failover mid-op | go-redis/v9 retries; surfaced as error after retry budget | Same as unreachable |
| Lua script not loaded | go-redis/v9 falls back to `EVAL` (vs `EVALSHA`) automatically | Transparent |
| Key expires between Get and Caller's use | Stale read — caller treats as fresh enough within TTL semantics | Acceptable per ADR-009 |
| Cache version mismatch on deserialize | Treated as miss; helper rebuilds from loader | Cache effectively flushed for old payload version |
| Identity mutation while cached | 60s window of stale identity | Documented (D10); invalidator consumer narrows it on best-effort basis |
| Negative-cache hit during a deletion-then-recreation race | Up to 30s of phantom not-found after recreation | Acceptable; admins can manually `Delete` the cache key for rapid recovery |

## 16. Future work (not blocking #13)

- XFetch / probabilistic early refresh for cross-process stampede
- Per-key encryption at rest (if compliance demands)
- Sentinel topology support (only if Cluster proves wrong)
- A streaming `Subscribe` method on `CacheStore` for pub/sub-driven invalidation across pods (shorter-than-TTL invalidation without polling)
- Identity-cache invalidator consumer subscribing to `gitscale.identity.events` — separate issue, blocked by #11/#12

## 17. Acceptance criteria (refines issue #13's list)

The issue's acceptance criteria are kept; this spec adds:

- [ ] `RateLimiter` is a separate interface in `plane/data/ratelimit/`, not a method on `CacheStore`.
- [ ] `IncrBy` is **removed** from `CacheStore` (rate-limit moved to `RateLimiter`; quota uses `CompareAndSwap`).
- [ ] Agent-session quota is mutated via `CompareAndSwap` on a `SessionQuota` JSON blob (§11), not via an atomic counter.
- [ ] `MGet` is on `CacheStore`.
- [ ] `Get` returns `ErrNotFound` on miss (sentinel error, not nil-byte ambiguity).
- [ ] All keys auto-prefixed via `WithNamespace(inner, env)` wrapper; key templates do not include the prefix.
- [ ] Typed helpers carry a `Version int` field; mismatch on decode → treat as miss.
- [ ] Typed helpers cache not-found sentinel with shorter TTL.
- [ ] Typed helpers wrap loader calls in `singleflight.Group`.
- [ ] `CompareAndSwap` and token bucket `Take` are each implemented as a single Lua script, single round-trip.
- [ ] Memory impl accepts `clock.Clock` for deterministic TTL/refill tests.
- [ ] Compliance suite runs identical cases against both Redis and memory impls.
- [ ] Connection config supports both Cluster (prod/staging) and single (dev) without separate code paths above the interface.
- [ ] TLS via `rediss://` documented as standard for staging + prod.
- [ ] Identity cache invalidation consumer is explicitly out of scope and tracked as future work.
- [ ] `RateLimiter` namespacing: `WithNamespace(inner, env)` wrapper present and applied at startup.
- [ ] `TokenBucketKey` template lives in `plane/data/ratelimit/keys.go`, not in `cache/keys.go`.
- [ ] Token bucket state stored as Redis HASH (`tokens`, `last_ms` fields), not JSON.
