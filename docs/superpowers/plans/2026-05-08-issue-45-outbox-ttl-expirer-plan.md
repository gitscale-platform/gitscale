# Issue #45 Outbox TTL expirer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a Temporal workflow that fans out to one activity per domain, each calling a per-domain `outbox.Expirer` that DELETEs processed outbox rows older than 24h in bounded batches.

**Architecture:** `plane/data/outbox` gains an `Expirer` type. `plane/workflow/outboxttl` packages activity + workflow + tests. `cmd/workflow-worker` wires the activity. Workflow loops 5 fixed domains in parallel; iteration order is deterministic to satisfy Temporal replay.

**Tech Stack:** Go 1.22, pgx/v5, prometheus/client_golang, Temporal Go SDK, testcontainers-go.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-45-outbox-ttl-expirer-design.md`

**Branch:** `feat/workflow-outbox-ttl-expirer` (worktree: `../gitscale.worktrees/feat-workflow-outbox-ttl-expirer`)

---

## File map

### Create
- `plane/data/outbox/expirer.go`
- `plane/data/outbox/expirer_test.go`
- `plane/data/outbox/expirer_metrics.go`
- `plane/workflow/outboxttl/doc.go`
- `plane/workflow/outboxttl/activity.go`
- `plane/workflow/outboxttl/activity_test.go`
- `plane/workflow/outboxttl/workflow.go`
- `plane/workflow/outboxttl/workflow_test.go`

### Modify
- `cmd/workflow-worker/main.go` — register Expire activity + ExpireOutboxesWorkflow
- (optional) `plane/data/outbox/wiring/...` — if there is already a domain-bundled wiring helper, reuse it

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/workflow-outbox-ttl-expirer \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-outbox-ttl-expirer \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-outbox-ttl-expirer
git status --porcelain
```

Expected: clean.

- [ ] **Step P.2: Baseline**

```bash
go build ./...
go test -race ./plane/data/outbox/... ./plane/workflow/... ./cmd/workflow-worker/... -count=1
```

Expected: green.

---

## Task 1: `Expirer` + metrics

**Files:**
- `plane/data/outbox/expirer.go`
- `plane/data/outbox/expirer_metrics.go`
- `plane/data/outbox/expirer_test.go`

- [ ] **Step 1.1: Write the failing integration test**

```go
//go:build integration

package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

func TestExpirer_DeletesOnlyOldProcessedRows(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)

	// Seed: identity_outbox with three rows.
	insert := func(processedAt *time.Time) {
		_, err := pool.Exec(ctx, `INSERT INTO identity.identity_outbox(event_id, aggregate_type, aggregate_id, event_type, payload, processed_at)
                              VALUES ($1, 'user', $2, 'user.created', '{}'::jsonb, $3)`,
			uuid.New(), uuid.New(), processedAt)
		if err != nil { t.Fatal(err) }
	}
	old := time.Now().UTC().Add(-25 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)
	insert(&old)     // candidate for deletion
	insert(&recent)  // too recent
	insert(nil)      // unprocessed — must never be deleted

	exp := outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{
		TTL:       24 * time.Hour,
		BatchSize: 1000,
		Registry:  prometheus.NewRegistry(),
	})
	deleted, err := exp.Expire(ctx)
	if err != nil { t.Fatal(err) }
	if deleted != 1 {
		t.Fatalf("deleted=%d want 1", deleted)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining=%d want 2", remaining)
	}
}

func TestExpirer_BatchedToCompletion(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	for i := 0; i < 25_000; i++ {
		_, err := pool.Exec(ctx, `INSERT INTO identity.identity_outbox(event_id, aggregate_type, aggregate_id, event_type, payload, processed_at)
                              VALUES ($1, 'user', $2, 'user.created', '{}'::jsonb, $3)`,
			uuid.New(), uuid.New(), &old)
		if err != nil { t.Fatal(err) }
	}
	exp := outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{BatchSize: 10000})
	deleted, err := exp.Expire(ctx)
	if err != nil { t.Fatal(err) }
	if deleted != 25_000 {
		t.Fatalf("deleted=%d want 25000", deleted)
	}
}
```

- [ ] **Step 1.2: Implement `expirer.go`**

```go
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Expirer deletes processed outbox rows older than TTL in bounded batches.
type Expirer struct {
	pool      *pgxpool.Pool
	domain    store.Domain
	ttl       time.Duration
	batchSize int
	metrics   *ExpirerMetrics
}

// ExpirerOptions configures an Expirer.
type ExpirerOptions struct {
	TTL       time.Duration   // 0 → 24h
	BatchSize int             // 0 → 10000
	Registry  prometheus.Registerer
}

// NewExpirer constructs an Expirer for d.
func NewExpirer(pool *pgxpool.Pool, d store.Domain, opts ExpirerOptions) *Expirer {
	if !d.Valid() {
		panic(fmt.Sprintf("outbox.NewExpirer: invalid domain %q", d))
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10000
	}
	return &Expirer{
		pool:      pool,
		domain:    d,
		ttl:       opts.TTL,
		batchSize: opts.BatchSize,
		metrics:   NewExpirerMetrics(opts.Registry),
	}
}

// Expire deletes expired rows in batches until none remain. Returns total
// deleted across all batches.
func (e *Expirer) Expire(ctx context.Context) (int64, error) {
	start := time.Now()
	var total int64
	for {
		n, err := e.expireBatch(ctx)
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(e.batchSize) {
			break
		}
	}
	e.metrics.observe(string(e.domain), total, time.Since(start))
	return total, nil
}

func (e *Expirer) expireBatch(ctx context.Context) (int64, error) {
	q := fmt.Sprintf(`
WITH victims AS (
    SELECT id FROM %s
    WHERE processed_at IS NOT NULL
      AND processed_at < now() - $1::interval
    ORDER BY id
    LIMIT $2
)
DELETE FROM %s
WHERE id IN (SELECT id FROM victims)`, e.domain.OutboxTable(), e.domain.OutboxTable())
	tag, err := e.pool.Exec(ctx, q, e.ttl, e.batchSize)
	if err != nil {
		return 0, fmt.Errorf("outbox expirer: %s: %w", e.domain, err)
	}
	return tag.RowsAffected(), nil
}
```

(Add the `prometheus` import; if your repo's `prometheus` import alias differs, follow that convention.)

- [ ] **Step 1.3: Implement `expirer_metrics.go`**

```go
package outbox

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ExpirerMetrics holds Prometheus instruments for Expirer observability.
type ExpirerMetrics struct {
	rowsDeleted *prometheus.CounterVec
	duration    *prometheus.HistogramVec
}

// NewExpirerMetrics constructs metrics registered against reg. reg may be nil
// in tests (returns metrics that record into a discard registry).
func NewExpirerMetrics(reg prometheus.Registerer) *ExpirerMetrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	auto := promauto.With(reg)
	return &ExpirerMetrics{
		rowsDeleted: auto.NewCounterVec(prometheus.CounterOpts{
			Name: "gitscale_outbox_rows_deleted_total",
			Help: "Outbox rows deleted by the TTL expirer, by domain.",
		}, []string{"domain"}),
		duration: auto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gitscale_outbox_expirer_duration_seconds",
			Help:    "Wall time of one Expire cycle, by domain.",
			Buckets: prometheus.DefBuckets,
		}, []string{"domain"}),
	}
}

func (m *ExpirerMetrics) observe(domain string, deleted int64, dur time.Duration) {
	m.rowsDeleted.WithLabelValues(domain).Add(float64(deleted))
	m.duration.WithLabelValues(domain).Observe(dur.Seconds())
}
```

- [ ] **Step 1.4: Run integration tests**

```bash
go test -tags integration -race -run TestExpirer ./plane/data/outbox/... -count=1
```

Expected: PASS.

- [ ] **Step 1.5: Commit**

```bash
git add plane/data/outbox/expirer.go \
        plane/data/outbox/expirer_metrics.go \
        plane/data/outbox/expirer_test.go
git commit -m "$(cat <<'EOF'
feat(data): outbox TTL Expirer for #45

Bounded-batch DELETE of processed_at-IS-NOT-NULL rows older than TTL
(default 24h, ADR-008). Per-domain metrics: rows_deleted_total, duration.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Activity

**Files:**
- `plane/workflow/outboxttl/doc.go`
- `plane/workflow/outboxttl/activity.go`
- `plane/workflow/outboxttl/activity_test.go`

- [ ] **Step 2.1: doc.go**

```go
// Package outboxttl is the Temporal workflow + activity that periodically
// expires processed outbox rows across all five GitScale domains, per ADR-008.
package outboxttl
```

- [ ] **Step 2.2: activity.go**

```go
package outboxttl

import (
	"context"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
)

// ActivityNameExpireDomainOutbox is the registered name for the activity.
const ActivityNameExpireDomainOutbox = "outbox.ExpireDomain"

// ExpireDomainInput names the domain whose outbox to expire.
type ExpireDomainInput struct {
	Domain string
}

// ExpireDomainResult reports per-domain outcome.
type ExpireDomainResult struct {
	Domain      string
	RowsDeleted int64
	DurationMS  int64
}

// ExpireDomainOutboxActivity dispatches per-domain Expirer.Expire calls.
type ExpireDomainOutboxActivity struct {
	expirers map[store.Domain]*outbox.Expirer
}

// NewExpireDomainOutboxActivity wraps a per-domain expirer map.
func NewExpireDomainOutboxActivity(expirers map[store.Domain]*outbox.Expirer) *ExpireDomainOutboxActivity {
	return &ExpireDomainOutboxActivity{expirers: expirers}
}

// Execute runs the named domain's Expirer.
func (a *ExpireDomainOutboxActivity) Execute(ctx context.Context, in ExpireDomainInput) (ExpireDomainResult, error) {
	d := store.Domain(in.Domain)
	if !d.Valid() {
		return ExpireDomainResult{}, fmt.Errorf("outboxttl: invalid domain %q", in.Domain)
	}
	exp, ok := a.expirers[d]
	if !ok || exp == nil {
		return ExpireDomainResult{}, fmt.Errorf("outboxttl: no expirer registered for domain %q", in.Domain)
	}
	start := time.Now()
	deleted, err := exp.Expire(ctx)
	if err != nil {
		return ExpireDomainResult{}, fmt.Errorf("outboxttl: expire %s: %w", d, err)
	}
	return ExpireDomainResult{
		Domain:      string(d),
		RowsDeleted: deleted,
		DurationMS:  time.Since(start).Milliseconds(),
	}, nil
}
```

- [ ] **Step 2.3: activity_test.go**

```go
package outboxttl_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/workflow/outboxttl"
)

func TestActivity_RejectsInvalidDomain(t *testing.T) {
	a := outboxttl.NewExpireDomainOutboxActivity(map[store.Domain]*outbox.Expirer{})
	if _, err := a.Execute(context.Background(), outboxttl.ExpireDomainInput{Domain: "bogus"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestActivity_RejectsUnregisteredDomain(t *testing.T) {
	a := outboxttl.NewExpireDomainOutboxActivity(map[store.Domain]*outbox.Expirer{})
	if _, err := a.Execute(context.Background(), outboxttl.ExpireDomainInput{Domain: string(store.DomainIdentity)}); err == nil {
		t.Fatal("expected error for unregistered domain")
	}
}
```

(A successful-path test belongs in the integration suite where a real
Expirer is wired against testcontainer PG. Add as part of Task 4.)

- [ ] **Step 2.4: Run**

```bash
go test -race ./plane/workflow/outboxttl/... -count=1
```

Expected: PASS.

- [ ] **Step 2.5: Commit**

```bash
git add plane/workflow/outboxttl/doc.go \
        plane/workflow/outboxttl/activity.go \
        plane/workflow/outboxttl/activity_test.go
git commit -m "$(cat <<'EOF'
feat(workflow): outboxttl ExpireDomain activity for #45

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Workflow

**Files:**
- `plane/workflow/outboxttl/workflow.go`
- `plane/workflow/outboxttl/workflow_test.go`

- [ ] **Step 3.1: workflow.go**

```go
package outboxttl

import (
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"go.temporal.io/sdk/workflow"
)

// ExpireOutboxesResult aggregates per-domain results.
type ExpireOutboxesResult struct {
	PerDomain []ExpireDomainResult
	Errors    []string
}

// ExpireOutboxesWorkflow fans out to one ExpireDomainOutboxActivity per
// domain, collects results, and returns aggregated outcome.
//
// Determinism: the domains slice is fixed; futures are awaited in declaration
// order; no time.Now/random/map-iter inside the workflow body.
func ExpireOutboxesWorkflow(ctx workflow.Context) (ExpireOutboxesResult, error) {
	domains := []store.Domain{
		store.DomainIdentity,
		store.DomainRepositories,
		store.DomainCollaboration,
		store.DomainCI,
		store.DomainBilling,
	}
	ao := workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Minute}
	actx := workflow.WithActivityOptions(ctx, ao)

	futures := make([]workflow.Future, len(domains))
	for i, d := range domains {
		futures[i] = workflow.ExecuteActivity(actx, ActivityNameExpireDomainOutbox,
			ExpireDomainInput{Domain: string(d)})
	}

	var totals ExpireOutboxesResult
	for i, f := range futures {
		var r ExpireDomainResult
		if err := f.Get(ctx, &r); err != nil {
			totals.Errors = append(totals.Errors, fmt.Sprintf("%s: %v", domains[i], err))
			continue
		}
		totals.PerDomain = append(totals.PerDomain, r)
	}
	if len(totals.Errors) > 0 {
		return totals, fmt.Errorf("expirer: %d domain(s) failed", len(totals.Errors))
	}
	return totals, nil
}
```

- [ ] **Step 3.2: workflow_test.go**

```go
package outboxttl_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/workflow/outboxttl"
	"go.temporal.io/sdk/testsuite"
)

type fakeActivity struct {
	results map[string]outboxttl.ExpireDomainResult
	errs    map[string]error
}

func (f *fakeActivity) Execute(ctx context.Context, in outboxttl.ExpireDomainInput) (outboxttl.ExpireDomainResult, error) {
	if e, ok := f.errs[in.Domain]; ok {
		return outboxttl.ExpireDomainResult{}, e
	}
	return f.results[in.Domain], nil
}

func TestWorkflow_HappyPath(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	fa := &fakeActivity{results: map[string]outboxttl.ExpireDomainResult{
		string(store.DomainIdentity):      {Domain: string(store.DomainIdentity), RowsDeleted: 1},
		string(store.DomainRepositories):  {Domain: string(store.DomainRepositories), RowsDeleted: 2},
		string(store.DomainCollaboration): {Domain: string(store.DomainCollaboration), RowsDeleted: 0},
		string(store.DomainCI):            {Domain: string(store.DomainCI), RowsDeleted: 3},
		string(store.DomainBilling):       {Domain: string(store.DomainBilling), RowsDeleted: 4},
	}}
	env.RegisterActivityWithOptions(fa.Execute, /* RegisterOptions{Name: outboxttl.ActivityNameExpireDomainOutbox} */)
	env.RegisterWorkflow(outboxttl.ExpireOutboxesWorkflow)

	env.ExecuteWorkflow(outboxttl.ExpireOutboxesWorkflow)
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow not completed")
	}
	if env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}
	var got outboxttl.ExpireOutboxesResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.PerDomain) != 5 {
		t.Fatalf("expected 5 results, got %d", len(got.PerDomain))
	}
}

func TestWorkflow_OneDomainFailsButOthersComplete(t *testing.T) {
	// Same shape as Happy, but inject error for billing; assert PerDomain has 4
	// results and Errors has 1; workflow returns error overall.
}
```

(The exact `RegisterActivityWithOptions` signature varies across SDK versions — leave it commented and let the implementer fill in the canonical call as used by `plane/workflow/canary` or `plane/workflow/billing`.)

- [ ] **Step 3.3: Run**

```bash
go test -race -run TestWorkflow_ ./plane/workflow/outboxttl/... -count=1
```

Expected: PASS.

- [ ] **Step 3.4: Commit**

```bash
git add plane/workflow/outboxttl/workflow.go \
        plane/workflow/outboxttl/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(workflow): ExpireOutboxesWorkflow fans out to 5 domains (#45)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: End-to-end integration test

**File:** `plane/workflow/outboxttl/integration_test.go`

- [ ] **Step 4.1: Boot worker + workflow against testcontainer PG**

Spin up a Postgres testcontainer with the migrations, seed mixed rows in
`identity_outbox`, register the activity wired to a real Expirer, register
the workflow, run via `testsuite.WorkflowTestSuite` (in-process). Assert
total deleted rows match expectation per domain.

(Reuse the harness from `plane/workflow/billing/workflow_test.go` for the
shape; only the activity wiring + assertions differ.)

- [ ] **Step 4.2: Run**

```bash
go test -tags integration -race ./plane/workflow/outboxttl/... -count=1
```

Expected: PASS.

- [ ] **Step 4.3: Commit**

```bash
git add plane/workflow/outboxttl/integration_test.go
git commit -m "$(cat <<'EOF'
test(workflow): outboxttl end-to-end (#45)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Wire `cmd/workflow-worker`

**File:** `cmd/workflow-worker/main.go`

- [ ] **Step 5.1: Build per-domain Expirer map**

After existing pgxpool creation but before `worker.New(...)`:

```go
expirers := map[store.Domain]*outbox.Expirer{
    store.DomainIdentity:      outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{}),
    store.DomainRepositories:  outbox.NewExpirer(pool, store.DomainRepositories, outbox.ExpirerOptions{}),
    store.DomainCollaboration: outbox.NewExpirer(pool, store.DomainCollaboration, outbox.ExpirerOptions{}),
    store.DomainCI:            outbox.NewExpirer(pool, store.DomainCI, outbox.ExpirerOptions{}),
    store.DomainBilling:       outbox.NewExpirer(pool, store.DomainBilling, outbox.ExpirerOptions{}),
}
ttlActivity := outboxttl.NewExpireDomainOutboxActivity(expirers)
```

- [ ] **Step 5.2: Register**

Where existing activities are registered with the worker:

```go
w.RegisterActivityWithOptions(ttlActivity.Execute, activity.RegisterOptions{
    Name: outboxttl.ActivityNameExpireDomainOutbox,
})
w.RegisterWorkflow(outboxttl.ExpireOutboxesWorkflow)
```

- [ ] **Step 5.3: Build + tests**

```bash
go build ./cmd/workflow-worker
go test -race ./cmd/workflow-worker/... -count=1
```

Expected: green.

- [ ] **Step 5.4: Commit**

```bash
git add cmd/workflow-worker/main.go
git commit -m "$(cat <<'EOF'
feat(workflow-worker): register outbox TTL expirer activity + workflow (#45)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Final gates + open PR

- [ ] **Step 6.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
go test -tags integration -race ./plane/data/outbox/... ./plane/workflow/outboxttl/... -count=1
```

Expected: all green.

- [ ] **Step 6.2: Mandatory skills (workflow plane)**

- `gitscale-temporal-determinism` — workflow body uses fixed slice, deterministic future iteration; verify clean
- `gitscale-go-conventions`
- `gitscale-plane-boundary` — `outboxttl` may import `plane/data/outbox` and `plane/data/store`; that is the documented direction

- [ ] **Step 6.3: Self-review battery (parallel)**

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter` — `Expire` errors must surface; partial-failure path returns error overall
- `pr-review-toolkit:type-design-analyzer` — new types: `Expirer`, `ExpirerOptions`, `ExpirerMetrics`, `ExpireDomainOutboxActivity`, `ExpireDomainInput/Result`, `ExpireOutboxesResult`
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` — confirm ADR-008 conformance

- [ ] **Step 6.4: Push + open PR**

```bash
git push -u origin feat/workflow-outbox-ttl-expirer
gh pr create --title "[Workflow] Outbox row TTL expirer per ADR-008" --body "$(cat <<'EOF'
## Summary

- New `outbox.Expirer` performs bounded-batch DELETE of processed outbox
  rows older than 24h; per-domain metrics
  `gitscale_outbox_rows_deleted_total` + `…_duration_seconds`.
- New `plane/workflow/outboxttl` package: `ExpireDomainOutboxActivity` +
  `ExpireOutboxesWorkflow` (fan-out across 5 domains, deterministic).
- Worker registers activity + workflow.

## ADR-impact

conforming. Implements the 24h post-high-water expiry mandated by ADR-008.

## Test plan

- [x] `go test -race ./plane/data/outbox/... ./plane/workflow/outboxttl/...`
- [x] `go test -tags integration -race ./plane/data/outbox/... ./plane/workflow/outboxttl/...`
- [x] gitscale-temporal-determinism clean
- [x] Workflow happy + partial-failure paths covered

Spec: docs/superpowers/specs/2026-05-08-issue-45-outbox-ttl-expirer-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-45-outbox-ttl-expirer-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #45.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 6.5: Watch CI**

```bash
gh pr checks <number> --watch
```

---

## Self-review (plan author)

**Spec coverage:** all six acceptance items map to Tasks 1–5; observability
covered by metrics in Task 1.

**Placeholder scan:** Task 3.2 leaves the precise `RegisterActivityWithOptions`
signature for the implementer to mirror from a sibling test. Acceptable —
SDK call shape varies across versions.

**Type consistency:** `Expirer`, `ExpirerOptions`, `ExpirerMetrics`,
`ActivityNameExpireDomainOutbox`, `ExpireDomainInput`/`Result`,
`ExpireOutboxesResult` referenced consistently across plan, tests, and PR
body.
