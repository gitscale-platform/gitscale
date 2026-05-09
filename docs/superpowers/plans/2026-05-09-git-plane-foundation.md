# Git Plane Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bootstrap `plane/git` from its doc.go stub into a working Gitaly RPC proxy with repo-location lookup, a pre-receive hook interface, and two-tier metering (Redis enforcement + outbox reconciliation).

**Architecture:** `GitalyProxy` holds a connection pool, a `RepoLocator` (CacheStore-first via the existing `cache.GetRepoLocation` helper, MetadataStore fallback), and an injected `HookHandler`. `ReceivePack` calls `HookHandler.PreReceive` inline before forwarding to Gitaly; on success it records a metering event to Redis and the outbox. A new `git` domain outbox table drains to a new Kafka topic via the existing outbox consumer.

**Tech Stack:** Go 1.25, `google.golang.org/grpc` v1.81 (already in go.mod), `gitlab.com/gitlab-org/gitaly/v16` (added in Task 1), existing `plane/data/cache`, `plane/data/store`, `plane/data/outbox`, `plane/data/kafka`.

---

## File map

| File | Action | Responsibility |
|------|--------|---------------|
| `plane/git/client/pool.go` | Create | gRPC connection pool keyed by target addr |
| `plane/git/client/pool_test.go` | Create | Pool unit tests |
| `plane/git/locator/locator.go` | Create | `RepoLocator` interface + `FileServerAddr` type |
| `plane/git/locator/cache.go` | Create | CacheStore-backed locator (wraps `cache.GetRepoLocation`) |
| `plane/git/locator/postgres.go` | Create | MetadataStore fallback locator |
| `plane/git/locator/locator_test.go` | Create | Locator unit tests |
| `plane/git/rpc/rpc.go` | Create | `GitRPC` interface, `RepoRef`, `RefUpdate` types |
| `plane/git/hook/hook.go` | Create | `HookHandler` interface |
| `plane/git/hook/noop.go` | Create | `NoopHookHandler` |
| `plane/git/rpc/proxy.go` | Create | `GitalyProxy` impl — InfoRefs, UploadPack, ReceivePack |
| `plane/git/rpc/proxy_test.go` | Create | Proxy unit tests (in-process stub Gitaly) |
| `plane/git/metering/events.go` | Create | `MeteringEvent` type |
| `plane/git/metering/counter.go` | Create | `TwoTierCounter` — Redis incr + outbox write |
| `plane/git/metering/counter_test.go` | Create | Counter unit tests |
| `plane/git/sink/sink.go` | Create | `AnalyticsSink` interface |
| `plane/git/sink/stub.go` | Create | In-memory stub |
| `plane/data/store/domain.go` | Modify | Add `DomainGit` constant |
| `plane/data/migrations/010_git_domain.sql` | Create | `git` schema + `git_outbox` table |
| `plane/data/kafka/topics.go` | Modify | Add `TopicGitMeteringEvents` |
| `plane/data/outbox/wiring/wiring.go` | Modify | Add git domain consumer |
| `plane/git/integration_test.go` | Create | End-to-end test (real Postgres + real Redis) |
| `docker-compose.yml` | Modify | Add `gitaly` service |
| `config/gitaly/config.toml` | Create | Minimal Gitaly dev config |

---

## Task 1: Add Gitaly dependency + Gitaly connection pool

**Files:**
- Create: `plane/git/client/pool.go`
- Create: `plane/git/client/pool_test.go`

- [ ] **Step 1: Add Gitaly gRPC dependency**

```bash
go get gitlab.com/gitlab-org/gitaly/v16@latest
go mod tidy
```

Expected: `go.mod` updated with `gitlab.com/gitlab-org/gitaly/v16`.

- [ ] **Step 2: Write failing test**

```go
// plane/git/client/pool_test.go
package client_test

import (
	"context"
	"net"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestPool_SameConnReturned(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)

	addr := lis.Addr().String()
	c1, err := pool.Conn(context.Background(), addr)
	require.NoError(t, err)
	c2, err := pool.Conn(context.Background(), addr)
	require.NoError(t, err)
	require.Same(t, c1, c2, "pool must return the same conn for the same addr")
}

func TestPool_Close_IdempotentAfterClose(t *testing.T) {
	pool := client.NewGitalyPool()
	pool.Close()
	pool.Close() // must not panic
}
```

- [ ] **Step 3: Run test — expect compile failure**

```bash
go test ./plane/git/client/... 2>&1 | head -20
```

Expected: `cannot find package "github.com/gitscale-platform/gitscale/plane/git/client"`

- [ ] **Step 4: Implement pool**

```go
// plane/git/client/pool.go
package client

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GitalyPool maintains one gRPC ClientConn per Gitaly target address.
// Safe for concurrent use.
type GitalyPool struct {
	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// NewGitalyPool returns an empty pool.
func NewGitalyPool() *GitalyPool {
	return &GitalyPool{conns: make(map[string]*grpc.ClientConn)}
}

// Conn returns the existing connection to target, or dials a new one.
// target must be a host:port string.
func (p *GitalyPool) Conn(ctx context.Context, target string) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.conns[target]; ok {
		return c, nil
	}
	c, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gitaly pool: dial %s: %w", target, err)
	}
	p.conns[target] = c
	return c, nil
}

// Close closes all pooled connections. Safe to call multiple times.
func (p *GitalyPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./plane/git/client/... -v
```

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git add plane/git/client/ go.mod go.sum
git commit -m "feat(git): Gitaly gRPC connection pool

Closes #107"
```

---

## Task 2: RepoLocator interface + cache-backed implementation

**Files:**
- Create: `plane/git/locator/locator.go`
- Create: `plane/git/locator/cache.go`
- Create: `plane/git/locator/locator_test.go`

- [ ] **Step 1: Write failing tests**

```go
// plane/git/locator/locator_test.go
package locator_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCacheLocator_Hit(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	repoID := uuid.New()

	// Prime the cache directly.
	err := cache.SetRepoLocation(context.Background(), mem, repoID, cache.RepoLocation{
		ReplicaSetID: "rs-west-1",
		HomeRegion:   "us-west-2",
	})
	require.NoError(t, err)

	loc := locator.NewCacheLocator(mem, &alwaysMissLocator{})
	addr, err := loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, "rs-west-1", addr.ReplicaSetID)
}

func TestCacheLocator_MissFallsThrough(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	repoID := uuid.New()

	fallback := &recordingLocator{
		result: locator.FileServerAddr{ReplicaSetID: "rs-east-1", HomeRegion: "us-east-1", Addr: "rs-east-1:8075"},
	}
	loc := locator.NewCacheLocator(mem, fallback)

	addr, err := loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, "rs-east-1", addr.ReplicaSetID)
	require.Equal(t, 1, fallback.calls, "fallback must be called exactly once")

	// Second call must hit cache, not fallback.
	_, err = loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, 1, fallback.calls, "cache must absorb second call")
}

func TestCacheLocator_NotFound(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	repoID := uuid.New()
	loc := locator.NewCacheLocator(mem, &alwaysMissLocator{})
	_, err := loc.Resolve(context.Background(), repoID.String())
	require.ErrorIs(t, err, locator.ErrRepoNotFound)
}

// alwaysMissLocator simulates a MetadataStore that knows nothing.
type alwaysMissLocator struct{}

func (l *alwaysMissLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	return locator.FileServerAddr{}, locator.ErrRepoNotFound
}

// recordingLocator records how many times it was called.
type recordingLocator struct {
	result locator.FileServerAddr
	calls  int
}

func (l *recordingLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	l.calls++
	return l.result, nil
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./plane/git/locator/... 2>&1 | head -20
```

- [ ] **Step 3: Implement interface + types**

```go
// plane/git/locator/locator.go
package locator

import "errors"

// ErrRepoNotFound is returned when no repo with the given ID is known.
var ErrRepoNotFound = errors.New("locator: repo not found")

// FileServerAddr is the resolved routing address for a repository.
type FileServerAddr struct {
	ReplicaSetID string
	HomeRegion   string
	Addr         string // host:port of the Gitaly node
}

// RepoLocator resolves a repo_id string to its file-server address.
type RepoLocator interface {
	Resolve(ctx context.Context, repoID string) (FileServerAddr, error)
}
```

Add missing import:
```go
// plane/git/locator/locator.go
package locator

import (
	"context"
	"errors"
)

// ErrRepoNotFound is returned when no repo with the given ID is known.
var ErrRepoNotFound = errors.New("locator: repo not found")

// FileServerAddr is the resolved routing address for a repository.
type FileServerAddr struct {
	ReplicaSetID string
	HomeRegion   string
	Addr         string // host:port of the Gitaly node
}

// RepoLocator resolves a repo_id string to its file-server address.
type RepoLocator interface {
	Resolve(ctx context.Context, repoID string) (FileServerAddr, error)
}
```

- [ ] **Step 4: Implement CacheLocator**

```go
// plane/git/locator/cache.go
package locator

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/google/uuid"
)

// CacheLocator resolves repo locations via cache.GetRepoLocation, falling
// back to next on a miss. Write-through ensures future lookups hit the cache.
type CacheLocator struct {
	store cache.CacheStore
	next  RepoLocator
}

// NewCacheLocator returns a CacheLocator that uses store as L1 and next as L2.
func NewCacheLocator(store cache.CacheStore, next RepoLocator) *CacheLocator {
	return &CacheLocator{store: store, next: next}
}

func (l *CacheLocator) Resolve(ctx context.Context, repoID string) (FileServerAddr, error) {
	id, err := uuid.Parse(repoID)
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: invalid repo_id %q: %w", repoID, err)
	}

	loc, err := cache.GetRepoLocation(ctx, l.store, id, func(ctx context.Context, id uuid.UUID) (*cache.RepoLocation, error) {
		addr, nextErr := l.next.Resolve(ctx, id.String())
		if errors.Is(nextErr, ErrRepoNotFound) {
			return nil, nil // negative-cache the miss
		}
		if nextErr != nil {
			return nil, nextErr
		}
		return &cache.RepoLocation{
			ReplicaSetID: addr.ReplicaSetID,
			HomeRegion:   addr.HomeRegion,
		}, nil
	})

	if errors.Is(err, cache.ErrNotFound) {
		return FileServerAddr{}, ErrRepoNotFound
	}
	if err != nil {
		return FileServerAddr{}, err
	}

	return FileServerAddr{
		ReplicaSetID: loc.ReplicaSetID,
		HomeRegion:   loc.HomeRegion,
		Addr:         loc.ReplicaSetID + ":8075",
	}, nil
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./plane/git/locator/... -v
```

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git add plane/git/locator/
git commit -m "feat(git): repo-location cache locator

Closes #107"
```

---

## Task 3: MetadataStore fallback locator

**Files:**
- Create: `plane/git/locator/postgres.go`

- [ ] **Step 1: Write failing test (add to locator_test.go)**

```go
// Append to plane/git/locator/locator_test.go

func TestMetadataLocator_Found(t *testing.T) {
	repoID := uuid.New()
	mds := &stubMetadataStore{repos: map[uuid.UUID]store.Repository{
		repoID: {
			ID:           repoID,
			ReplicaSetID: "rs-us-1",
			HomeRegion:   "us-west-2",
		},
	}}
	loc := locator.NewMetadataLocator(mds)
	addr, err := loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, "rs-us-1", addr.ReplicaSetID)
	require.Equal(t, "rs-us-1:8075", addr.Addr)
}

func TestMetadataLocator_NotFound(t *testing.T) {
	mds := &stubMetadataStore{repos: map[uuid.UUID]store.Repository{}}
	loc := locator.NewMetadataLocator(mds)
	_, err := loc.Resolve(context.Background(), uuid.New().String())
	require.ErrorIs(t, err, locator.ErrRepoNotFound)
}

// stubMetadataStore satisfies the store.MetadataStore interface minimally.
// It only needs Repositories() for these tests.
type stubMetadataStore struct {
	repos map[uuid.UUID]store.Repository
}

func (s *stubMetadataStore) Transact(_ context.Context, _ func(store.Tx) error) error {
	return errors.New("stub: transact not implemented")
}
func (s *stubMetadataStore) Identity() store.IdentityReader   { return nil }
func (s *stubMetadataStore) Billing() store.BillingReader     { return nil }
func (s *stubMetadataStore) Repositories() store.RepositoryReader {
	return &stubRepoReader{repos: s.repos}
}

type stubRepoReader struct {
	repos map[uuid.UUID]store.Repository
}

func (r *stubRepoReader) GetByID(_ context.Context, id uuid.UUID) (*store.Repository, error) {
	if repo, ok := r.repos[id]; ok {
		return &repo, nil
	}
	return nil, nil
}

func (r *stubRepoReader) GetBySlug(_ context.Context, _ string) (*store.Repository, error) {
	return nil, errors.New("stub: GetBySlug not implemented")
}
```

Add imports to `locator_test.go`:
```go
import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./plane/git/locator/... 2>&1 | head -20
```

- [ ] **Step 3: Implement MetadataLocator**

```go
// plane/git/locator/postgres.go
package locator

import (
	"context"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// MetadataLocator resolves repo locations from the MetadataStore.
// Use as the fallback inside a CacheLocator.
type MetadataLocator struct {
	store store.MetadataStore
}

// NewMetadataLocator returns a locator backed by store.
func NewMetadataLocator(store store.MetadataStore) *MetadataLocator {
	return &MetadataLocator{store: store}
}

func (l *MetadataLocator) Resolve(ctx context.Context, repoID string) (FileServerAddr, error) {
	id, err := uuid.Parse(repoID)
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: invalid repo_id %q: %w", repoID, err)
	}
	repo, err := l.store.Repositories().GetByID(ctx, id)
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: metadata lookup: %w", err)
	}
	if repo == nil {
		return FileServerAddr{}, ErrRepoNotFound
	}
	return FileServerAddr{
		ReplicaSetID: repo.ReplicaSetID,
		HomeRegion:   repo.HomeRegion,
		Addr:         repo.ReplicaSetID + ":8075",
	}, nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./plane/git/locator/... -v
```

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git add plane/git/locator/
git commit -m "feat(git): MetadataStore fallback locator

Closes #107"
```

---

## Task 4: GitRPC interface + HookHandler

**Files:**
- Create: `plane/git/rpc/rpc.go`
- Create: `plane/git/hook/hook.go`
- Create: `plane/git/hook/noop.go`

- [ ] **Step 1: Create GitRPC types and interface**

```go
// plane/git/rpc/rpc.go
package rpc

import (
	"context"
	"io"
)

// RepoRef identifies a repository for a Git operation.
type RepoRef struct {
	RepoID  string // UUID string
	AgentID string // empty for human operations
}

// RefUpdate is a single ref change within a push.
type RefUpdate struct {
	RefName string
	OldOID  string
	NewOID  string
}

// GitRPC is the public surface of plane/git.
// Callers never import Gitaly proto directly.
type GitRPC interface {
	// InfoRefs returns the smart-HTTP info/refs response for service
	// ("git-upload-pack" or "git-receive-pack").
	InfoRefs(ctx context.Context, repo RepoRef, service string) (io.ReadCloser, error)

	// UploadPack proxies a git-upload-pack (fetch/clone) request.
	UploadPack(ctx context.Context, repo RepoRef, r io.Reader) (io.ReadCloser, error)

	// ReceivePack proxies a git-receive-pack (push) request.
	// Calls HookHandler.PreReceive before forwarding to Gitaly.
	ReceivePack(ctx context.Context, repo RepoRef, updates []RefUpdate, r io.Reader) (io.ReadCloser, error)
}
```

- [ ] **Step 2: Create HookHandler interface + noop**

```go
// plane/git/hook/hook.go
package hook

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/rpc"
)

// HookHandler is called synchronously inside ReceivePack before the push is
// forwarded to Gitaly. A non-nil error rejects the push; the error message
// is returned to the Git client.
type HookHandler interface {
	PreReceive(ctx context.Context, repo rpc.RepoRef, updates []rpc.RefUpdate) error
}
```

```go
// plane/git/hook/noop.go
package hook

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/rpc"
)

// NoopHookHandler passes every push unconditionally.
// Used as default until AGENTS.md enforcement wires in (#114).
type NoopHookHandler struct{}

func (NoopHookHandler) PreReceive(_ context.Context, _ rpc.RepoRef, _ []rpc.RefUpdate) error {
	return nil
}
```

- [ ] **Step 3: Verify compilation**

```bash
go build ./plane/git/...
```

Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add plane/git/rpc/ plane/git/hook/
git commit -m "feat(git): GitRPC interface and HookHandler

Closes #107"
```

---

## Task 5: GitalyProxy — InfoRefs and UploadPack

**Files:**
- Create: `plane/git/rpc/proxy.go`
- Create: `plane/git/rpc/proxy_test.go`

- [ ] **Step 1: Write failing tests for InfoRefs and UploadPack**

```go
// plane/git/rpc/proxy_test.go
package rpc_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/hook"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	gitplanerpc "github.com/gitscale-platform/gitscale/plane/git/rpc"
	"github.com/stretchr/testify/require"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
	"google.golang.org/grpc"
)

// fakeGitalyServer is a minimal SmartHTTP server for testing.
type fakeGitalyServer struct {
	gitalypb.UnimplementedSmartHTTPServiceServer
	infoRefsData []byte
	uploadData   []byte
}

func (s *fakeGitalyServer) InfoRefs(req *gitalypb.InfoRefsRequest, stream gitalypb.SmartHTTPService_InfoRefsServer) error {
	return stream.Send(&gitalypb.InfoRefsResponse{Data: s.infoRefsData})
}

func (s *fakeGitalyServer) PostUploadPack(stream gitalypb.SmartHTTPService_PostUploadPackServer) error {
	_, _ = stream.Recv() // consume request
	return stream.Send(&gitalypb.PostUploadPackResponse{Data: s.uploadData})
}

func startFakeGitaly(t *testing.T, srv *fakeGitalyServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	gitalypb.RegisterSmartHTTPServiceServer(s, srv)
	go s.Serve(lis)
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func newTestProxy(t *testing.T, gitalyAddr string) gitplanerpc.GitRPC {
	t.Helper()
	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)

	repoID := "00000000-0000-0000-0000-000000000001"
	fixedLocator := &fixedAddrLocator{addr: locator.FileServerAddr{
		ReplicaSetID: "test-rs",
		HomeRegion:   "us-test",
		Addr:         gitalyAddr,
	}}
	return gitplanerpc.NewGitalyProxy(pool, fixedLocator, hook.NoopHookHandler{}, metering.NewNoopCounter())
}

type fixedAddrLocator struct{ addr locator.FileServerAddr }

func (f *fixedAddrLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	return f.addr, nil
}

func TestProxy_InfoRefs(t *testing.T) {
	srv := &fakeGitalyServer{infoRefsData: []byte("# service=git-upload-pack\n")}
	addr := startFakeGitaly(t, srv)
	proxy := newTestProxy(t, addr)

	rc, err := proxy.InfoRefs(context.Background(), gitplanerpc.RepoRef{RepoID: "test"}, "git-upload-pack")
	require.NoError(t, err)
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	require.Equal(t, srv.infoRefsData, data)
}

func TestProxy_UploadPack(t *testing.T) {
	srv := &fakeGitalyServer{uploadData: []byte("PACK...")}
	addr := startFakeGitaly(t, srv)
	proxy := newTestProxy(t, addr)

	rc, err := proxy.UploadPack(context.Background(), gitplanerpc.RepoRef{RepoID: "test"}, bytes.NewReader([]byte("want abc")))
	require.NoError(t, err)
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	require.Equal(t, srv.uploadData, data)
}

func TestProxy_LocatorNotFound(t *testing.T) {
	pool := client.NewGitalyPool()
	proxy := gitplanerpc.NewGitalyProxy(pool, &notFoundLocator{}, hook.NoopHookHandler{}, metering.NewNoopCounter())
	_, err := proxy.InfoRefs(context.Background(), gitplanerpc.RepoRef{RepoID: "missing"}, "git-upload-pack")
	require.ErrorContains(t, err, "not found")
}

type notFoundLocator struct{}
func (n *notFoundLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	return locator.FileServerAddr{}, locator.ErrRepoNotFound
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./plane/git/rpc/... 2>&1 | head -20
```

- [ ] **Step 3: Implement GitalyProxy (InfoRefs + UploadPack only)**

```go
// plane/git/rpc/proxy.go
package rpc

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/hook"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GitalyProxy implements GitRPC by forwarding to Gitaly via the pool.
type GitalyProxy struct {
	pool    *client.GitalyPool
	locator locator.RepoLocator
	hook    hook.HookHandler
	meter   metering.Counter
}

// NewGitalyProxy constructs a proxy. All arguments are required.
func NewGitalyProxy(
	pool *client.GitalyPool,
	loc locator.RepoLocator,
	h hook.HookHandler,
	meter metering.Counter,
) *GitalyProxy {
	return &GitalyProxy{pool: pool, locator: loc, hook: h, meter: meter}
}

func (p *GitalyProxy) resolve(ctx context.Context, repoID string) (locator.FileServerAddr, *gitalypb.Repository, error) {
	addr, err := p.locator.Resolve(ctx, repoID)
	if err != nil {
		return locator.FileServerAddr{}, nil, status.Errorf(codes.NotFound, "git: repo %s not found", repoID)
	}
	repo := &gitalypb.Repository{
		StorageName:  addr.ReplicaSetID,
		RelativePath: repoID + ".git",
	}
	return addr, repo, nil
}

func (p *GitalyProxy) InfoRefs(ctx context.Context, ref RepoRef, service string) (io.ReadCloser, error) {
	addr, gRepo, err := p.resolve(ctx, ref.RepoID)
	if err != nil {
		return nil, err
	}
	conn, err := p.pool.Conn(ctx, addr.Addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "git: gitaly unavailable: %v", err)
	}
	cl := gitalypb.NewSmartHTTPServiceClient(conn)
	stream, err := cl.InfoRefs(ctx, &gitalypb.InfoRefsRequest{
		Repository:  gRepo,
		GitProtocol: service,
	})
	if err != nil {
		return nil, fmt.Errorf("git: info_refs: %w", err)
	}
	return streamToReader(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return resp.Data, nil
	}), nil
}

func (p *GitalyProxy) UploadPack(ctx context.Context, ref RepoRef, r io.Reader) (io.ReadCloser, error) {
	addr, gRepo, err := p.resolve(ctx, ref.RepoID)
	if err != nil {
		return nil, err
	}
	conn, err := p.pool.Conn(ctx, addr.Addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "git: gitaly unavailable: %v", err)
	}
	cl := gitalypb.NewSmartHTTPServiceClient(conn)
	stream, err := cl.PostUploadPack(ctx)
	if err != nil {
		return nil, fmt.Errorf("git: upload_pack: %w", err)
	}
	body, _ := io.ReadAll(r)
	if err := stream.Send(&gitalypb.PostUploadPackRequest{
		Repository: gRepo,
		Data:       body,
	}); err != nil {
		return nil, fmt.Errorf("git: upload_pack send: %w", err)
	}
	_ = stream.CloseSend()
	return streamToReader(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return resp.Data, nil
	}), nil
}

// ReceivePack — implemented in Task 6.
func (p *GitalyProxy) ReceivePack(_ context.Context, _ RepoRef, _ []RefUpdate, _ io.Reader) (io.ReadCloser, error) {
	return nil, status.Error(codes.Unimplemented, "git: receive_pack not yet implemented")
}

// streamToReader returns an io.ReadCloser that drains chunks from next.
// next should return (nil, io.EOF) to signal end-of-stream.
func streamToReader(next func() ([]byte, error)) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for {
			chunk, err := next()
			if err == io.EOF {
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, werr := pw.Write(chunk); werr != nil {
				return
			}
		}
	}()
	return pr
}

// NoopCounter satisfies metering.Counter for tests that don't need metering.
// Defined here to avoid circular imports in proxy_test.go; the real Counter
// lives in plane/git/metering (wired in Task 8).
var _ = bytes.NewReader // silence unused import lint
```

- [ ] **Step 4: Create metering.Counter stub for compilation**

```go
// plane/git/metering/counter.go (stub — full impl in Task 8)
package metering

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/rpc"
)

// Counter records a metering event for a completed Git operation.
type Counter interface {
	Record(ctx context.Context, ref rpc.RepoRef, op string, bytes int64, packObjects int64, refUpdates int) error
}

// NoopCounter discards all events. Used in tests and as a safe default.
type noopCounter struct{}

func NewNoopCounter() Counter { return noopCounter{} }

func (noopCounter) Record(_ context.Context, _ rpc.RepoRef, _ string, _ int64, _ int64, _ int) error {
	return nil
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./plane/git/rpc/... -v
```

Expected: `TestProxy_InfoRefs PASS`, `TestProxy_UploadPack PASS`, `TestProxy_LocatorNotFound PASS`

- [ ] **Step 6: Commit**

```bash
git add plane/git/rpc/ plane/git/metering/counter.go
git commit -m "feat(git): GitalyProxy InfoRefs + UploadPack

Closes #107"
```

---

## Task 6: GitalyProxy — ReceivePack with hook invocation

**Files:**
- Modify: `plane/git/rpc/proxy.go`
- Modify: `plane/git/rpc/proxy_test.go`

- [ ] **Step 1: Write failing tests for ReceivePack**

```go
// Append to plane/git/rpc/proxy_test.go

func (s *fakeGitalyServer) PostReceivePack(stream gitalypb.SmartHTTPService_PostReceivePackServer) error {
	_, _ = stream.Recv() // consume header
	_, _ = stream.Recv() // consume body
	return stream.Send(&gitalypb.PostReceivePackResponse{Data: []byte("ok")})
}

func TestProxy_ReceivePack_NoHook(t *testing.T) {
	srv := &fakeGitalyServer{}
	addr := startFakeGitaly(t, srv)
	proxy := newTestProxy(t, addr)

	updates := []gitplanerpc.RefUpdate{{RefName: "refs/heads/main", OldOID: "abc", NewOID: "def"}}
	rc, err := proxy.ReceivePack(context.Background(), gitplanerpc.RepoRef{RepoID: "test"}, updates, bytes.NewReader([]byte("data")))
	require.NoError(t, err)
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	require.Equal(t, []byte("ok"), data)
}

func TestProxy_ReceivePack_HookRejects(t *testing.T) {
	srv := &fakeGitalyServer{}
	addr := startFakeGitaly(t, srv)
	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)
	fixedLoc := &fixedAddrLocator{addr: locator.FileServerAddr{Addr: addr}}
	proxy := gitplanerpc.NewGitalyProxy(pool, fixedLoc, &rejectHook{msg: "never push to main"}, metering.NewNoopCounter())

	updates := []gitplanerpc.RefUpdate{{RefName: "refs/heads/main"}}
	_, err := proxy.ReceivePack(context.Background(), gitplanerpc.RepoRef{RepoID: "test"}, updates, bytes.NewReader(nil))
	require.Error(t, err)
	require.ErrorContains(t, err, "never push to main")
}

type rejectHook struct{ msg string }

func (h *rejectHook) PreReceive(_ context.Context, _ gitplanerpc.RepoRef, _ []gitplanerpc.RefUpdate) error {
	return fmt.Errorf("%s", h.msg)
}
```

Add `fmt` to proxy_test.go imports.

- [ ] **Step 2: Run test — expect `ReceivePack not yet implemented`**

```bash
go test ./plane/git/rpc/... -run TestProxy_ReceivePack -v
```

- [ ] **Step 3: Implement ReceivePack in proxy.go**

Replace the stub `ReceivePack` method with:

```go
func (p *GitalyProxy) ReceivePack(ctx context.Context, ref RepoRef, updates []RefUpdate, r io.Reader) (io.ReadCloser, error) {
	if err := p.hook.PreReceive(ctx, ref, updates); err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "git: pre-receive hook: %v", err)
	}

	addr, gRepo, err := p.resolve(ctx, ref.RepoID)
	if err != nil {
		return nil, err
	}
	conn, err := p.pool.Conn(ctx, addr.Addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "git: gitaly unavailable: %v", err)
	}
	cl := gitalypb.NewSmartHTTPServiceClient(conn)
	stream, err := cl.PostReceivePack(ctx)
	if err != nil {
		return nil, fmt.Errorf("git: receive_pack: %w", err)
	}

	// First message: header with repository metadata.
	if err := stream.Send(&gitalypb.PostReceivePackRequest{
		Repository: gRepo,
		GlId:       ref.AgentID,
		GlRepository: ref.RepoID,
	}); err != nil {
		return nil, fmt.Errorf("git: receive_pack header: %w", err)
	}

	// Second message: git-receive-pack stdin.
	body, _ := io.ReadAll(r)
	if err := stream.Send(&gitalypb.PostReceivePackRequest{Data: body}); err != nil {
		return nil, fmt.Errorf("git: receive_pack data: %w", err)
	}
	_ = stream.CloseSend()

	// Record metering after successful forward. Best-effort for Redis;
	// outbox write failure propagates (see metering.TwoTierCounter).
	_ = p.meter.Record(ctx, ref, "receive_pack", int64(len(body)), 0, len(updates))

	return streamToReader(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return resp.Data, nil
	}), nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./plane/git/rpc/... -v
```

Expected: all tests `PASS`

- [ ] **Step 5: Commit**

```bash
git add plane/git/rpc/
git commit -m "feat(git): ReceivePack with hook invocation

Closes #107"
```

---

## Task 7: Git domain — migration + Kafka topic + Domain constant

**Files:**
- Modify: `plane/data/store/domain.go`
- Create: `plane/data/migrations/010_git_domain.sql`
- Modify: `plane/data/kafka/topics.go`
- Modify: `plane/data/outbox/wiring/wiring.go`

- [ ] **Step 1: Add DomainGit constant**

In `plane/data/store/domain.go`, add to the const block and update `Valid()`:

```go
const (
	DomainIdentity      Domain = "identity"
	DomainRepositories  Domain = "repositories"
	DomainCollaboration Domain = "collaboration"
	DomainCI            Domain = "ci"
	DomainBilling       Domain = "billing"
	DomainGit           Domain = "git"
)
```

Update `Valid()`:
```go
func (d Domain) Valid() bool {
	switch d {
	case DomainIdentity, DomainRepositories, DomainCollaboration, DomainCI, DomainBilling, DomainGit:
		return true
	}
	return false
}
```

- [ ] **Step 2: Write migration**

```sql
-- plane/data/migrations/010_git_domain.sql
-- requires PostgreSQL 16+
-- Git domain: metering outbox for hook events.

CREATE SCHEMA IF NOT EXISTS git;

CREATE TABLE git.git_outbox (
  id            BIGSERIAL PRIMARY KEY,
  event_id      UUID NOT NULL DEFAULT gen_random_uuid(),
  aggregate_type TEXT NOT NULL,
  aggregate_id  UUID NOT NULL,
  event_type    TEXT NOT NULL,
  payload       JSONB NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT git_outbox_event_id_unique UNIQUE (event_id)
);

CREATE INDEX idx_git_outbox_created_at ON git.git_outbox (created_at);
```

- [ ] **Step 3: Add Kafka topic constants**

In `plane/data/kafka/topics.go`, add:

```go
const (
	// existing constants unchanged ...

	TopicGitMeteringEvents    = "gitscale.git.metering.events"
	TopicGitMeteringEventsDLQ = "gitscale.git.metering.events.dlq"
)
```

Also add `TopicGitMeteringEvents` to `AllMainTopics`:

```go
var AllMainTopics = []string{
	TopicIdentityEvents,
	TopicRepositoriesEvents,
	TopicCollaborationEvents,
	TopicCIEvents,
	TopicBillingEvents,
	TopicGitMeteringEvents,
}
```

- [ ] **Step 4: Wire git domain into outbox consumer**

In `plane/data/outbox/wiring/wiring.go`, add to `AllDomains`:

```go
{
    Domain: store.DomainGit,
    Table:  store.DomainGit.OutboxTable(),
    Topic:  kafkadata.TopicGitMeteringEvents,
},
```

- [ ] **Step 5: Verify compilation and tests**

```bash
go build ./plane/data/...
go test ./plane/data/... -v 2>&1 | tail -20
```

Expected: clean build, existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add plane/data/store/domain.go plane/data/migrations/010_git_domain.sql \
        plane/data/kafka/topics.go plane/data/outbox/wiring/wiring.go
git commit -m "feat(data): git domain — outbox table + metering topic

Closes #109"
```

---

## Task 8: TwoTierCounter — Redis enforcement + outbox reconciliation

**Files:**
- Create: `plane/git/metering/events.go`
- Modify: `plane/git/metering/counter.go` (replace stub with full impl)
- Create: `plane/git/metering/counter_test.go`

- [ ] **Step 1: Define MeteringEvent**

```go
// plane/git/metering/events.go
package metering

import (
	"time"

	"github.com/google/uuid"
)

// MeteringEvent is the payload written to the git outbox for reconciliation.
type MeteringEvent struct {
	AgentID         string    `json:"agent_id"`
	RepoID          string    `json:"repo_id"`
	Operation       string    `json:"operation"` // "receive_pack" | "upload_pack" | "info_refs"
	BytesTransferred int64    `json:"bytes_transferred"`
	PackObjects      int64    `json:"pack_objects"`
	RefUpdates       int      `json:"ref_updates"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// eventID returns a deterministic idempotency key for deduplication.
// Uses a fresh UUID — callers must store or log it if replay is needed.
func newEventID() uuid.UUID { return uuid.New() }
```

- [ ] **Step 2: Write failing tests**

```go
// plane/git/metering/counter_test.go
package metering_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	stubstore "github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	"github.com/gitscale-platform/gitscale/plane/git/rpc"
	"github.com/stretchr/testify/require"
)

func TestTwoTierCounter_BothTiersWritten(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	mds := stubstore.NewMetadataStore()
	counter := metering.NewTwoTierCounter(mem, mds)

	ref := rpc.RepoRef{RepoID: "repo-1", AgentID: "agent-1"}
	err := counter.Record(context.Background(), ref, "receive_pack", 1024, 10, 2)
	require.NoError(t, err)

	// Tier 1: Redis enforcement counter must be set.
	window := time.Now().UTC().Format("2006-01-02T15")
	key := "git:meter:agent-1:" + window
	val, err := mem.Get(context.Background(), key)
	require.NoError(t, err, "enforcement counter must be in cache")
	require.NotEmpty(t, val)

	// Tier 2: Outbox must have one row.
	rows := mds.OutboxRows(store.DomainGit)
	require.Len(t, rows, 1)
	require.Equal(t, "git.metering", rows[0].EventType)
}

func TestTwoTierCounter_RedisFailDoesNotBlockOutbox(t *testing.T) {
	// A nil cache simulates Redis being unreachable; outbox must still write.
	mds := stubstore.NewMetadataStore()
	counter := metering.NewTwoTierCounter(nil, mds)

	ref := rpc.RepoRef{RepoID: "repo-1", AgentID: "agent-1"}
	err := counter.Record(context.Background(), ref, "receive_pack", 512, 5, 1)
	require.NoError(t, err, "Redis failure must not block outbox write")

	rows := mds.OutboxRows(store.DomainGit)
	require.Len(t, rows, 1)
}

func TestTwoTierCounter_OutboxFailPropagates(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	// failingStore.Transact always returns an error.
	counter := metering.NewTwoTierCounter(mem, &failingMetadataStore{})

	ref := rpc.RepoRef{RepoID: "repo-1", AgentID: "agent-1"}
	err := counter.Record(context.Background(), ref, "receive_pack", 512, 5, 1)
	require.Error(t, err, "outbox failure must propagate to caller")
}

type failingMetadataStore struct{}

func (f *failingMetadataStore) Transact(_ context.Context, _ func(store.Tx) error) error {
	return errors.New("db: connection lost")
}
func (f *failingMetadataStore) Identity() store.IdentityReader       { return nil }
func (f *failingMetadataStore) Billing() store.BillingReader         { return nil }
func (f *failingMetadataStore) Repositories() store.RepositoryReader { return nil }
```

Add to imports: `"errors"`, `"github.com/google/uuid"`.

- [ ] **Step 3: Add OutboxRows helper to stub MetadataStore**

The test calls `mds.OutboxRows(domain)` — add this to `plane/data/store/stub/metadata.go`:

```go
// OutboxRows returns all outbox rows recorded for domain. Test helper only.
func (s *MetadataStore) OutboxRows(d store.Domain) []store.OutboxRowRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.OutboxRowRecord(nil), s.outbox[d]...)
}
```

Also add the `OutboxRowRecord` type and the outbox recording to `plane/data/store/stub/metadata.go`. First, check the current state of the stub:

```bash
cat plane/data/store/stub/metadata.go | head -80
```

Then add the `outbox map[store.Domain][]OutboxRowRecord` field to the stub and record calls in `WriteOutbox`. Add to `plane/data/store/` (new shared type):

```go
// plane/data/store/outbox_record.go
package store

import "encoding/json"

// OutboxRowRecord captures a WriteOutbox call for test inspection.
type OutboxRowRecord struct {
	Domain        Domain
	AggregateType string
	AggregateID   interface{}
	EventType     string
	Payload       json.RawMessage
}
```

- [ ] **Step 4: Implement TwoTierCounter (replace stub in counter.go)**

```go
// plane/git/metering/counter.go
package metering

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/git/rpc"
	"github.com/google/uuid"
)

// Counter records a metering event for a completed Git operation.
type Counter interface {
	Record(ctx context.Context, ref rpc.RepoRef, op string, bytes int64, packObjects int64, refUpdates int) error
}

// noopCounter discards all events.
type noopCounter struct{}

func NewNoopCounter() Counter { return noopCounter{} }
func (noopCounter) Record(_ context.Context, _ rpc.RepoRef, _ string, _ int64, _ int64, _ int) error {
	return nil
}

// TwoTierCounter writes to Redis (best-effort enforcement) and to the git
// outbox (mandatory reconciliation). A Redis failure is logged and skipped;
// an outbox failure propagates to the caller, rejecting the push.
type TwoTierCounter struct {
	cache cache.CacheStore  // may be nil (Redis unavailable)
	store store.MetadataStore
}

func NewTwoTierCounter(c cache.CacheStore, s store.MetadataStore) *TwoTierCounter {
	return &TwoTierCounter{cache: c, store: s}
}

func (t *TwoTierCounter) Record(ctx context.Context, ref rpc.RepoRef, op string, bytes, packObjects int64, refUpdates int) error {
	// Tier 1: Redis enforcement counter (best-effort).
	if t.cache != nil && ref.AgentID != "" {
		window := time.Now().UTC().Format("2006-01-02T15") // hourly window
		key := fmt.Sprintf("git:meter:%s:%s", ref.AgentID, window)
		// Increment by bytes; ignore errors — enforcement tier is non-blocking.
		existing, _ := t.cache.Get(ctx, key)
		var prev int64
		_ = json.Unmarshal(existing, &prev)
		updated, _ := json.Marshal(prev + bytes)
		_ = t.cache.Set(ctx, key, updated, 2*time.Hour)
	}

	// Tier 2: Outbox reconciliation (mandatory).
	evt := MeteringEvent{
		AgentID:          ref.AgentID,
		RepoID:           ref.RepoID,
		Operation:        op,
		BytesTransferred: bytes,
		PackObjects:      packObjects,
		RefUpdates:       refUpdates,
		OccurredAt:       time.Now().UTC(),
	}
	aggID := uuid.New()
	return t.store.Transact(ctx, func(tx store.Tx) error {
		return tx.WriteOutbox(ctx, store.DomainGit, "git.metering", aggID, "git.metering", evt)
	})
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./plane/git/metering/... -v
```

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git add plane/git/metering/ plane/data/store/outbox_record.go plane/data/store/stub/
git commit -m "feat(git): two-tier metering counter

Closes #109"
```

---

## Task 9: AnalyticsSink interface + in-memory stub

**Files:**
- Create: `plane/git/sink/sink.go`
- Create: `plane/git/sink/stub.go`

- [ ] **Step 1: Write failing test**

```go
// plane/git/sink/stub_test.go
package sink_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/git/metering"
	"github.com/gitscale-platform/gitscale/plane/git/sink"
	"github.com/stretchr/testify/require"
)

func TestStubSink_RecordAndQuery(t *testing.T) {
	s := sink.NewStubSink()
	evt := metering.MeteringEvent{
		AgentID:          "agent-1",
		RepoID:           "repo-1",
		Operation:        "receive_pack",
		BytesTransferred: 2048,
		OccurredAt:       time.Now(),
	}
	require.NoError(t, s.Record(context.Background(), evt))
	all := s.All()
	require.Len(t, all, 1)
	require.Equal(t, evt.AgentID, all[0].AgentID)
}
```

- [ ] **Step 2: Implement**

```go
// plane/git/sink/sink.go
package sink

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/git/metering"
)

// AnalyticsSink receives metering events drained from the git outbox.
type AnalyticsSink interface {
	Record(ctx context.Context, e metering.MeteringEvent) error
}
```

```go
// plane/git/sink/stub.go
package sink

import (
	"context"
	"sync"

	"github.com/gitscale-platform/gitscale/plane/git/metering"
)

// StubSink is an in-memory AnalyticsSink for tests.
// ClickHouse wires in as a concrete implementation in a later phase.
type StubSink struct {
	mu     sync.Mutex
	events []metering.MeteringEvent
}

func NewStubSink() *StubSink { return &StubSink{} }

func (s *StubSink) Record(_ context.Context, e metering.MeteringEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

// All returns a snapshot of recorded events. Test helper only.
func (s *StubSink) All() []metering.MeteringEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]metering.MeteringEvent(nil), s.events...)
}
```

- [ ] **Step 3: Run tests — expect pass**

```bash
go test ./plane/git/sink/... -v
```

Expected: `PASS`

- [ ] **Step 4: Commit**

```bash
git add plane/git/sink/
git commit -m "feat(git): AnalyticsSink interface + in-memory stub

Closes #109"
```

---

## Task 10: Integration test

**Files:**
- Create: `plane/git/integration_test.go`

- [ ] **Step 1: Write integration test**

```go
//go:build integration

// plane/git/integration_test.go
package git_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/hook"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	gitrpc "github.com/gitscale-platform/gitscale/plane/git/rpc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	redismodule "github.com/testcontainers/testcontainers-go/modules/redis"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
	"google.golang.org/grpc"
	"time"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/redis/go-redis/v9"
	rediscache "github.com/gitscale-platform/gitscale/plane/data/cache"
)

func TestIntegration_ReceivePack_MetersBothTiers(t *testing.T) {
	ctx := context.Background()

	// Start Postgres.
	pgCtr, err := pgmodule.Run(ctx, "postgres:16-alpine",
		pgmodule.WithDatabase("gitscale_test"),
		pgmodule.WithUsername("gs"), pgmodule.WithPassword("gs"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgCtr.Terminate(ctx) })
	pgConn, _ := pgCtr.ConnectionString(ctx, "sslmode=disable")
	pool, _ := pgxpool.New(ctx, pgConn)
	t.Cleanup(pool.Close)

	// Apply migrations 000–010.
	migrationsDir := filepath.Join("..", "plane", "data", "migrations")
	for _, f := range []string{
		"000_init.sql", "001_identity.sql", "002_repositories.sql",
		"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
		"006_identity_revocation.sql", "007_billing_partition_archives.sql",
		"008_updated_at_triggers.sql", "009_identity_temporal_columns.sql",
		"010_git_domain.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		require.NoError(t, err, "read migration %s", f)
		_, err = pool.Exec(ctx, string(sql))
		require.NoError(t, err, "apply migration %s", f)
	}

	mds := pgstore.New(pool)

	// Start Redis.
	redisCtr, err := redismodule.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = redisCtr.Terminate(ctx) })
	redisEndpoint, _ := redisCtr.Endpoint(ctx, "")
	rdb := redis.NewClient(&redis.Options{Addr: redisEndpoint})
	t.Cleanup(func() { _ = rdb.Close() })
	cacheStore := rediscache.NewRedisStore(rdb)

	// Start fake Gitaly.
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	fakeSrv := grpc.NewServer()
	gitalypb.RegisterSmartHTTPServiceServer(fakeSrv, &integrationFakeGitaly{})
	go fakeSrv.Serve(lis)
	t.Cleanup(fakeSrv.Stop)

	// Insert a repo row so the locator can find it.
	repoID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO repositories.repositories (id, org_id, name, slug, owner_id, replica_set_id, home_region)
		 VALUES ($1, $2, 'test', 'test', $3, $4, 'us-test')`,
		repoID, uuid.New(), uuid.New(), "test-rs",
	)
	require.NoError(t, err)

	// Build the proxy.
	gitalyPool := client.NewGitalyPool()
	t.Cleanup(gitalyPool.Close)

	// Override addr resolution: map "test-rs:8075" → fake Gitaly addr.
	addrMap := map[string]string{"test-rs:8075": lis.Addr().String()}
	metaLoc := locator.NewMetadataLocator(mds)
	cacheLoc := locator.NewCacheLocator(cacheStore, metaLoc)
	overrideLoc := &addrOverrideLocator{inner: cacheLoc, overrides: addrMap}

	counter := metering.NewTwoTierCounter(cacheStore, mds)
	proxy := gitrpc.NewGitalyProxy(gitalyPool, overrideLoc, hook.NoopHookHandler{}, counter)

	updates := []gitrpc.RefUpdate{{RefName: "refs/heads/main", OldOID: "0000000", NewOID: "abc1234"}}
	rc, err := proxy.ReceivePack(ctx, gitrpc.RepoRef{RepoID: repoID.String(), AgentID: "agent-test"}, updates, bytes.NewReader([]byte("data")))
	require.NoError(t, err)
	defer rc.Close()
	_, _ = io.ReadAll(rc)

	// Verify Tier 2: outbox row exists.
	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM git.git_outbox WHERE event_type = 'git.metering'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "metering outbox must have one row")
}

type integrationFakeGitaly struct {
	gitalypb.UnimplementedSmartHTTPServiceServer
}

func (s *integrationFakeGitaly) PostReceivePack(stream gitalypb.SmartHTTPService_PostReceivePackServer) error {
	_, _ = stream.Recv()
	_, _ = stream.Recv()
	return stream.Send(&gitalypb.PostReceivePackResponse{Data: []byte("ok")})
}

// addrOverrideLocator substitutes Addr in FileServerAddr for test routing.
type addrOverrideLocator struct {
	inner     locator.RepoLocator
	overrides map[string]string
}

func (l *addrOverrideLocator) Resolve(ctx context.Context, repoID string) (locator.FileServerAddr, error) {
	addr, err := l.inner.Resolve(ctx, repoID)
	if err != nil {
		return addr, err
	}
	if override, ok := l.overrides[addr.Addr]; ok {
		addr.Addr = override
	}
	return addr, nil
}
```

Add `"os"` to imports.

- [ ] **Step 2: Run integration test**

```bash
go test ./plane/git/... -tags integration -v -run TestIntegration_ReceivePack_MetersBothTiers
```

Expected: `PASS`

- [ ] **Step 3: Commit**

```bash
git add plane/git/integration_test.go
git commit -m "test(git): integration test — ReceivePack meters both tiers

Closes #109"
```

---

## Task 11: docker-compose Gitaly service + config

**Files:**
- Modify: `docker-compose.yml`
- Create: `config/gitaly/config.toml`

- [ ] **Step 1: Add Gitaly service to docker-compose.yml**

Add to the `services:` section of `docker-compose.yml`:

```yaml
  gitaly:
    image: gitlab/gitaly:v16.11.0
    volumes:
      - gitaly-data:/home/git/repositories
      - ./config/gitaly:/etc/gitaly
    ports:
      - "8075:8075"
    healthcheck:
      test: ["CMD", "grpc_health_probe", "-addr=:8075"]
      interval: 10s
      timeout: 5s
      retries: 5
```

Add to the `volumes:` section:

```yaml
  gitaly-data:
```

- [ ] **Step 2: Create minimal Gitaly config**

```toml
# config/gitaly/config.toml
socket_path = ""
listen_addr = "0.0.0.0:8075"
bin_dir = "/usr/lib/gitaly"

[[storage]]
name = "default"
path = "/home/git/repositories"

[logging]
level = "warn"
format = "json"
```

- [ ] **Step 3: Verify compose parses**

```bash
docker compose config --quiet
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add docker-compose.yml config/gitaly/
git commit -m "chore(meta): add Gitaly dev service to docker-compose

Closes #107"
```

---

## Self-review checklist

- [x] **Spec §2 (package layout):** all packages created across tasks 1–9.
- [x] **Spec §3 (interfaces):** `GitRPC`, `HookHandler`, `AnalyticsSink`, `Counter` all defined.
- [x] **Spec §4.1 (push flow):** locator → hook → pool → Gitaly → metering implemented in Tasks 2–3, 6, 8.
- [x] **Spec §4.2 (fetch flow):** UploadPack + InfoRefs in Task 5, metering records bytes only.
- [x] **Spec §4.3 (reconciliation):** git domain + topic + wiring in Task 7; sink in Task 9.
- [x] **Spec §5 (two-tier counter):** Redis best-effort + outbox mandatory in Task 8.
- [x] **Spec §6 (error handling):** `codes.NotFound` (locator miss), `codes.PermissionDenied` (hook reject), `codes.Unavailable` (pool fail), outbox failure propagates — all implemented in Tasks 5–8.
- [x] **Spec §7 (testing):** unit tests per package + integration test in Tasks 1–10.
- [x] **Spec §8 (docker-compose):** Task 11. Config ships in same commit (CI linter rule).
- [x] **ADR-008:** Outbox written in `store.Transact()`; Task 8.
- [x] **ADR-009:** CacheStore-backed locator using `cache.GetRepoLocation`; Task 2.
- [x] **ADR-012:** Two-tier counter; Task 8.
- [x] **GA gate (no mock-DB tests):** integration test uses real Postgres + real Redis via testcontainers.
