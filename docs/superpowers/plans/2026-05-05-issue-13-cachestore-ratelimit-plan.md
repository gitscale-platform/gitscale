# Redis CacheStore + RateLimiter (#13) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement two interfaces (`CacheStore` + `RateLimiter`), each with Redis (Lua-driven) and in-memory implementations, plus typed helpers for `RepoLocation`, `IdentityCacheEntry`, and `SessionQuota`. Per `docs/superpowers/specs/2026-05-04-issue-13-redis-cachestore-design.md`.

**Architecture:** Two separate Go packages — `plane/data/cache` and `plane/data/ratelimit`. Each has interface + Redis impl + memory impl. Memory impls use injectable clock. Redis impls use Lua scripts for CAS and token-bucket (atomic, cluster-safe).

**Tech Stack:** Go 1.22, `github.com/redis/go-redis/v9`, `golang.org/x/sync/singleflight`, `github.com/google/uuid`, `testcontainers-go` for integration tests.

**Spec:** [`docs/superpowers/specs/2026-05-04-issue-13-redis-cachestore-design.md`](../specs/2026-05-04-issue-13-redis-cachestore-design.md)

**Issue:** [#13](https://github.com/gitscale-platform/gitscale/issues/13)

---

## File Structure

| File | Responsibility |
|---|---|
| `plane/data/cache/store.go` | `CacheStore` interface + `ErrNotFound` |
| `plane/data/cache/keys.go` | Cache key templates + TTL constants |
| `plane/data/cache/clock.go` | `Clock` interface for testable time |
| `plane/data/cache/store_memory.go` | In-process map impl, thread-safe, clock-injected |
| `plane/data/cache/store_namespace.go` | Env-prefix wrapper |
| `plane/data/cache/store_redis.go` | go-redis impl (single + cluster) |
| `plane/data/cache/lua/cas.lua` | CompareAndSwap script |
| `plane/data/cache/repo_location.go` | Typed helper w/ singleflight + negative cache |
| `plane/data/cache/identity.go` | Typed helper for `IdentityCacheEntry` |
| `plane/data/cache/session_quota.go` | CAS-based admit/release for `SessionQuota` |
| `plane/data/cache/store_compliance.go` | Shared test suite (not `_test.go` — exported helper) |
| `plane/data/cache/store_memory_test.go` | Memory unit tests via compliance suite |
| `plane/data/cache/store_redis_test.go` | Redis integration tests (testcontainers, build-tagged) |
| `plane/data/cache/repo_location_test.go` | Helper unit tests |
| `plane/data/ratelimit/limiter.go` | `RateLimiter` interface |
| `plane/data/ratelimit/keys.go` | `TokenBucketKey` template |
| `plane/data/ratelimit/limiter_memory.go` | sync.Mutex-based bucket impl |
| `plane/data/ratelimit/limiter_redis.go` | Lua-driven Redis impl |
| `plane/data/ratelimit/lua/token_bucket.lua` | Take-or-reject script |
| `plane/data/ratelimit/namespace.go` | Env-prefix wrapper |
| `plane/data/ratelimit/limiter_compliance.go` | Shared test suite |

---

## Task 1: Dependencies + package skeletons

**Files:**
- Modify: `go.mod`
- Create: `plane/data/cache/doc.go`, `plane/data/ratelimit/doc.go`

- [ ] **Step 1: Add deps**

```bash
go get github.com/redis/go-redis/v9@v9.5.1
go get golang.org/x/sync/singleflight
go mod tidy
```

- [ ] **Step 2: Write package docs**

```go
// Package cache defines the CacheStore interface and concrete impls per ADR-009 + ADR-017.
// Spec: docs/superpowers/specs/2026-05-04-issue-13-redis-cachestore-design.md
package cache
```

```go
// Package ratelimit defines the RateLimiter interface and concrete impls per ADR-009.
// Spec: docs/superpowers/specs/2026-05-04-issue-13-redis-cachestore-design.md
package ratelimit
```

- [ ] **Step 3: Verify build**

Run: `go build ./plane/data/cache/... ./plane/data/ratelimit/...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum plane/data/cache/doc.go plane/data/ratelimit/doc.go
git commit -m "feat(cache,ratelimit): package skeletons + deps (#13)"
```

---

## Task 2: `Clock` interface + `CacheStore` interface

**Files:**
- Create: `plane/data/cache/clock.go`
- Create: `plane/data/cache/store.go`

- [ ] **Step 1: Implement Clock**

```go
package cache

import "time"

// Clock returns the current time. Tests inject a fake clock for deterministic TTL behavior.
type Clock interface {
  Now() time.Time
}

type realClock struct{}
func (realClock) Now() time.Time { return time.Now() }

// SystemClock is the default clock used in production.
var SystemClock Clock = realClock{}

// FakeClock is a test clock with manual time control. Safe for concurrent use.
type FakeClock struct {
  mu  sync.Mutex
  now time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

func (c *FakeClock) Now() time.Time {
  c.mu.Lock(); defer c.mu.Unlock()
  return c.now
}

func (c *FakeClock) Advance(d time.Duration) {
  c.mu.Lock(); defer c.mu.Unlock()
  c.now = c.now.Add(d)
}

// import sync at top of file
```

Save to `plane/data/cache/clock.go` (add `"sync"` to imports).

- [ ] **Step 2: Implement CacheStore interface**

```go
package cache

import (
  "context"
  "errors"
  "time"
)

// ErrNotFound is returned by Get for cache misses. Sentinel error, not nil-byte ambiguity.
var ErrNotFound = errors.New("cache: key not found")

// CacheStore is a generic key/value cache with TTL, atomic CAS, and batch get.
// IncrBy is intentionally absent — use RateLimiter for rate-limit counters and
// CompareAndSwap for rich-state mutations (see SessionQuota helper).
type CacheStore interface {
  // Get returns the cached value, or (nil, ErrNotFound) on miss.
  Get(ctx context.Context, key string) ([]byte, error)

  // MGet returns one slot per requested key, in order. nil entries are misses.
  MGet(ctx context.Context, keys []string) ([][]byte, error)

  // Set stores value with TTL.
  Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

  // Delete is a no-op on a missing key.
  Delete(ctx context.Context, key string) error

  // CompareAndSwap sets key=replacement only if the current value equals expected.
  // Returns true on swap, false on mismatch. ttl is applied on success.
  // Single round-trip via Lua. expected="" matches an absent key.
  CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error)

  // Ping verifies connectivity.
  Ping(ctx context.Context) error
}
```

Save to `plane/data/cache/store.go`.

- [ ] **Step 3: Verify build**

Run: `go build ./plane/data/cache/...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add plane/data/cache/clock.go plane/data/cache/store.go
git commit -m "feat(cache): CacheStore interface + Clock (#13)"
```

---

## Task 3: Key conventions (`keys.go`)

**Files:**
- Create: `plane/data/cache/keys.go`

- [ ] **Step 1: Implement**

```go
package cache

import "time"

// Key templates. The env namespace prefix (gitscale:{env}:) is applied
// by the namespace wrapper — these constants do NOT include it.
const (
  // Repo location cache. Loader queries repositories.repositories.
  RepoLocationKey = "repo:loc:%s"  // %s = repo UUID

  // Identity cache. Loader queries identity domain. Invalidator consumer
  // (separate issue) deletes on gitscale.identity.events mutations.
  IdentityKey = "identity:%s"  // %s = principal UUID

  // Agent session quota. Stored as JSON, mutated via CompareAndSwap.
  // (Atomic-counter pattern lives on RateLimiter, not here.)
  AgentSessionQuotaKey = "quota:session:%s"  // %s = session UUID
)

// TTL constants — keep in sync with spec §7.
const (
  RepoLocationTTL         = 600 * time.Second
  RepoLocationNotFoundTTL = 30 * time.Second
  IdentityTTL             = 60 * time.Second
  IdentityNotFoundTTL     = 30 * time.Second
)
```

Save to `plane/data/cache/keys.go`.

- [ ] **Step 2: Commit**

```bash
git add plane/data/cache/keys.go
git commit -m "feat(cache): key templates + TTL constants (#13)"
```

---

## Task 4: Compliance suite + memory impl

**Files:**
- Create: `plane/data/cache/store_compliance.go`
- Create: `plane/data/cache/store_memory.go`
- Create: `plane/data/cache/store_memory_test.go`

The compliance suite is exported (no `_test.go` suffix) so the Redis-impl test file can re-use it.

- [ ] **Step 1: Write compliance suite**

```go
package cache

import (
  "context"
  "errors"
  "sync"
  "testing"
  "time"
)

// CompliancePack runs the standard CacheStore test cases against any impl.
// factory must return (store, advance time hook). For impls without a clock
// (e.g. real Redis), advance can be a real time.Sleep wrapper.
type CompliancePack struct {
  NewStore func(t *testing.T) (CacheStore, func(time.Duration))
}

func (p CompliancePack) Run(t *testing.T) {
  t.Run("Get_Missing_ReturnsErrNotFound", p.testGetMissing)
  t.Run("Set_then_Get_RoundTrip", p.testSetGetRoundTrip)
  t.Run("Set_with_TTL_Expires", p.testSetTTLExpires)
  t.Run("Delete_RemovesKey", p.testDelete)
  t.Run("Delete_AbsentKey_NoError", p.testDeleteAbsent)
  t.Run("MGet_MixedHitsAndMisses", p.testMGetMixed)
  t.Run("CAS_HappyPath", p.testCASHappy)
  t.Run("CAS_Mismatch", p.testCASMismatch)
  t.Run("CAS_OnAbsentKey_WithEmptyExpected", p.testCASAbsent)
  t.Run("CAS_Concurrent_ExactlyOneWins", p.testCASConcurrent)
  t.Run("Ping", p.testPing)
}

func (p CompliancePack) testGetMissing(t *testing.T) {
  s, _ := p.NewStore(t)
  _, err := s.Get(context.Background(), "nope")
  if !errors.Is(err, ErrNotFound) { t.Errorf("err = %v, want ErrNotFound", err) }
}

func (p CompliancePack) testSetGetRoundTrip(t *testing.T) {
  s, _ := p.NewStore(t)
  if err := s.Set(context.Background(), "k", []byte("v"), time.Minute); err != nil { t.Fatal(err) }
  got, err := s.Get(context.Background(), "k")
  if err != nil { t.Fatal(err) }
  if string(got) != "v" { t.Errorf("got %q, want v", got) }
}

func (p CompliancePack) testSetTTLExpires(t *testing.T) {
  s, advance := p.NewStore(t)
  s.Set(context.Background(), "k", []byte("v"), 100*time.Millisecond)
  advance(200 * time.Millisecond)
  _, err := s.Get(context.Background(), "k")
  if !errors.Is(err, ErrNotFound) { t.Errorf("after ttl: err = %v, want ErrNotFound", err) }
}

func (p CompliancePack) testDelete(t *testing.T) {
  s, _ := p.NewStore(t)
  s.Set(context.Background(), "k", []byte("v"), time.Minute)
  if err := s.Delete(context.Background(), "k"); err != nil { t.Fatal(err) }
  _, err := s.Get(context.Background(), "k")
  if !errors.Is(err, ErrNotFound) { t.Errorf("after delete: err = %v", err) }
}

func (p CompliancePack) testDeleteAbsent(t *testing.T) {
  s, _ := p.NewStore(t)
  if err := s.Delete(context.Background(), "absent"); err != nil { t.Errorf("delete absent: err = %v, want nil", err) }
}

func (p CompliancePack) testMGetMixed(t *testing.T) {
  s, _ := p.NewStore(t)
  s.Set(context.Background(), "a", []byte("1"), time.Minute)
  s.Set(context.Background(), "c", []byte("3"), time.Minute)
  got, err := s.MGet(context.Background(), []string{"a", "b", "c"})
  if err != nil { t.Fatal(err) }
  if len(got) != 3 { t.Fatalf("got %d slots, want 3", len(got)) }
  if string(got[0]) != "1" || got[1] != nil || string(got[2]) != "3" {
    t.Errorf("MGet: got %v, want [1, nil, 3]", got)
  }
}

func (p CompliancePack) testCASHappy(t *testing.T) {
  s, _ := p.NewStore(t)
  s.Set(context.Background(), "k", []byte("a"), time.Minute)
  ok, err := s.CompareAndSwap(context.Background(), "k", []byte("a"), []byte("b"), time.Minute)
  if err != nil || !ok { t.Errorf("CAS: ok=%v err=%v, want true,nil", ok, err) }
  got, _ := s.Get(context.Background(), "k")
  if string(got) != "b" { t.Errorf("post-CAS: %q, want b", got) }
}

func (p CompliancePack) testCASMismatch(t *testing.T) {
  s, _ := p.NewStore(t)
  s.Set(context.Background(), "k", []byte("a"), time.Minute)
  ok, _ := s.CompareAndSwap(context.Background(), "k", []byte("WRONG"), []byte("b"), time.Minute)
  if ok { t.Error("expected ok=false on mismatch") }
  got, _ := s.Get(context.Background(), "k")
  if string(got) != "a" { t.Errorf("after failed CAS: %q, want a (unchanged)", got) }
}

func (p CompliancePack) testCASAbsent(t *testing.T) {
  s, _ := p.NewStore(t)
  ok, err := s.CompareAndSwap(context.Background(), "absent", []byte(""), []byte("v"), time.Minute)
  if err != nil || !ok { t.Errorf("CAS absent: ok=%v err=%v, want true,nil", ok, err) }
  got, _ := s.Get(context.Background(), "absent")
  if string(got) != "v" { t.Errorf("post-CAS-absent: %q, want v", got) }
}

func (p CompliancePack) testCASConcurrent(t *testing.T) {
  s, _ := p.NewStore(t)
  s.Set(context.Background(), "k", []byte("0"), time.Minute)
  var wins int64
  var wg sync.WaitGroup
  for i := 0; i < 10; i++ {
    wg.Add(1)
    go func() {
      defer wg.Done()
      if ok, _ := s.CompareAndSwap(context.Background(), "k", []byte("0"), []byte("1"), time.Minute); ok {
        atomic.AddInt64(&wins, 1)
      }
    }()
  }
  wg.Wait()
  if wins != 1 { t.Errorf("wins = %d, want 1", wins) }
}

func (p CompliancePack) testPing(t *testing.T) {
  s, _ := p.NewStore(t)
  if err := s.Ping(context.Background()); err != nil { t.Errorf("Ping: %v", err) }
}

// Add atomic import at top: "sync/atomic"
```

Save to `plane/data/cache/store_compliance.go`. Add `"sync/atomic"` to imports.

- [ ] **Step 2: Implement memory store**

```go
package cache

import (
  "bytes"
  "context"
  "sync"
  "time"
)

type memEntry struct {
  value     []byte
  expiresAt time.Time // zero = no expiry
}

// MemoryStore is an in-process CacheStore for tests.
type MemoryStore struct {
  mu    sync.Mutex
  data  map[string]memEntry
  clock Clock
}

// NewMemoryStore constructs a MemoryStore. Pass a *FakeClock for deterministic tests.
func NewMemoryStore(clock Clock) *MemoryStore {
  if clock == nil { clock = SystemClock }
  return &MemoryStore{data: map[string]memEntry{}, clock: clock}
}

func (m *MemoryStore) get(key string) ([]byte, bool) {
  e, ok := m.data[key]
  if !ok { return nil, false }
  if !e.expiresAt.IsZero() && !m.clock.Now().Before(e.expiresAt) {
    delete(m.data, key)
    return nil, false
  }
  return e.value, true
}

func (m *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
  m.mu.Lock(); defer m.mu.Unlock()
  v, ok := m.get(key)
  if !ok { return nil, ErrNotFound }
  out := make([]byte, len(v)); copy(out, v); return out, nil
}

func (m *MemoryStore) MGet(ctx context.Context, keys []string) ([][]byte, error) {
  m.mu.Lock(); defer m.mu.Unlock()
  out := make([][]byte, len(keys))
  for i, k := range keys {
    if v, ok := m.get(k); ok {
      cp := make([]byte, len(v)); copy(cp, v)
      out[i] = cp
    }
  }
  return out, nil
}

func (m *MemoryStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
  m.mu.Lock(); defer m.mu.Unlock()
  cp := make([]byte, len(value)); copy(cp, value)
  exp := time.Time{}
  if ttl > 0 { exp = m.clock.Now().Add(ttl) }
  m.data[key] = memEntry{value: cp, expiresAt: exp}
  return nil
}

func (m *MemoryStore) Delete(ctx context.Context, key string) error {
  m.mu.Lock(); defer m.mu.Unlock()
  delete(m.data, key)
  return nil
}

func (m *MemoryStore) CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error) {
  m.mu.Lock(); defer m.mu.Unlock()
  cur, ok := m.get(key)
  curVal := []byte("")
  if ok { curVal = cur }
  if !bytes.Equal(curVal, expected) { return false, nil }
  cp := make([]byte, len(replacement)); copy(cp, replacement)
  exp := time.Time{}
  if ttl > 0 { exp = m.clock.Now().Add(ttl) }
  m.data[key] = memEntry{value: cp, expiresAt: exp}
  return true, nil
}

func (m *MemoryStore) Ping(ctx context.Context) error { return nil }
```

Save to `plane/data/cache/store_memory.go`.

- [ ] **Step 3: Wire memory store into compliance suite**

```go
package cache

import (
  "testing"
  "time"
)

func TestMemoryStore_Compliance(t *testing.T) {
  pack := CompliancePack{
    NewStore: func(t *testing.T) (CacheStore, func(time.Duration)) {
      fc := NewFakeClock(time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC))
      return NewMemoryStore(fc), fc.Advance
    },
  }
  pack.Run(t)
}
```

Save to `plane/data/cache/store_memory_test.go`.

- [ ] **Step 4: Run, expect all PASS**

Run: `go test ./plane/data/cache/... -run TestMemoryStore_Compliance -v`
Expected: 11 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/cache/store_compliance.go plane/data/cache/store_memory.go plane/data/cache/store_memory_test.go
git commit -m "feat(cache): MemoryStore + shared compliance suite (#13)"
```

---

## Task 5: Env-namespace wrapper

**Files:**
- Create: `plane/data/cache/store_namespace.go`
- Create: `plane/data/cache/store_namespace_test.go`

- [ ] **Step 1: Write failing test**

```go
package cache

import (
  "context"
  "testing"
  "time"
)

func TestNamespacedStore_PrependsPrefix(t *testing.T) {
  inner := NewMemoryStore(NewFakeClock(time.Now()))
  store := WithNamespace(inner, "test")

  store.Set(context.Background(), "k", []byte("v"), time.Minute)

  // Inner should see the prefixed key
  got, err := inner.Get(context.Background(), "gitscale:test:k")
  if err != nil || string(got) != "v" {
    t.Errorf("inner.Get(prefixed): got=%q err=%v, want v", got, err)
  }
}

func TestNamespacedStore_AllOps(t *testing.T) {
  inner := NewMemoryStore(NewFakeClock(time.Now()))
  store := WithNamespace(inner, "test")
  pack := CompliancePack{
    NewStore: func(t *testing.T) (CacheStore, func(time.Duration)) {
      // Each sub-test gets a fresh inner+wrapper
      fc := NewFakeClock(time.Now())
      i := NewMemoryStore(fc)
      return WithNamespace(i, "test"), fc.Advance
    },
  }
  pack.Run(t)
  _ = store
}
```

Save to `plane/data/cache/store_namespace_test.go`.

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./plane/data/cache/... -run TestNamespacedStore -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package cache

import (
  "context"
  "time"
)

type namespacedStore struct {
  inner  CacheStore
  prefix string
}

// WithNamespace wraps inner so all keys are auto-prefixed with "gitscale:{env}:".
func WithNamespace(inner CacheStore, env string) CacheStore {
  return &namespacedStore{inner: inner, prefix: "gitscale:" + env + ":"}
}

func (n *namespacedStore) wrap(key string) string { return n.prefix + key }

func (n *namespacedStore) wrapMany(keys []string) []string {
  out := make([]string, len(keys))
  for i, k := range keys { out[i] = n.wrap(k) }
  return out
}

func (n *namespacedStore) Get(ctx context.Context, key string) ([]byte, error) { return n.inner.Get(ctx, n.wrap(key)) }
func (n *namespacedStore) MGet(ctx context.Context, keys []string) ([][]byte, error) { return n.inner.MGet(ctx, n.wrapMany(keys)) }
func (n *namespacedStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error { return n.inner.Set(ctx, n.wrap(key), value, ttl) }
func (n *namespacedStore) Delete(ctx context.Context, key string) error { return n.inner.Delete(ctx, n.wrap(key)) }
func (n *namespacedStore) CompareAndSwap(ctx context.Context, key string, e, r []byte, ttl time.Duration) (bool, error) { return n.inner.CompareAndSwap(ctx, n.wrap(key), e, r, ttl) }
func (n *namespacedStore) Ping(ctx context.Context) error { return n.inner.Ping(ctx) }
```

Save to `plane/data/cache/store_namespace.go`.

- [ ] **Step 4: Run, expect pass**

Run: `go test ./plane/data/cache/... -run TestNamespacedStore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/cache/store_namespace.go plane/data/cache/store_namespace_test.go
git commit -m "feat(cache): WithNamespace wrapper for env-prefixed keys (#13)"
```

---

## Task 6: Redis impl (single-node mode)

**Files:**
- Create: `plane/data/cache/lua/cas.lua`
- Create: `plane/data/cache/store_redis.go`
- Create: `plane/data/cache/store_redis_test.go`

- [ ] **Step 1: Write `cas.lua`**

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

- [ ] **Step 2: Implement RedisStore**

```go
package cache

import (
  "context"
  _ "embed"
  "errors"
  "time"

  "github.com/redis/go-redis/v9"
)

//go:embed lua/cas.lua
var casScriptSrc string

var casScript = redis.NewScript(casScriptSrc)

// RedisConfig wires either a single-Redis or Cluster client.
type RedisConfig struct {
  Addrs       []string  // single-mode: one entry; cluster: many
  UseCluster  bool
  Username    string
  Password    string
  TLS         bool      // emits rediss:// behavior
  PoolSize    int
  DialTimeout time.Duration
}

// redisCmder unifies *redis.Client and *redis.ClusterClient under one interface.
type redisCmder interface {
  redis.Cmdable
  Close() error
}

type RedisStore struct {
  client redisCmder
}

// NewRedisStore constructs a RedisStore for either single-node or Cluster mode.
func NewRedisStore(cfg RedisConfig) (*RedisStore, error) {
  if cfg.UseCluster {
    c := redis.NewClusterClient(&redis.ClusterOptions{
      Addrs:       cfg.Addrs,
      Username:    cfg.Username,
      Password:    cfg.Password,
      PoolSize:    cfg.PoolSize,
      DialTimeout: cfg.DialTimeout,
    })
    return &RedisStore{client: c}, nil
  }
  if len(cfg.Addrs) == 0 { return nil, errors.New("RedisConfig.Addrs required") }
  c := redis.NewClient(&redis.Options{
    Addr:        cfg.Addrs[0],
    Username:    cfg.Username,
    Password:    cfg.Password,
    PoolSize:    cfg.PoolSize,
    DialTimeout: cfg.DialTimeout,
  })
  return &RedisStore{client: c}, nil
}

func (r *RedisStore) Close() error { return r.client.Close() }

func (r *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
  v, err := r.client.Get(ctx, key).Bytes()
  if errors.Is(err, redis.Nil) { return nil, ErrNotFound }
  return v, err
}

func (r *RedisStore) MGet(ctx context.Context, keys []string) ([][]byte, error) {
  res, err := r.client.MGet(ctx, keys...).Result()
  if err != nil { return nil, err }
  out := make([][]byte, len(res))
  for i, v := range res {
    if v == nil { continue }
    s, _ := v.(string)
    out[i] = []byte(s)
  }
  return out, nil
}

func (r *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
  return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *RedisStore) Delete(ctx context.Context, key string) error {
  return r.client.Del(ctx, key).Err()
}

func (r *RedisStore) CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error) {
  res, err := casScript.Run(ctx, r.client, []string{key}, string(expected), string(replacement), int(ttl/time.Millisecond)).Int()
  if err != nil { return false, err }
  return res == 1, nil
}

func (r *RedisStore) Ping(ctx context.Context) error {
  return r.client.Ping(ctx).Err()
}
```

Save to `plane/data/cache/store_redis.go`.

- [ ] **Step 3: Write Redis integration test (testcontainers)**

```go
//go:build integration

package cache

import (
  "context"
  "testing"
  "time"

  "github.com/testcontainers/testcontainers-go"
  "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestRedisStore_Single_Compliance(t *testing.T) {
  ctx := context.Background()
  c, err := redis.RunContainer(ctx, testcontainers.WithImage("redis:7-alpine"))
  if err != nil { t.Fatal(err) }
  defer c.Terminate(ctx)

  addr, err := c.ConnectionString(ctx)
  if err != nil { t.Fatal(err) }

  pack := CompliancePack{
    NewStore: func(t *testing.T) (CacheStore, func(time.Duration)) {
      s, err := NewRedisStore(RedisConfig{Addrs: []string{addr[len("redis://"):]}})
      if err != nil { t.Fatal(err) }
      t.Cleanup(func() { s.Close() })
      // Flush between subtests for isolation
      s.client.FlushAll(context.Background())
      // For TTL tests, we use real time; advance is wall-clock sleep
      return s, time.Sleep
    },
  }
  pack.Run(t)
}
```

Save to `plane/data/cache/store_redis_test.go`. Note `//go:build integration`. Add dep:

```bash
go get github.com/testcontainers/testcontainers-go/modules/redis@v0.29.1
```

- [ ] **Step 4: Run integration test**

Run: `go test -tags integration ./plane/data/cache/... -run TestRedisStore_Single -v -timeout 120s`
Expected: 11 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/cache/lua plane/data/cache/store_redis.go plane/data/cache/store_redis_test.go go.mod go.sum
git commit -m "feat(cache): Redis impl (single + cluster modes) + Lua CAS (#13)"
```

---

## Task 7: `RepoLocation` typed helper

**Files:**
- Create: `plane/data/cache/repo_location.go`
- Create: `plane/data/cache/repo_location_test.go`

- [ ] **Step 1: Write failing test**

```go
package cache

import (
  "context"
  "errors"
  "sync/atomic"
  "testing"
  "time"

  "github.com/google/uuid"
)

func TestGetRepoLocation_CacheHit(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  want := RepoLocation{ReplicaSetID: "rs1", HomeRegion: "us-east-1", ACLFingerprint: "deadbeef"}
  if err := SetRepoLocation(context.Background(), store, id, want); err != nil { t.Fatal(err) }

  loadCount := int64(0)
  got, err := GetRepoLocation(context.Background(), store, id, func(ctx context.Context, _ uuid.UUID) (*RepoLocation, error) {
    atomic.AddInt64(&loadCount, 1)
    return nil, nil
  })
  if err != nil { t.Fatal(err) }
  if got.ReplicaSetID != want.ReplicaSetID { t.Errorf("got %+v, want %+v", got, want) }
  if loadCount != 0 { t.Errorf("loader called %d times on cache hit, want 0", loadCount) }
}

func TestGetRepoLocation_CacheMiss_LoadsAndCaches(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  want := RepoLocation{ReplicaSetID: "rs1"}

  loadCount := int64(0)
  loader := func(ctx context.Context, _ uuid.UUID) (*RepoLocation, error) {
    atomic.AddInt64(&loadCount, 1)
    return &want, nil
  }
  got, err := GetRepoLocation(context.Background(), store, id, loader)
  if err != nil || got.ReplicaSetID != want.ReplicaSetID { t.Fatalf("first call: got=%v err=%v", got, err) }
  if loadCount != 1 { t.Errorf("loadCount = %d, want 1", loadCount) }

  // Second call: cache hit, no loader
  _, _ = GetRepoLocation(context.Background(), store, id, loader)
  if loadCount != 1 { t.Errorf("after cache hit: loadCount = %d, want still 1", loadCount) }
}

func TestGetRepoLocation_LoaderReturnsNil_NegativeCache(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  loadCount := int64(0)
  loader := func(ctx context.Context, _ uuid.UUID) (*RepoLocation, error) {
    atomic.AddInt64(&loadCount, 1)
    return nil, nil // not-found
  }
  _, err := GetRepoLocation(context.Background(), store, id, loader)
  if !errors.Is(err, ErrNotFound) { t.Errorf("first: err = %v, want ErrNotFound", err) }
  _, err = GetRepoLocation(context.Background(), store, id, loader)
  if !errors.Is(err, ErrNotFound) { t.Errorf("second: err = %v, want ErrNotFound", err) }
  if loadCount != 1 { t.Errorf("loadCount = %d, want 1 (negative cache absorbed second call)", loadCount) }
}

func TestGetRepoLocation_VersionMismatch_TreatedAsMiss(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  // Plant an old-version blob
  oldBlob := []byte(`{"v":99,"replica_set_id":"old"}`)
  store.Set(context.Background(), "repo:loc:"+id.String(), oldBlob, time.Minute)

  want := RepoLocation{ReplicaSetID: "new"}
  got, err := GetRepoLocation(context.Background(), store, id, func(ctx context.Context, _ uuid.UUID) (*RepoLocation, error) {
    return &want, nil
  })
  if err != nil || got.ReplicaSetID != "new" {
    t.Errorf("version mismatch: got=%v err=%v, want loader-rebuild", got, err)
  }
}
```

Save to `plane/data/cache/repo_location_test.go`.

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./plane/data/cache/... -run TestGetRepoLocation -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package cache

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"

  "github.com/google/uuid"
  "golang.org/x/sync/singleflight"
)

const repoLocationVersion = 1

type RepoLocation struct {
  Version        int    `json:"v"`
  ReplicaSetID   string `json:"replica_set_id"`
  HomeRegion     string `json:"home_region"`
  ACLFingerprint string `json:"acl_fingerprint"`
}

// repoLocationMissBytes is the negative-cache sentinel.
var repoLocationMissBytes = []byte(`{"v":1,"_miss":true}`)

var repoLocationGroup singleflight.Group

var errVersionMismatch = errors.New("repo_location: version mismatch")

// GetRepoLocation tries the cache, then loads on miss, caching the result.
// loader returns (nil, nil) for not-found — that result is negatively cached.
func GetRepoLocation(
  ctx context.Context,
  c CacheStore,
  repoID uuid.UUID,
  loader func(ctx context.Context, id uuid.UUID) (*RepoLocation, error),
) (*RepoLocation, error) {
  key := fmt.Sprintf(RepoLocationKey, repoID)
  b, err := c.Get(ctx, key)
  if err == nil {
    if loc, miss, decErr := decodeRepoLocation(b); decErr == nil {
      if miss { return nil, ErrNotFound }
      return loc, nil
    }
    // fallthrough on decode error (version mismatch or corruption)
  } else if !errors.Is(err, ErrNotFound) {
    return nil, err
  }

  v, err, _ := repoLocationGroup.Do(key, func() (any, error) { return loader(ctx, repoID) })
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

// SetRepoLocation overwrites the cached entry directly (used by invalidator).
func SetRepoLocation(ctx context.Context, c CacheStore, repoID uuid.UUID, loc RepoLocation) error {
  loc.Version = repoLocationVersion
  payload, err := json.Marshal(loc)
  if err != nil { return err }
  return c.Set(ctx, fmt.Sprintf(RepoLocationKey, repoID), payload, RepoLocationTTL)
}

func decodeRepoLocation(b []byte) (*RepoLocation, bool, error) {
  var raw struct {
    Version int  `json:"v"`
    Miss    bool `json:"_miss"`
  }
  if err := json.Unmarshal(b, &raw); err != nil { return nil, false, err }
  if raw.Version != repoLocationVersion { return nil, false, errVersionMismatch }
  if raw.Miss { return nil, true, nil }
  var out RepoLocation
  if err := json.Unmarshal(b, &out); err != nil { return nil, false, err }
  return &out, false, nil
}
```

Save to `plane/data/cache/repo_location.go`.

- [ ] **Step 4: Run, expect pass**

Run: `go test ./plane/data/cache/... -run TestGetRepoLocation -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/cache/repo_location.go plane/data/cache/repo_location_test.go
git commit -m "feat(cache): RepoLocation typed helper w/ singleflight + negative cache + version (#13)"
```

---

## Task 8: `IdentityCacheEntry` typed helper

**Files:**
- Create: `plane/data/cache/identity.go`
- Create: `plane/data/cache/identity_test.go`

Same shape as `RepoLocation` with different fields. Code repeated in full so this task can be implemented without consulting Task 7.

- [ ] **Step 1: Write failing tests**

```go
package cache

import (
  "context"
  "errors"
  "sync/atomic"
  "testing"
  "time"

  "github.com/google/uuid"
)

func TestGetIdentity_CacheHit(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  want := IdentityCacheEntry{PrincipalID: id.String(), PrincipalType: "human", OrgID: uuid.New().String(), Permissions: []string{"repo.read"}}
  if err := SetIdentity(context.Background(), store, id, want); err != nil { t.Fatal(err) }
  loadCount := int64(0)
  got, err := GetIdentity(context.Background(), store, id, func(ctx context.Context, _ uuid.UUID) (*IdentityCacheEntry, error) {
    atomic.AddInt64(&loadCount, 1)
    return nil, nil
  })
  if err != nil { t.Fatal(err) }
  if got.PrincipalID != want.PrincipalID { t.Errorf("got %+v, want %+v", got, want) }
  if loadCount != 0 { t.Errorf("loader called %d times on cache hit, want 0", loadCount) }
}

func TestGetIdentity_CacheMiss_LoadsAndCaches(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  want := IdentityCacheEntry{PrincipalID: id.String(), PrincipalType: "agent"}
  loadCount := int64(0)
  loader := func(ctx context.Context, _ uuid.UUID) (*IdentityCacheEntry, error) {
    atomic.AddInt64(&loadCount, 1)
    return &want, nil
  }
  got, err := GetIdentity(context.Background(), store, id, loader)
  if err != nil || got.PrincipalID != want.PrincipalID { t.Fatalf("first: got=%v err=%v", got, err) }
  if loadCount != 1 { t.Errorf("loadCount = %d, want 1", loadCount) }
  GetIdentity(context.Background(), store, id, loader)
  if loadCount != 1 { t.Errorf("after cache hit: loadCount = %d, want still 1", loadCount) }
}

func TestGetIdentity_LoaderReturnsNil_NegativeCache(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  loadCount := int64(0)
  loader := func(ctx context.Context, _ uuid.UUID) (*IdentityCacheEntry, error) {
    atomic.AddInt64(&loadCount, 1)
    return nil, nil
  }
  if _, err := GetIdentity(context.Background(), store, id, loader); !errors.Is(err, ErrNotFound) {
    t.Errorf("first: err = %v, want ErrNotFound", err)
  }
  if _, err := GetIdentity(context.Background(), store, id, loader); !errors.Is(err, ErrNotFound) {
    t.Errorf("second: err = %v, want ErrNotFound", err)
  }
  if loadCount != 1 { t.Errorf("loadCount = %d, want 1 (negative cache absorbed)", loadCount) }
}

func TestGetIdentity_VersionMismatch_TreatedAsMiss(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  id := uuid.New()
  store.Set(context.Background(), "identity:"+id.String(), []byte(`{"v":99,"principal_id":"old"}`), time.Minute)
  want := IdentityCacheEntry{PrincipalID: id.String(), PrincipalType: "human"}
  got, err := GetIdentity(context.Background(), store, id, func(ctx context.Context, _ uuid.UUID) (*IdentityCacheEntry, error) {
    return &want, nil
  })
  if err != nil || got.PrincipalID != want.PrincipalID {
    t.Errorf("version mismatch: got=%v err=%v, want loader-rebuild", got, err)
  }
}
```

Save to `plane/data/cache/identity_test.go`.

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./plane/data/cache/... -run TestGetIdentity -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package cache

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"
  "time"

  "github.com/google/uuid"
  "golang.org/x/sync/singleflight"
)

const identityVersion = 1

// IdentityCacheEntry is the cached principal record consumed by edge / app planes.
type IdentityCacheEntry struct {
  Version       int        `json:"v"`
  PrincipalID   string     `json:"principal_id"`
  PrincipalType string     `json:"principal_type"`   // "human" | "agent"
  OrgID         string     `json:"org_id"`
  Permissions   []string   `json:"permissions"`
  DisabledAt    *time.Time `json:"disabled_at,omitempty"`
}

var identityMissBytes = []byte(`{"v":1,"_miss":true}`)

var identityGroup singleflight.Group

var errIdentityVersionMismatch = errors.New("identity: version mismatch")

// GetIdentity tries the cache, then loads on miss, caching the result.
// loader returns (nil, nil) for not-found — that result is negatively cached.
func GetIdentity(
  ctx context.Context,
  c CacheStore,
  principalID uuid.UUID,
  loader func(ctx context.Context, id uuid.UUID) (*IdentityCacheEntry, error),
) (*IdentityCacheEntry, error) {
  key := fmt.Sprintf(IdentityKey, principalID)
  b, err := c.Get(ctx, key)
  if err == nil {
    if entry, miss, decErr := decodeIdentity(b); decErr == nil {
      if miss { return nil, ErrNotFound }
      return entry, nil
    }
  } else if !errors.Is(err, ErrNotFound) {
    return nil, err
  }

  v, err, _ := identityGroup.Do(key, func() (any, error) { return loader(ctx, principalID) })
  if err != nil { return nil, err }
  entry, _ := v.(*IdentityCacheEntry)
  if entry == nil {
    _ = c.Set(ctx, key, identityMissBytes, IdentityNotFoundTTL)
    return nil, ErrNotFound
  }
  entry.Version = identityVersion
  payload, _ := json.Marshal(entry)
  _ = c.Set(ctx, key, payload, IdentityTTL)
  return entry, nil
}

// SetIdentity overwrites the cached entry directly.
func SetIdentity(ctx context.Context, c CacheStore, principalID uuid.UUID, entry IdentityCacheEntry) error {
  entry.Version = identityVersion
  payload, err := json.Marshal(entry)
  if err != nil { return err }
  return c.Set(ctx, fmt.Sprintf(IdentityKey, principalID), payload, IdentityTTL)
}

// InvalidateIdentity deletes the cached entry. Used by the identity-cache-invalidator
// consumer (separate issue) on gitscale.identity.events mutations.
func InvalidateIdentity(ctx context.Context, c CacheStore, principalID uuid.UUID) error {
  return c.Delete(ctx, fmt.Sprintf(IdentityKey, principalID))
}

func decodeIdentity(b []byte) (*IdentityCacheEntry, bool, error) {
  var raw struct {
    Version int  `json:"v"`
    Miss    bool `json:"_miss"`
  }
  if err := json.Unmarshal(b, &raw); err != nil { return nil, false, err }
  if raw.Version != identityVersion { return nil, false, errIdentityVersionMismatch }
  if raw.Miss { return nil, true, nil }
  var out IdentityCacheEntry
  if err := json.Unmarshal(b, &out); err != nil { return nil, false, err }
  return &out, false, nil
}
```

Save to `plane/data/cache/identity.go`.

- [ ] **Step 4: Run, expect pass**

Run: `go test ./plane/data/cache/... -run TestGetIdentity -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/cache/identity.go plane/data/cache/identity_test.go
git commit -m "feat(cache): Identity typed helper w/ singleflight + invalidator hook (#13)"
```

---

## Task 9: `SessionQuota` admit pattern (CAS)

**Files:**
- Create: `plane/data/cache/session_quota.go`
- Create: `plane/data/cache/session_quota_test.go`

- [ ] **Step 1: Write failing tests**

```go
package cache

import (
  "context"
  "errors"
  "testing"
  "time"

  "github.com/google/uuid"
)

func TestAdmit_SuccessDecrementsRemaining(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  sid := uuid.New()
  if err := InitSessionQuota(context.Background(), store, sid, 1000); err != nil { t.Fatal(err) }

  if err := Admit(context.Background(), store, sid, 10); err != nil { t.Errorf("Admit: %v", err) }

  q, err := GetSessionQuota(context.Background(), store, sid)
  if err != nil { t.Fatal(err) }
  if q.Remaining != 990 { t.Errorf("remaining = %d, want 990", q.Remaining) }
}

func TestAdmit_InsufficientQuota_ReturnsErrQuotaExceeded(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  sid := uuid.New()
  InitSessionQuota(context.Background(), store, sid, 5)
  if err := Admit(context.Background(), store, sid, 10); !errors.Is(err, ErrQuotaExceeded) {
    t.Errorf("err = %v, want ErrQuotaExceeded", err)
  }
}

func TestAdmit_MissingSession_ReturnsErrNotFound(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  if err := Admit(context.Background(), store, uuid.New(), 1); !errors.Is(err, ErrNotFound) {
    t.Errorf("err = %v, want ErrNotFound", err)
  }
}

func TestAdmit_ConcurrentCalls_SumExactlyMatchInitial(t *testing.T) {
  store := NewMemoryStore(NewFakeClock(time.Now()))
  sid := uuid.New()
  const initial = 100
  InitSessionQuota(context.Background(), store, sid, initial)

  var wg sync.WaitGroup
  var grants int64
  for i := 0; i < 200; i++ {
    wg.Add(1)
    go func() {
      defer wg.Done()
      if err := Admit(context.Background(), store, sid, 1); err == nil {
        atomic.AddInt64(&grants, 1)
      }
    }()
  }
  wg.Wait()

  if grants != initial { t.Errorf("grants = %d, want %d (no over-grant under contention)", grants, initial) }
  q, _ := GetSessionQuota(context.Background(), store, sid)
  if q.Remaining != 0 { t.Errorf("remaining = %d, want 0", q.Remaining) }
}
```

Save to `plane/data/cache/session_quota_test.go`. Add `"sync"`, `"sync/atomic"` imports.

- [ ] **Step 2: Run, expect failure**

Run: `go test ./plane/data/cache/... -run TestAdmit -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package cache

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"
  "time"

  "github.com/google/uuid"
)

const (
  sessionQuotaVersion = 1
  maxQuotaRetries     = 3
  defaultSessionTTL   = 24 * time.Hour
)

type SessionQuota struct {
  Version   int       `json:"v"`
  SessionID string    `json:"session_id"`
  Remaining int64     `json:"remaining"`
  Capacity  int64     `json:"capacity"`
  UpdatedAt time.Time `json:"updated_at"`
}

var (
  ErrQuotaExceeded  = errors.New("session_quota: insufficient remaining")
  ErrQuotaContended = errors.New("session_quota: too many CAS retries")
  ErrQuotaCorrupt   = errors.New("session_quota: cached blob is unparseable or wrong version")
)

// InitSessionQuota seeds the cache with a fresh quota.
func InitSessionQuota(ctx context.Context, c CacheStore, sessionID uuid.UUID, capacity int64) error {
  q := SessionQuota{
    Version: sessionQuotaVersion, SessionID: sessionID.String(),
    Remaining: capacity, Capacity: capacity, UpdatedAt: time.Now().UTC(),
  }
  b, _ := json.Marshal(q)
  return c.Set(ctx, fmt.Sprintf(AgentSessionQuotaKey, sessionID), b, defaultSessionTTL)
}

// GetSessionQuota reads the quota.
func GetSessionQuota(ctx context.Context, c CacheStore, sessionID uuid.UUID) (*SessionQuota, error) {
  b, err := c.Get(ctx, fmt.Sprintf(AgentSessionQuotaKey, sessionID))
  if err != nil { return nil, err }
  var q SessionQuota
  if err := json.Unmarshal(b, &q); err != nil { return nil, ErrQuotaCorrupt }
  if q.Version != sessionQuotaVersion { return nil, ErrQuotaCorrupt }
  return &q, nil
}

// Admit charges `cost` against the session's remaining quota via CAS.
// Retries up to maxQuotaRetries on contention.
func Admit(ctx context.Context, c CacheStore, sessionID uuid.UUID, cost int64) error {
  key := fmt.Sprintf(AgentSessionQuotaKey, sessionID)
  for attempt := 0; attempt < maxQuotaRetries; attempt++ {
    cur, err := c.Get(ctx, key)
    if err != nil { return err } // ErrNotFound bubbles up

    var q SessionQuota
    if err := json.Unmarshal(cur, &q); err != nil || q.Version != sessionQuotaVersion {
      return ErrQuotaCorrupt
    }
    if q.Remaining < cost { return ErrQuotaExceeded }

    q.Remaining -= cost
    q.UpdatedAt = time.Now().UTC()
    next, _ := json.Marshal(q)

    ok, err := c.CompareAndSwap(ctx, key, cur, next, defaultSessionTTL)
    if err != nil { return err }
    if ok { return nil }
    // CAS lost; retry
  }
  return ErrQuotaContended
}
```

Save to `plane/data/cache/session_quota.go`.

- [ ] **Step 4: Run, expect pass**

Run: `go test ./plane/data/cache/... -run TestAdmit -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/cache/session_quota.go plane/data/cache/session_quota_test.go
git commit -m "feat(cache): SessionQuota CAS-based admit (#13)"
```

---

## Task 10: RateLimiter — interface + memory impl

**Files:**
- Create: `plane/data/ratelimit/limiter.go`
- Create: `plane/data/ratelimit/keys.go`
- Create: `plane/data/ratelimit/limiter_compliance.go`
- Create: `plane/data/ratelimit/limiter_memory.go`
- Create: `plane/data/ratelimit/limiter_memory_test.go`
- Create: `plane/data/ratelimit/clock.go` (or re-use a shared clock — see Step 1)

- [ ] **Step 1: Decide clock placement**

Two options: (a) duplicate `Clock`+`FakeClock` in ratelimit pkg; (b) move to a new `plane/data/internal/clock` shared pkg. **Choose (a) — duplicate.** ratelimit and cache are sibling packages; sharing via internal/ adds complexity for ~30 lines of code. Copy the same `Clock`/`FakeClock`/`SystemClock` into `plane/data/ratelimit/clock.go` verbatim.

- [ ] **Step 2: Write `keys.go`**

```go
package ratelimit

// TokenBucketKey identifies a per-principal-per-surface bucket.
// %s = principal UUID; %s = surface enum (e.g. "git_push", "pr_open").
// Surface MUST NOT contain ':' — keep to [a-z_].
const TokenBucketKey = "rl:bucket:%s:%s"
```

- [ ] **Step 3: Write `limiter.go`**

```go
package ratelimit

import "context"

// RateLimiter is a token-bucket rate limiter. Take attempts to consume n tokens
// atomically; granted=false means insufficient tokens.
type RateLimiter interface {
  Take(ctx context.Context, key string, capacity, refillPerSec, n float64) (granted bool, remaining float64, err error)
}
```

- [ ] **Step 4: Write compliance suite**

```go
package ratelimit

import (
  "context"
  "testing"
  "time"
)

type CompliancePack struct {
  NewLimiter func(t *testing.T) (RateLimiter, func(time.Duration))
}

func (p CompliancePack) Run(t *testing.T) {
  t.Run("EmptyBucket_FirstTake_Granted", func(t *testing.T) {
    l, _ := p.NewLimiter(t)
    ok, rem, err := l.Take(context.Background(), "k", 10, 1, 1)
    if err != nil { t.Fatal(err) }
    if !ok { t.Error("first take should be granted") }
    if rem != 9 { t.Errorf("remaining = %v, want 9", rem) }
  })

  t.Run("Exhausted_NextTake_Denied", func(t *testing.T) {
    l, _ := p.NewLimiter(t)
    // Drain 10 tokens
    for i := 0; i < 10; i++ {
      if ok, _, _ := l.Take(context.Background(), "k", 10, 0, 1); !ok {
        t.Fatalf("drain %d: not granted", i)
      }
    }
    ok, _, _ := l.Take(context.Background(), "k", 10, 0, 1)
    if ok { t.Error("expected denied after drain") }
  })

  t.Run("Refill_AdvanceClock_GrantsAgain", func(t *testing.T) {
    l, advance := p.NewLimiter(t)
    // Drain
    for i := 0; i < 10; i++ { l.Take(context.Background(), "k", 10, 1, 1) }
    // Wait 5s with refill 1/sec → +5 tokens
    advance(5 * time.Second)
    ok, rem, _ := l.Take(context.Background(), "k", 10, 1, 1)
    if !ok { t.Error("expected grant after refill") }
    if rem < 3 || rem > 5 { t.Errorf("remaining = %v, want ~4 (5 refilled - 1 taken)", rem) }
  })

  t.Run("Refill_NeverExceedsCapacity", func(t *testing.T) {
    l, advance := p.NewLimiter(t)
    // Take then advance way past capacity
    l.Take(context.Background(), "k", 5, 1000, 1) // refill 1000/s
    advance(time.Hour)
    ok, rem, _ := l.Take(context.Background(), "k", 5, 1000, 1)
    if !ok { t.Error("grant expected") }
    if rem > 4 { t.Errorf("remaining = %v, want ≤ 4 (capacity=5, took 1)", rem) }
  })
}
```

- [ ] **Step 5: Implement memory limiter**

```go
package ratelimit

import (
  "context"
  "sync"
)

type memBucket struct {
  tokens float64
  lastNs int64
}

type MemoryLimiter struct {
  mu    sync.Mutex
  data  map[string]*memBucket
  clock Clock
}

func NewMemoryLimiter(clock Clock) *MemoryLimiter {
  if clock == nil { clock = SystemClock }
  return &MemoryLimiter{data: map[string]*memBucket{}, clock: clock}
}

func (m *MemoryLimiter) Take(ctx context.Context, key string, capacity, refillPerSec, n float64) (bool, float64, error) {
  m.mu.Lock(); defer m.mu.Unlock()
  now := m.clock.Now()
  nowNs := now.UnixNano()
  b, ok := m.data[key]
  if !ok {
    b = &memBucket{tokens: capacity, lastNs: nowNs}
    m.data[key] = b
  }
  elapsedSec := float64(nowNs-b.lastNs) / 1e9
  b.tokens = b.tokens + elapsedSec*refillPerSec
  if b.tokens > capacity { b.tokens = capacity }
  b.lastNs = nowNs

  if b.tokens >= n {
    b.tokens -= n
    return true, b.tokens, nil
  }
  return false, b.tokens, nil
}
```

Save to `plane/data/ratelimit/limiter_memory.go`.

- [ ] **Step 6: Wire memory limiter into compliance suite**

```go
package ratelimit

import (
  "testing"
  "time"
)

func TestMemoryLimiter_Compliance(t *testing.T) {
  pack := CompliancePack{
    NewLimiter: func(t *testing.T) (RateLimiter, func(time.Duration)) {
      fc := NewFakeClock(time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC))
      return NewMemoryLimiter(fc), fc.Advance
    },
  }
  pack.Run(t)
}
```

Save to `plane/data/ratelimit/limiter_memory_test.go`.

- [ ] **Step 7: Run, expect pass**

Run: `go test ./plane/data/ratelimit/... -run TestMemoryLimiter -v`
Expected: 4 sub-tests PASS.

- [ ] **Step 8: Commit**

```bash
git add plane/data/ratelimit
git commit -m "feat(ratelimit): RateLimiter interface + memory impl + compliance suite (#13)"
```

---

## Task 11: Redis token-bucket limiter

**Files:**
- Create: `plane/data/ratelimit/lua/token_bucket.lua`
- Create: `plane/data/ratelimit/limiter_redis.go`
- Create: `plane/data/ratelimit/limiter_redis_test.go`

- [ ] **Step 1: Write the Lua script (verbatim from spec §9)**

```lua
-- plane/data/ratelimit/lua/token_bucket.lua
-- KEYS[1] = bucket key
-- ARGV[1] = capacity
-- ARGV[2] = refill_per_sec
-- ARGV[3] = now_unix_ms
-- ARGV[4] = take_n
-- ARGV[5] = ttl_ms
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

- [ ] **Step 2: Implement Redis limiter**

```go
package ratelimit

import (
  "context"
  _ "embed"
  "strconv"
  "time"

  "github.com/redis/go-redis/v9"
)

//go:embed lua/token_bucket.lua
var tokenBucketSrc string

var tokenBucketScript = redis.NewScript(tokenBucketSrc)

type RedisLimiter struct {
  client redis.Cmdable
  clock  Clock
  ttl    time.Duration
}

// NewRedisLimiter wires either a single-Redis or Cluster client.
// ttl is the bucket key's expiry — should be ≥ 2× the slowest refill window.
func NewRedisLimiter(client redis.Cmdable, clock Clock, ttl time.Duration) *RedisLimiter {
  if clock == nil { clock = SystemClock }
  if ttl == 0 { ttl = 5 * time.Minute }
  return &RedisLimiter{client: client, clock: clock, ttl: ttl}
}

func (r *RedisLimiter) Take(ctx context.Context, key string, capacity, refillPerSec, n float64) (bool, float64, error) {
  now := r.clock.Now().UnixMilli()
  res, err := tokenBucketScript.Run(ctx, r.client,
    []string{key},
    capacity, refillPerSec, now, n, int(r.ttl/time.Millisecond),
  ).Result()
  if err != nil { return false, 0, err }
  arr, ok := res.([]interface{})
  if !ok || len(arr) != 2 { return false, 0, nil }
  granted, _ := arr[0].(int64)
  remStr, _ := arr[1].(string)
  rem, _ := strconv.ParseFloat(remStr, 64)
  return granted == 1, rem, nil
}
```

Save to `plane/data/ratelimit/limiter_redis.go`.

- [ ] **Step 3: Write integration test**

```go
//go:build integration

package ratelimit

import (
  "context"
  "testing"
  "time"

  "github.com/redis/go-redis/v9"
  "github.com/testcontainers/testcontainers-go"
  rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestRedisLimiter_Integration(t *testing.T) {
  ctx := context.Background()
  c, err := rediscontainer.RunContainer(ctx, testcontainers.WithImage("redis:7-alpine"))
  if err != nil { t.Fatal(err) }
  defer c.Terminate(ctx)
  url, _ := c.ConnectionString(ctx)
  client := redis.NewClient(&redis.Options{Addr: url[len("redis://"):]})
  defer client.Close()

  pack := CompliancePack{
    NewLimiter: func(t *testing.T) (RateLimiter, func(time.Duration)) {
      client.FlushAll(ctx)
      fc := NewFakeClock(time.Now())
      return NewRedisLimiter(client, fc, time.Hour), fc.Advance
    },
  }
  pack.Run(t)
}
```

Save to `plane/data/ratelimit/limiter_redis_test.go`.

- [ ] **Step 4: Run integration**

Run: `go test -tags integration ./plane/data/ratelimit/... -run TestRedisLimiter -v -timeout 120s`
Expected: 4 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/ratelimit/lua plane/data/ratelimit/limiter_redis.go plane/data/ratelimit/limiter_redis_test.go
git commit -m "feat(ratelimit): Redis Lua-driven token-bucket impl (#13)"
```

---

## Task 12: RateLimiter env-namespace wrapper

**Files:**
- Create: `plane/data/ratelimit/namespace.go`
- Create: `plane/data/ratelimit/namespace_test.go`

- [ ] **Step 1: Implement**

```go
package ratelimit

import "context"

type namespacedLimiter struct {
  inner  RateLimiter
  prefix string
}

func WithNamespace(inner RateLimiter, env string) RateLimiter {
  return &namespacedLimiter{inner: inner, prefix: "gitscale:" + env + ":"}
}

func (n *namespacedLimiter) Take(ctx context.Context, key string, capacity, refillPerSec, take float64) (bool, float64, error) {
  return n.inner.Take(ctx, n.prefix+key, capacity, refillPerSec, take)
}
```

- [ ] **Step 2: Write test that verifies prefix application**

```go
package ratelimit

import (
  "context"
  "testing"
  "time"
)

type captureLimiter struct{ lastKey string }
func (c *captureLimiter) Take(ctx context.Context, key string, _, _, _ float64) (bool, float64, error) {
  c.lastKey = key
  return true, 0, nil
}

func TestWithNamespace_PrependsPrefix(t *testing.T) {
  cap := &captureLimiter{}
  l := WithNamespace(cap, "test")
  l.Take(context.Background(), "rl:bucket:abc:git_push", 1, 1, 1)
  if cap.lastKey != "gitscale:test:rl:bucket:abc:git_push" {
    t.Errorf("got %q, want gitscale:test:rl:bucket:abc:git_push", cap.lastKey)
  }
}

func TestWithNamespace_Compliance(t *testing.T) {
  pack := CompliancePack{
    NewLimiter: func(t *testing.T) (RateLimiter, func(time.Duration)) {
      fc := NewFakeClock(time.Now())
      return WithNamespace(NewMemoryLimiter(fc), "test"), fc.Advance
    },
  }
  pack.Run(t)
}
```

- [ ] **Step 3: Run, expect pass**

Run: `go test ./plane/data/ratelimit/... -v -count=1`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add plane/data/ratelimit/namespace.go plane/data/ratelimit/namespace_test.go
git commit -m "feat(ratelimit): WithNamespace wrapper (#13)"
```

---

## Task 13: Final verification

- [ ] **Step 1: Full unit suite**

Run: `go test ./plane/data/cache/... ./plane/data/ratelimit/... -v -count=1`
Expected: all PASS.

- [ ] **Step 2: Full integration suite**

Run: `go test -tags integration ./plane/data/cache/... ./plane/data/ratelimit/... -v -count=1 -timeout 5m`
Expected: all PASS.

- [ ] **Step 3: Lint**

Run: `make lint && make lint-md`
Expected: clean.

---

## Acceptance criteria (verifies spec §17)

- [ ] `RateLimiter` is a separate interface in `plane/data/ratelimit/` (verified by file layout)
- [ ] `IncrBy` is **not** on `CacheStore` (verified by `store.go`)
- [ ] Agent-session quota mutated via `CompareAndSwap` on `SessionQuota` JSON blob (verified by `TestAdmit_*`)
- [ ] `MGet` is on `CacheStore` (verified by compliance suite)
- [ ] `Get` returns `ErrNotFound` (verified by `TestGetMissing_*`)
- [ ] `WithNamespace(inner, env)` wrapper for both interfaces (verified by `TestNamespacedStore_*` + `TestWithNamespace_*`)
- [ ] Typed payloads carry `Version int` (verified by `TestGetRepoLocation_VersionMismatch`)
- [ ] Negative caching with shorter TTL (verified by `TestGetRepoLocation_LoaderReturnsNil_NegativeCache`)
- [ ] Loader calls wrapped in `singleflight.Group` (verified by code inspection)
- [ ] `CompareAndSwap` and token bucket `Take` each implemented as a single Lua script (verified by `cas.lua`, `token_bucket.lua`)
- [ ] Memory impl uses `clock.Clock` (verified by `NewFakeClock` use)
- [ ] Compliance suite runs identical cases against both impls (verified by `store_compliance.go` + `limiter_compliance.go`)
- [ ] `RedisConfig.UseCluster` toggles between single + cluster modes (verified by `store_redis.go`)
- [ ] `TokenBucketKey` lives in `plane/data/ratelimit/keys.go`
- [ ] Token bucket state stored as Redis HASH (verified by `token_bucket.lua` HMGET/HMSET)
