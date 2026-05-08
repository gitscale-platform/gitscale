# Issue #48 partition-gap monitoring + runbook — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Prometheus gauge that reports days remaining until `billing.usage_events` next-month partition is missing, an alert rule that pages on ≤11 days, a manual CLI to trigger the rollover workflow, and a runbook the oncall reads.

**Architecture:** Lightweight observability daemon `cmd/partition-gap-monitor` reads `pg_inherits` against `billing.usage_events`, computes days-to-gap, exports `gitscale_billing_partition_days_until_gap`. Alert rule + runbook live under `deploy/alerts/` and `docs/runbooks/`. Manual trigger CLI `cmd/partition-rollover-trigger` calls Temporal `StartWorkflow`.

**Tech Stack:** Go 1.22, pgx/v5, prometheus/client_golang, Temporal Go SDK, testcontainers-go, promtool.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-48-partition-gap-monitoring-design.md`

**Branch:** `feat/data-partition-gap-monitoring` (worktree: `../gitscale.worktrees/feat-data-partition-gap-monitoring`)

---

## File map

### Create
- `plane/data/store/billing/partition_gap_metric.go` — `PartitionGapMetric`
- `plane/data/store/billing/partition_gap_metric_test.go` — unit + integration tests
- `cmd/partition-gap-monitor/main.go` — daemon
- `cmd/partition-rollover-trigger/main.go` — CLI
- `cmd/partition-rollover-trigger/main_test.go` — table-driven flag/parse test
- `deploy/alerts/billing_partition_gap.yaml`
- `docs/runbooks/billing-partition-gap.md`

### Modify
- (none — purely additive)

---

## Pre-flight

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/data-partition-gap-monitoring \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-data-partition-gap-monitoring \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-data-partition-gap-monitoring
git status --porcelain
```

Expected: clean.

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
```

Expected: green. If `promtool` is not installed: `go install github.com/prometheus/prometheus/cmd/promtool@latest` (or apt/brew).

---

## Task 1: `PartitionGapMetric` + unit test

**Files:**
- Create: `plane/data/store/billing/partition_gap_metric.go`
- Create: `plane/data/store/billing/partition_gap_metric_test.go`

- [ ] **Step 1.1: Write the failing unit test**

```go
package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type fakeProbe struct {
	upperBound time.Time
	count      int
}

func (f fakeProbe) MaxPartitionUpperBound(_ context.Context) (time.Time, int, error) {
	return f.upperBound, f.count, nil
}

func TestPartitionGapMetric_RefreshComputesDaysAndCount(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	m := billing.NewPartitionGapMetricWithProbe(fakeProbe{
		upperBound: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC),
		count:      12,
	}, reg, func() time.Time { return time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC) })

	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	got := readGauge(t, reg, "gitscale_billing_partition_days_until_gap")
	if got != 16 {
		t.Fatalf("days_until_gap=%v want 16", got)
	}
	got = readGauge(t, reg, "gitscale_billing_partition_count")
	if got != 12 {
		t.Fatalf("partition_count=%v want 12", got)
	}
}

func readGauge(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not present", name)
	return 0
}

var _ = dto.Metric{} // keep import in case readGauge expands
```

- [ ] **Step 1.2: Run test (should fail — no impl)**

```bash
go test -race -run TestPartitionGapMetric ./plane/data/store/billing/...
```

Expected: build error / FAIL.

- [ ] **Step 1.3: Implement `partition_gap_metric.go`**

```go
package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PartitionUpperBoundProbe returns the upper bound of the latest existing
// monthly partition of billing.usage_events and the total partition count.
type PartitionUpperBoundProbe interface {
	MaxPartitionUpperBound(ctx context.Context) (upperBound time.Time, count int, err error)
}

// PartitionGapMetric exposes Prometheus gauges that surface the calendar
// distance between today and the first INSERT-failing date for
// billing.usage_events.
type PartitionGapMetric struct {
	probe        PartitionUpperBoundProbe
	clock        func() time.Time
	daysUntilGap *prometheus.GaugeVec
	partCount    *prometheus.GaugeVec
}

// NewPartitionGapMetric registers gauges against reg and probes via pool.
func NewPartitionGapMetric(pool *pgxpool.Pool, reg prometheus.Registerer) *PartitionGapMetric {
	return NewPartitionGapMetricWithProbe(&pgProbe{pool: pool}, reg, func() time.Time { return time.Now().UTC() })
}

// NewPartitionGapMetricWithProbe is the test seam.
func NewPartitionGapMetricWithProbe(p PartitionUpperBoundProbe, reg prometheus.Registerer, clock func() time.Time) *PartitionGapMetric {
	auto := promauto.With(reg)
	return &PartitionGapMetric{
		probe: p,
		clock: clock,
		daysUntilGap: auto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gitscale_billing_partition_days_until_gap",
			Help: "Days remaining before billing.usage_events INSERTs will fail",
		}, []string{"schema", "table"}),
		partCount: auto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gitscale_billing_partition_count",
			Help: "Number of monthly partitions currently attached to billing.usage_events",
		}, []string{"schema", "table"}),
	}
}

// Refresh recomputes both gauges. Safe to call on a ticker.
func (m *PartitionGapMetric) Refresh(ctx context.Context) error {
	ub, count, err := m.probe.MaxPartitionUpperBound(ctx)
	if err != nil {
		return fmt.Errorf("partition gap metric: probe: %w", err)
	}
	days := int(ub.Sub(m.clock()).Hours() / 24)
	m.daysUntilGap.WithLabelValues("billing", "usage_events").Set(float64(days))
	m.partCount.WithLabelValues("billing", "usage_events").Set(float64(count))
	return nil
}

type pgProbe struct {
	pool *pgxpool.Pool
}

// MaxPartitionUpperBound parses the upper bound expression of every
// child partition of billing.usage_events and returns the maximum.
func (p *pgProbe) MaxPartitionUpperBound(ctx context.Context) (time.Time, int, error) {
	const q = `
SELECT pg_get_expr(c.relpartbound, c.oid) AS bound,
       count(*) OVER ()                  AS count
FROM   pg_inherits i
JOIN   pg_class    c ON c.oid = i.inhrelid
JOIN   pg_class    p ON p.oid = i.inhparent
JOIN   pg_namespace n ON n.oid = p.relnamespace
WHERE  n.nspname = 'billing' AND p.relname = 'usage_events'`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer rows.Close()

	var (
		max   time.Time
		count int
	)
	for rows.Next() {
		var bound string
		if err := rows.Scan(&bound, &count); err != nil {
			return time.Time{}, 0, err
		}
		ub, err := parsePartitionUpperBound(bound)
		if err != nil {
			return time.Time{}, 0, err
		}
		if ub.After(max) {
			max = ub
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, 0, err
	}
	return max, count, nil
}

// parsePartitionUpperBound extracts the TO ('YYYY-MM-DD') date from a
// `pg_get_expr` partition-bound string of the form
//
//   FOR VALUES FROM ('2026-05-01') TO ('2026-06-01')
func parsePartitionUpperBound(expr string) (time.Time, error) {
	const marker = "TO ('"
	i := stringsIndex(expr, marker)
	if i < 0 {
		return time.Time{}, fmt.Errorf("partition gap metric: cannot find TO clause in %q", expr)
	}
	rest := expr[i+len(marker):]
	end := stringsIndex(rest, "'")
	if end < 0 {
		return time.Time{}, fmt.Errorf("partition gap metric: unterminated TO clause in %q", expr)
	}
	return time.Parse("2006-01-02", rest[:end])
}

// stringsIndex is strings.Index but inlined to avoid importing strings.
// (One use; not worth a top-level import.)
func stringsIndex(s, sub string) int {
	n := len(s) - len(sub)
	for i := 0; i <= n; i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

(If you would rather keep `strings.Index`: import `strings` and remove the
stringsIndex helper. Either is fine; keep one or the other.)

- [ ] **Step 1.4: Run test**

```bash
go test -race -run TestPartitionGapMetric ./plane/data/store/billing/...
```

Expected: PASS.

- [ ] **Step 1.5: Commit**

```bash
git add plane/data/store/billing/partition_gap_metric.go \
        plane/data/store/billing/partition_gap_metric_test.go
git commit -m "$(cat <<'EOF'
feat(data): partition gap Prometheus metric for #48

gitscale_billing_partition_days_until_gap surfaces calendar distance
to billing.usage_events INSERT failure.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Integration test against real Postgres

**Files:**
- Modify: `plane/data/store/billing/partition_gap_metric_test.go` (append integration test)

- [ ] **Step 2.1: Append the integration test**

```go
//go:build integration

package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	storetest "github.com/gitscale-platform/gitscale/plane/data/store/postgres/postgrestest"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPartitionGapMetric_IntegrationAgainstRealMigrations(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	reg := prometheus.NewRegistry()

	m := billing.NewPartitionGapMetric(pool, reg)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	gotCount := readGauge(t, reg, "gitscale_billing_partition_count")
	if gotCount != 12 {
		t.Fatalf("count=%v want 12 (per 005_billing.sql)", gotCount)
	}
	// last seeded partition upper bound is 2027-05-01.
	gotDays := readGauge(t, reg, "gitscale_billing_partition_days_until_gap")
	expectedDays := float64(int(time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC).Sub(time.Now().UTC()).Hours() / 24))
	if gotDays != expectedDays {
		t.Fatalf("days=%v want %v", gotDays, expectedDays)
	}
}
```

- [ ] **Step 2.2: Run**

```bash
go test -tags integration -race -run TestPartitionGapMetric_Integration ./plane/data/store/billing/...
```

Expected: PASS.

- [ ] **Step 2.3: Commit**

```bash
git add plane/data/store/billing/partition_gap_metric_test.go
git commit -m "$(cat <<'EOF'
test(data): integration test for partition gap metric (#48)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `cmd/partition-gap-monitor` daemon

**Files:**
- Create: `cmd/partition-gap-monitor/main.go`

- [ ] **Step 3.1: Write the daemon**

```go
// partition-gap-monitor exposes /metrics with the partition gap gauges.
//
// It runs in production as a sidecar / lightweight pod scraped by Prometheus.
// Tied to the alert rule deploy/alerts/billing_partition_gap.yaml.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatal("POSTGRES_DSN is required")
	}
	addr := getenv("PARTITION_GAP_MONITOR_ADDR", ":9100")
	intervalStr := getenv("PARTITION_GAP_MONITOR_INTERVAL", "60s")
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		log.Fatalf("invalid PARTITION_GAP_MONITOR_INTERVAL: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	metric := billing.NewPartitionGapMetric(pool, reg)
	// First refresh up front so /metrics has a value before the scraper hits.
	if err := metric.Refresh(ctx); err != nil {
		log.Printf("warning: initial refresh: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := metric.Refresh(ctx); err != nil {
					log.Printf("refresh: %v", err)
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
		shutdownCtx, cleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanup()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("partition-gap-monitor listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 3.2: Build**

```bash
go build ./cmd/partition-gap-monitor
```

Expected: success.

- [ ] **Step 3.3: Commit**

```bash
git add cmd/partition-gap-monitor/main.go
git commit -m "$(cat <<'EOF'
feat(cmd): partition-gap-monitor daemon for #48

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `cmd/partition-rollover-trigger` CLI

**Files:**
- Create: `cmd/partition-rollover-trigger/main.go`

- [ ] **Step 4.1: Inspect existing Temporal client wiring**

```bash
grep -rn "temporal.NewClient\|client.Dial\|temporalclient.NewLazyClient" plane/workflow/ cmd/ | head -10
```

Identify the helper used by `cmd/workflow-worker` to construct the Temporal
client. Reuse it. If a helper exists at `plane/workflow/temporalclient/`,
import it; otherwise inline the standard `temporal.Dial`.

- [ ] **Step 4.2: Write the CLI**

```go
// partition-rollover-trigger starts the PartitionRolloverWorkflow for the
// given (year, month). Idempotent because CreatePartitionActivity is.
//
// Used by the oncall when the BillingUsageEventsNextPartitionMissing alert
// pages. See docs/runbooks/billing-partition-gap.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	billingwf "github.com/gitscale-platform/gitscale/plane/workflow/billing"
	"go.temporal.io/sdk/client"
)

func main() {
	year := flag.Int("year", 0, "calendar year (e.g. 2027)")
	month := flag.Int("month", 0, "calendar month 1..12")
	addr := flag.String("addr", getenv("TEMPORAL_ADDR", "localhost:7233"), "Temporal frontend address")
	namespace := flag.String("namespace", getenv("TEMPORAL_NAMESPACE", "default"), "Temporal namespace")
	taskQueue := flag.String("task-queue", getenv("WORKFLOW_TASK_QUEUE", "billing"), "Workflow task queue")
	flag.Parse()

	if *year < 2026 || *year > 2100 {
		fmt.Fprintln(os.Stderr, "--year must be between 2026 and 2100")
		os.Exit(2)
	}
	if *month < 1 || *month > 12 {
		fmt.Fprintln(os.Stderr, "--month must be between 1 and 12")
		os.Exit(2)
	}

	c, err := client.Dial(client.Options{HostPort: *addr, Namespace: *namespace})
	if err != nil {
		log.Fatalf("temporal dial: %v", err)
	}
	defer c.Close()

	id := fmt.Sprintf("partition-rollover-manual-%04d-%02d-%d", *year, *month, time.Now().Unix())
	we, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        id,
		TaskQueue: *taskQueue,
	}, billingwf.PartitionRolloverWorkflow, billingwf.PartitionRolloverInput{
		Year: *year, Month: *month,
	})
	if err != nil {
		log.Fatalf("execute workflow: %v", err)
	}
	log.Printf("started workflow id=%s run_id=%s", we.GetID(), we.GetRunID())
	if err := we.Get(context.Background(), nil); err != nil {
		log.Fatalf("workflow result: %v", err)
	}
	log.Printf("ok: partition for %04d-%02d ensured", *year, *month)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

(Adapt `PartitionRolloverInput` field names to whatever exists in the
package; inspect `plane/workflow/billing/workflow.go` for the canonical
input struct.)

- [ ] **Step 4.3: Build**

```bash
go build ./cmd/partition-rollover-trigger
```

Expected: success. If `PartitionRolloverInput`'s actual field is e.g.
`RunTime` (driving Year+Month internally), restructure the call site
accordingly.

- [ ] **Step 4.4: Commit**

```bash
git add cmd/partition-rollover-trigger/main.go
git commit -m "$(cat <<'EOF'
feat(cmd): partition-rollover-trigger CLI for #48

Manual oncall path when the rollover workflow's cron misses a cycle.
Idempotent because the underlying activity is.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Alert rule + promtool gate

**Files:**
- Create: `deploy/alerts/billing_partition_gap.yaml`

- [ ] **Step 5.1: Write the alert rule**

```yaml
groups:
- name: billing-partition-gap
  rules:
  - alert: BillingUsageEventsNextPartitionMissing
    expr: gitscale_billing_partition_days_until_gap{schema="billing",table="usage_events"} <= 11
    for: 15m
    labels:
      severity: page
    annotations:
      summary: "billing.usage_events next-month partition missing"
      runbook: "docs/runbooks/billing-partition-gap.md"
      description: |
        Less than 11 days remain before billing.usage_events INSERTs will
        fail. The PartitionRolloverWorkflow runs 7 days before EoM, so this
        alert firing at <=11d means the rollover has missed at least one
        scheduled cycle. Run cmd/partition-rollover-trigger.

  - alert: BillingUsageEventsPartitionsLow
    expr: gitscale_billing_partition_count{schema="billing",table="usage_events"} < 3
    for: 1h
    labels:
      severity: warn
    annotations:
      summary: "billing.usage_events partition count below 3"
      runbook: "docs/runbooks/billing-partition-gap.md"
```

- [ ] **Step 5.2: Validate with promtool**

```bash
promtool check rules deploy/alerts/billing_partition_gap.yaml
```

Expected: `SUCCESS: 2 rules found`.

- [ ] **Step 5.3: Commit**

```bash
git add deploy/alerts/billing_partition_gap.yaml
git commit -m "$(cat <<'EOF'
feat(deploy): alert rules for billing partition gap (#48)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Runbook

**Files:**
- Create: `docs/runbooks/billing-partition-gap.md`

- [ ] **Step 6.1: Write the runbook**

```markdown
# Runbook: billing.usage_events partition gap

## What this alert means

`billing.usage_events` is range-partitioned by month. INSERTs require a
partition that covers the row's `ts`. If the next-month partition does not
exist, INSERTs fail with `no partition of relation "usage_events" found
for row`.

## Hard date

The original seed in `005_billing.sql` covers 2026-05 through 2027-04. The
first failure date — absent the rollover workflow — is **2027-05-01**.
Subsequent failure dates roll forward monthly.

## Resolution

1. Identify the missing month. The page links to the gauge:
   `gitscale_billing_partition_days_until_gap`. Subtract from today to find
   the first uncovered month.
2. Run:

   ```bash
   cmd/partition-rollover-trigger --year=YYYY --month=MM \
       --addr=$TEMPORAL_ADDR --namespace=$TEMPORAL_NAMESPACE
   ```

   The CreatePartition activity is idempotent — a duplicate run is harmless
   if the workflow self-resolves between page and execution.
3. Confirm `gitscale_billing_partition_days_until_gap` jumps by ~30.

## Why the workflow may have stalled

- Temporal worker pod is down → `kubectl get pods -n workflow`.
- Temporal namespace unreachable → `tctl --address $TEMPORAL_ADDR namespace list`.
- Partitioner advisory lock held by orphan session →
  `SELECT * FROM pg_locks WHERE locktype='advisory' AND granted='t';`

## Verification

- Re-scrape `partition-gap-monitor`: `curl :9100/metrics | grep partition_days`.
- New partition exists in Postgres:

  ```sql
  SELECT relname FROM pg_class
  WHERE relname LIKE 'usage_events_%' ORDER BY relname DESC LIMIT 3;
  ```

## Escalation

If `cmd/partition-rollover-trigger` fails repeatedly, page the data plane
on-call. Manual fallback: run `INSERT INTO billing.partition_archives` is
not a substitute — only `CREATE TABLE billing.usage_events_YYYY_MM PARTITION
OF ...` resolves the gap.
```

- [ ] **Step 6.2: Commit**

```bash
git add docs/runbooks/billing-partition-gap.md
git commit -m "$(cat <<'EOF'
docs(runbooks): billing partition gap runbook (#48)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Final gates + open PR

- [ ] **Step 7.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
go test -tags integration -race ./plane/data/store/billing/... -count=1
promtool check rules deploy/alerts/billing_partition_gap.yaml
```

Expected: all green.

- [ ] **Step 7.2: Mandatory skills (per supervisor §6 for data plane)**

- `gitscale-go-conventions`
- `database-design:sql-pro`

Resolve every finding.

- [ ] **Step 7.3: Self-review battery (parallel)**

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter`
- `pr-review-toolkit:type-design-analyzer` (new types: `PartitionGapMetric`, `PartitionUpperBoundProbe`)
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` (no ADR impact expected)

- [ ] **Step 7.4: Push + open PR**

```bash
git push -u origin feat/data-partition-gap-monitoring
gh pr create --title "[Data] usage_events 2027-05+ partition gap monitoring + runbook" --body "$(cat <<'EOF'
## Summary

- New Prometheus gauge `gitscale_billing_partition_days_until_gap` reports
  the calendar distance to the first INSERT-failure date for
  `billing.usage_events`.
- Daemon `cmd/partition-gap-monitor` and CLI `cmd/partition-rollover-trigger`
  give oncall both visibility and a one-shot manual recovery path.
- Alert rule pages on ≤11 days remaining; runbook documents the gap date
  (2027-05-01) and the fix.

## ADR-impact

none. Operational gating; no architectural change.

## Test plan

- [x] `go test -race ./plane/data/store/billing/...`
- [x] `go test -tags integration -race ./plane/data/store/billing/...` (testcontainer PG with real migrations)
- [x] `promtool check rules deploy/alerts/billing_partition_gap.yaml`
- [x] `go build ./cmd/partition-gap-monitor && go build ./cmd/partition-rollover-trigger`

Spec: docs/superpowers/specs/2026-05-08-issue-48-partition-gap-monitoring-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-48-partition-gap-monitoring-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #48.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 7.5: Watch CI**

```bash
gh pr checks <number> --watch
```

---

## Self-review (plan author)

**Spec coverage:**
- Metric — Tasks 1, 2.
- Daemon — Task 3.
- Manual trigger CLI — Task 4.
- Alert rule + promtool — Task 5.
- Runbook — Task 6.
- PR description references gap date — Task 7.

**Placeholder scan:** Step 4.2 directs the implementer to inspect the
existing Temporal client wiring rather than copying it verbatim. Acceptable —
the canonical helper may be named differently than this plan assumes.

**Type consistency:** `PartitionGapMetric`, `PartitionUpperBoundProbe`,
`gitscale_billing_partition_days_until_gap` referenced consistently across
plan, alert rule, runbook.
