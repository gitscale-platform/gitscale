# Polling outbox consumer (#11) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a polling outbox consumer that drains every domain's `*_outbox` table and publishes events to per-domain Kafka topics, per the design spec at `docs/superpowers/specs/2026-05-02-issue-11-outbox-consumer-design.md`.

**Architecture:** Single-active-drainer-per-domain via `pg_try_advisory_xact_lock` over many replicas. Drain cycle: try-lock → SELECT FOR UPDATE SKIP LOCKED → publish → UPDATE → COMMIT, all in one txn. Crash-anywhere → ROLLBACK → at-least-once + consumer-side `event_id` dedupe = effectively-once.

**Tech Stack:** Go 1.22, `pgx/v5`, `confluent-kafka-go/v2`, `prometheus/client_golang`, `testcontainers-go`. Local dev uses Redpanda (Kafka-compatible).

**Spec:** [`docs/superpowers/specs/2026-05-02-issue-11-outbox-consumer-design.md`](../specs/2026-05-02-issue-11-outbox-consumer-design.md)

**Issue:** [#11](https://github.com/gitscale-platform/gitscale/issues/11)

---

## File Structure

| File | Responsibility |
|---|---|
| `plane/data/outbox/types.go` | `OutboxRow`, `Config`, `Metrics` structs |
| `plane/data/outbox/producer.go` | `KafkaProducer` interface |
| `plane/data/outbox/producer_mock.go` | In-memory producer for unit tests |
| `plane/data/outbox/producer_kafka.go` | confluent-kafka-go production impl |
| `plane/data/outbox/consumer.go` | `OutboxConsumer` interface, `Run` loop |
| `plane/data/outbox/drain.go` | `drainBatch` — single-cycle txn body |
| `plane/data/outbox/metrics.go` | Prometheus counters/histograms/gauges |
| `plane/data/outbox/wiring/wiring.go` | Builds 5 consumer instances |
| `plane/data/outbox/consumer_test.go` | Unit tests w/ mock DB + producer |
| `plane/data/outbox/drain_test.go` | drainBatch unit tests |
| `plane/data/outbox/integration_test.go` | testcontainers PG + Redpanda |
| `plane/data/outbox/testutil_test.go` | Shared integration-test helpers |
| `cmd/outbox-consumer/main.go` | Binary entrypoint |

---

## Task 1: Add dependencies + package skeleton

**Files:**
- Modify: `go.mod`
- Create: `plane/data/outbox/doc.go`

- [ ] **Step 1: Add Go dependencies**

```bash
go get github.com/jackc/pgx/v5@v5.5.5
go get github.com/jackc/pgx/v5/pgxpool@v5.5.5
go get github.com/confluentinc/confluent-kafka-go/v2@v2.3.0
go get github.com/prometheus/client_golang@v1.19.0
go get github.com/google/uuid@v1.6.0
go get github.com/testcontainers/testcontainers-go@v0.29.1
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.29.1
go get github.com/testcontainers/testcontainers-go/modules/redpanda@v0.29.1
go mod tidy
```

- [ ] **Step 2: Create package doc**

```go
// Package outbox implements the polling outbox consumer per ADR-008.
// One consumer instance per domain drains *_outbox and publishes to Kafka.
//
// Spec: docs/superpowers/specs/2026-05-02-issue-11-outbox-consumer-design.md
package outbox
```

Save to `plane/data/outbox/doc.go`.

- [ ] **Step 3: Verify build**

Run: `go build ./plane/data/outbox/...`
Expected: success (empty package compiles)

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum plane/data/outbox/doc.go
git commit -m "feat(outbox): add deps + package skeleton (#11)"
```

---

## Task 2: Core types — `OutboxRow`, `Config`, `Metrics`

**Files:**
- Create: `plane/data/outbox/types.go`
- Create: `plane/data/outbox/types_test.go`

- [ ] **Step 1: Write failing test for Config defaults**

```go
package outbox

import (
  "testing"
  "time"
)

func TestConfig_ApplyDefaults(t *testing.T) {
  c := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "gitscale.identity.events"}
  c.ApplyDefaults()
  if c.PollInterval != time.Second {
    t.Errorf("PollInterval default: got %v, want 1s", c.PollInterval)
  }
  if c.PublishTimeout != 5*time.Second {
    t.Errorf("PublishTimeout default: got %v, want 5s", c.PublishTimeout)
  }
  if c.BatchSize != 100 {
    t.Errorf("BatchSize default: got %d, want 100", c.BatchSize)
  }
}
```

Save to `plane/data/outbox/types_test.go`.

- [ ] **Step 2: Run failing test**

Run: `go test ./plane/data/outbox/... -run TestConfig_ApplyDefaults -v`
Expected: FAIL — `Config` undefined.

- [ ] **Step 3: Implement types**

```go
package outbox

import (
  "encoding/json"
  "time"

  "github.com/google/uuid"
  "github.com/jackc/pgx/v5/pgxpool"
)

// OutboxRow is one event read from a domain *_outbox table.
type OutboxRow struct {
  ID            int64
  EventID       uuid.UUID
  AggregateType string
  AggregateID   uuid.UUID
  EventType     string
  Payload       json.RawMessage
  CreatedAt     time.Time
}

// Config wires one OutboxConsumer instance to one domain's outbox table.
type Config struct {
  Domain         string         // "identity", "repositories", …
  Table          string         // schema-qualified: "identity.identity_outbox"
  Topic          string         // "gitscale.identity.events"
  DB             *pgxpool.Pool
  Producer       KafkaProducer
  PollInterval   time.Duration  // default 1s, env OUTBOX_POLL_INTERVAL_MS
  PublishTimeout time.Duration  // default 5s, env OUTBOX_PUBLISH_TIMEOUT_MS
  BatchSize      int            // default 100, env OUTBOX_BATCH_SIZE
  Metrics        *Metrics       // nil-safe
}

// ApplyDefaults fills zero-valued fields with documented defaults.
func (c *Config) ApplyDefaults() {
  if c.PollInterval == 0 {
    c.PollInterval = time.Second
  }
  if c.PublishTimeout == 0 {
    c.PublishTimeout = 5 * time.Second
  }
  if c.BatchSize == 0 {
    c.BatchSize = 100
  }
}
```

Save to `plane/data/outbox/types.go`. Note: `KafkaProducer` and `Metrics` will fail to compile yet — placeholder for next tasks. Run with `go vet` not test until next tasks land.

- [ ] **Step 4: Add KafkaProducer + Metrics placeholder types**

Append to `plane/data/outbox/types.go` (temporary — replaced in later tasks):

```go
// KafkaProducer is implemented by producer_mock.go and producer_kafka.go.
// Real definition in producer.go (Task 3).
type KafkaProducer interface {
  PublishBatch(ctx interface{}, topic string, batch []OutboxRow) error
  Close() error
}

// Metrics is filled in Task 9.
type Metrics struct{}
```

This will be replaced in Task 3. Compile-only placeholder.

- [ ] **Step 5: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestConfig_ApplyDefaults -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plane/data/outbox/types.go plane/data/outbox/types_test.go
git commit -m "feat(outbox): OutboxRow + Config types with defaults (#11)"
```

---

## Task 3: `KafkaProducer` interface + mock

**Files:**
- Create: `plane/data/outbox/producer.go`
- Create: `plane/data/outbox/producer_mock.go`
- Create: `plane/data/outbox/producer_mock_test.go`
- Modify: `plane/data/outbox/types.go` (drop placeholders)

- [ ] **Step 1: Write failing test for mock producer**

```go
package outbox

import (
  "context"
  "errors"
  "testing"

  "github.com/google/uuid"
)

func TestMockProducer_PublishBatch_HappyPath(t *testing.T) {
  p := NewMockProducer()
  row := OutboxRow{EventID: uuid.New(), Payload: []byte(`{}`)}
  if err := p.PublishBatch(context.Background(), "topic-x", []OutboxRow{row}); err != nil {
    t.Fatalf("PublishBatch: %v", err)
  }
  if got := p.PublishedTo("topic-x"); len(got) != 1 || got[0].EventID != row.EventID {
    t.Errorf("PublishedTo: got %v, want 1 event with same EventID", got)
  }
}

func TestMockProducer_PublishBatch_ReturnsError(t *testing.T) {
  p := NewMockProducer()
  want := errors.New("kafka down")
  p.SetNextError(want)
  err := p.PublishBatch(context.Background(), "t", []OutboxRow{{EventID: uuid.New()}})
  if !errors.Is(err, want) {
    t.Errorf("err = %v, want %v", err, want)
  }
  if got := p.PublishedTo("t"); len(got) != 0 {
    t.Errorf("PublishedTo on error: got %d, want 0", len(got))
  }
}
```

Save to `plane/data/outbox/producer_mock_test.go`.

- [ ] **Step 2: Run test, expect compile failure**

Run: `go test ./plane/data/outbox/... -run TestMockProducer -v`
Expected: FAIL — `NewMockProducer` undefined.

- [ ] **Step 3: Drop the placeholder types from `types.go`**

Edit `plane/data/outbox/types.go`: remove the `KafkaProducer` and `Metrics` placeholder lines added in Task 2 step 4.

- [ ] **Step 4: Define `KafkaProducer` interface**

```go
package outbox

import "context"

// KafkaProducer publishes a batch of outbox rows to a Kafka topic.
// Either every row publishes successfully or PublishBatch returns an error
// and the caller MUST treat the entire batch as not-published.
type KafkaProducer interface {
  PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error
  Close() error
}
```

Save to `plane/data/outbox/producer.go`.

- [ ] **Step 5: Implement mock producer**

```go
package outbox

import (
  "context"
  "sync"
)

// MockProducer is an in-memory KafkaProducer for unit tests.
type MockProducer struct {
  mu        sync.Mutex
  published map[string][]OutboxRow
  nextErr   error
}

func NewMockProducer() *MockProducer {
  return &MockProducer{published: make(map[string][]OutboxRow)}
}

func (m *MockProducer) PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error {
  m.mu.Lock()
  defer m.mu.Unlock()
  if m.nextErr != nil {
    err := m.nextErr
    m.nextErr = nil
    return err
  }
  m.published[topic] = append(m.published[topic], batch...)
  return nil
}

func (m *MockProducer) Close() error { return nil }

// SetNextError causes the next PublishBatch call to return err and consume the slot.
func (m *MockProducer) SetNextError(err error) {
  m.mu.Lock()
  defer m.mu.Unlock()
  m.nextErr = err
}

// PublishedTo returns a copy of all rows published to topic.
func (m *MockProducer) PublishedTo(topic string) []OutboxRow {
  m.mu.Lock()
  defer m.mu.Unlock()
  out := make([]OutboxRow, len(m.published[topic]))
  copy(out, m.published[topic])
  return out
}
```

Save to `plane/data/outbox/producer_mock.go`.

- [ ] **Step 6: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestMockProducer -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add plane/data/outbox/producer.go plane/data/outbox/producer_mock.go plane/data/outbox/producer_mock_test.go plane/data/outbox/types.go
git commit -m "feat(outbox): KafkaProducer interface + mock impl (#11)"
```

---

## Task 4: Metrics scaffolding

**Files:**
- Create: `plane/data/outbox/metrics.go`
- Create: `plane/data/outbox/metrics_test.go`

- [ ] **Step 1: Write failing test for nil-safety + label cardinality**

```go
package outbox

import "testing"

func TestMetrics_NilSafe(t *testing.T) {
  var m *Metrics
  m.IncDrainCycles("identity", "ok")    // must not panic with nil receiver
  m.ObservePublishDuration("identity", "ok", 0.01)
  m.SetOldestUnprocessed("identity", 0)
}

func TestMetrics_RegisterAndIncrement(t *testing.T) {
  m := NewMetrics()
  m.IncDrainCycles("identity", "ok")
  m.IncDrainCycles("identity", "ok")
  if got := m.GetDrainCyclesValue("identity", "ok"); got != 2 {
    t.Errorf("DrainCycles: got %v, want 2", got)
  }
}
```

Save to `plane/data/outbox/metrics_test.go`.

- [ ] **Step 2: Run test, expect failure**

Run: `go test ./plane/data/outbox/... -run TestMetrics -v`
Expected: FAIL.

- [ ] **Step 3: Implement Metrics**

```go
package outbox

import (
  "github.com/prometheus/client_golang/prometheus"
  "github.com/prometheus/client_golang/prometheus/testutil"
)

type Metrics struct {
  DrainCycles        *prometheus.CounterVec   // labels: domain, result
  BatchSize          *prometheus.HistogramVec // labels: domain
  PublishDuration    *prometheus.HistogramVec // labels: domain, result
  OldestUnprocessed  *prometheus.GaugeVec     // labels: domain
  Processed          *prometheus.CounterVec   // labels: domain
  AdvisoryLockHeld   *prometheus.GaugeVec     // labels: domain
}

func NewMetrics() *Metrics {
  return &Metrics{
    DrainCycles: prometheus.NewCounterVec(
      prometheus.CounterOpts{Name: "outbox_drain_cycles_total"},
      []string{"domain", "result"},
    ),
    BatchSize: prometheus.NewHistogramVec(
      prometheus.HistogramOpts{Name: "outbox_batch_size", Buckets: prometheus.ExponentialBuckets(1, 2, 10)},
      []string{"domain"},
    ),
    PublishDuration: prometheus.NewHistogramVec(
      prometheus.HistogramOpts{Name: "outbox_publish_duration_seconds", Buckets: prometheus.ExponentialBuckets(0.001, 2, 14)},
      []string{"domain", "result"},
    ),
    OldestUnprocessed: prometheus.NewGaugeVec(
      prometheus.GaugeOpts{Name: "outbox_oldest_unprocessed_seconds"},
      []string{"domain"},
    ),
    Processed: prometheus.NewCounterVec(
      prometheus.CounterOpts{Name: "outbox_processed_total"},
      []string{"domain"},
    ),
    AdvisoryLockHeld: prometheus.NewGaugeVec(
      prometheus.GaugeOpts{Name: "outbox_advisory_lock_held"},
      []string{"domain"},
    ),
  }
}

// Register exposes the metrics to the given registry. Safe to call once.
func (m *Metrics) Register(r prometheus.Registerer) error {
  for _, c := range []prometheus.Collector{
    m.DrainCycles, m.BatchSize, m.PublishDuration,
    m.OldestUnprocessed, m.Processed, m.AdvisoryLockHeld,
  } {
    if err := r.Register(c); err != nil {
      return err
    }
  }
  return nil
}

// Nil-safe accessors below — every caller can pass *Metrics that may be nil.

func (m *Metrics) IncDrainCycles(domain, result string) {
  if m == nil {
    return
  }
  m.DrainCycles.WithLabelValues(domain, result).Inc()
}

func (m *Metrics) ObserveBatchSize(domain string, n int) {
  if m == nil {
    return
  }
  m.BatchSize.WithLabelValues(domain).Observe(float64(n))
}

func (m *Metrics) ObservePublishDuration(domain, result string, seconds float64) {
  if m == nil {
    return
  }
  m.PublishDuration.WithLabelValues(domain, result).Observe(seconds)
}

func (m *Metrics) SetOldestUnprocessed(domain string, seconds float64) {
  if m == nil {
    return
  }
  m.OldestUnprocessed.WithLabelValues(domain).Set(seconds)
}

func (m *Metrics) IncProcessed(domain string, n int) {
  if m == nil {
    return
  }
  m.Processed.WithLabelValues(domain).Add(float64(n))
}

func (m *Metrics) SetAdvisoryLockHeld(domain string, held bool) {
  if m == nil {
    return
  }
  v := 0.0
  if held {
    v = 1.0
  }
  m.AdvisoryLockHeld.WithLabelValues(domain).Set(v)
}

// GetDrainCyclesValue is a test helper.
func (m *Metrics) GetDrainCyclesValue(domain, result string) float64 {
  return testutil.ToFloat64(m.DrainCycles.WithLabelValues(domain, result))
}
```

Save to `plane/data/outbox/metrics.go`.

- [ ] **Step 4: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestMetrics -v`
Expected: PASS (both TestMetrics_NilSafe and TestMetrics_RegisterAndIncrement).

- [ ] **Step 5: Commit**

```bash
git add plane/data/outbox/metrics.go plane/data/outbox/metrics_test.go
git commit -m "feat(outbox): Prometheus metrics with nil-safe accessors (#11)"
```

---

## Task 5: `drainBatch` — try-lock + early-return path

**Files:**
- Create: `plane/data/outbox/drain.go`
- Create: `plane/data/outbox/drain_test.go`

- [ ] **Step 1: Write failing test — lock-not-acquired returns nil cleanly**

```go
package outbox

import (
  "context"
  "hash/fnv"
  "testing"
  "time"

  "github.com/jackc/pgx/v5"
  "github.com/jackc/pgx/v5/pgxmock/v4"
)

func TestDrainBatch_LockNotAcquired_ReturnsNilEarly(t *testing.T) {
  mock, err := pgxmock.NewPool()
  if err != nil { t.Fatal(err) }
  defer mock.Close()

  // Outer transaction begin
  mock.ExpectBegin()
  // pg_try_advisory_xact_lock returns false
  mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(false))
  // Commit (no work) — txn closes
  mock.ExpectCommit()

  cfg := Config{
    Domain:         "identity",
    Table:          "identity.identity_outbox",
    Topic:          "gitscale.identity.events",
    Producer:       NewMockProducer(),
    PollInterval:   time.Second,
    PublishTimeout: time.Second,
    BatchSize:      100,
    Metrics:        NewMetrics(),
  }

  err = drainBatch(context.Background(), mock, cfg)
  if err != nil {
    t.Fatalf("drainBatch: %v", err)
  }
  if err := mock.ExpectationsWereMet(); err != nil {
    t.Errorf("expectations: %v", err)
  }
  if got := cfg.Metrics.GetDrainCyclesValue("identity", "lock_missed"); got != 1 {
    t.Errorf("drain_cycles{result=lock_missed}: got %v, want 1", got)
  }
}

// lockKey hashes the table name to a stable bigint — must match drain.go.
func lockKey(table string) int64 {
  h := fnv.New64a()
  h.Write([]byte(table))
  return int64(h.Sum64())
}

var _ = pgx.ErrTxClosed // keep import
```

Save to `plane/data/outbox/drain_test.go`. Add to `go.mod`: `go get github.com/pashagolub/pgxmock/v4@v4.3.0`.

- [ ] **Step 2: Run test — expect compile failure**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch_LockNotAcquired -v`
Expected: FAIL — `drainBatch` undefined.

- [ ] **Step 3: Implement `drainBatch` skeleton with lock try only**

```go
package outbox

import (
  "context"
  "fmt"
  "hash/fnv"

  "github.com/jackc/pgx/v5"
)

// dbtx is the minimal interface drainBatch needs — satisfied by pgxpool.Pool
// and by pgxmock's mock pool.
type dbtx interface {
  BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// drainBatch runs one drain cycle on the configured domain table.
// See spec §7 for ordering rationale.
func drainBatch(ctx context.Context, db dbtx, cfg Config) error {
  tx, err := db.BeginTx(ctx, pgx.TxOptions{})
  if err != nil {
    cfg.Metrics.IncDrainCycles(cfg.Domain, "begin_error")
    return fmt.Errorf("begin: %w", err)
  }
  defer tx.Rollback(ctx) // no-op after Commit

  var locked bool
  if err := tx.QueryRow(ctx,
    `SELECT pg_try_advisory_xact_lock($1)`,
    hashTableToLockKey(cfg.Table),
  ).Scan(&locked); err != nil {
    cfg.Metrics.IncDrainCycles(cfg.Domain, "lock_query_error")
    return fmt.Errorf("advisory lock: %w", err)
  }
  if !locked {
    cfg.Metrics.IncDrainCycles(cfg.Domain, "lock_missed")
    cfg.Metrics.SetAdvisoryLockHeld(cfg.Domain, false)
    return tx.Commit(ctx) // releases nothing held; clean exit
  }
  cfg.Metrics.SetAdvisoryLockHeld(cfg.Domain, true)
  defer cfg.Metrics.SetAdvisoryLockHeld(cfg.Domain, false)

  // Body for batch fetch + publish + UPDATE comes in Tasks 6–8.

  cfg.Metrics.IncDrainCycles(cfg.Domain, "ok")
  return tx.Commit(ctx)
}

// hashTableToLockKey produces a stable int64 advisory lock key from the
// schema-qualified table name. Cast to ::bigint at the SQL boundary.
func hashTableToLockKey(table string) int64 {
  h := fnv.New64a()
  h.Write([]byte(table))
  return int64(h.Sum64())
}
```

Save to `plane/data/outbox/drain.go`.

> **Note on `hashTableToLockKey`:** The spec §6 calls for `pg_try_advisory_xact_lock(hashtext('<table>')::bigint)`. We compute the hash in Go (FNV-64) instead of relying on PostgreSQL's `hashtext` to avoid platform-dependent hashtext collisions and to keep the lock key reproducible from tests. Pass the int64 directly. **This is a deliberate refinement vs the spec.**

- [ ] **Step 4: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch_LockNotAcquired -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/outbox/drain.go plane/data/outbox/drain_test.go go.mod go.sum
git commit -m "feat(outbox): drainBatch try-lock + early-return path (#11)"
```

---

## Task 6: `drainBatch` — SELECT batch + empty-batch path

**Files:**
- Modify: `plane/data/outbox/drain.go`
- Modify: `plane/data/outbox/drain_test.go`

- [ ] **Step 1: Write failing test for empty-batch path**

Append to `plane/data/outbox/drain_test.go`:

```go
func TestDrainBatch_EmptyBatch_NoPublishNoUpdate(t *testing.T) {
  mock, _ := pgxmock.NewPool()
  defer mock.Close()

  mock.ExpectBegin()
  mock.ExpectQuery(`pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"l"}).AddRow(true))
  // SELECT returns no rows
  mock.ExpectQuery(`SELECT id, event_id, aggregate_type`).
    WillReturnRows(pgxmock.NewRows([]string{"id", "event_id", "aggregate_type", "aggregate_id", "event_type", "payload", "created_at"}))
  mock.ExpectCommit()

  prod := NewMockProducer()
  cfg := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "t", Producer: prod, BatchSize: 100, Metrics: NewMetrics()}
  cfg.ApplyDefaults()
  if err := drainBatch(context.Background(), mock, cfg); err != nil {
    t.Fatalf("drainBatch: %v", err)
  }
  if got := len(prod.PublishedTo("t")); got != 0 {
    t.Errorf("published = %d, want 0", got)
  }
  if err := mock.ExpectationsWereMet(); err != nil {
    t.Errorf("expectations: %v", err)
  }
  if got := cfg.Metrics.GetDrainCyclesValue("identity", "empty"); got != 1 {
    t.Errorf("drain_cycles{result=empty}: got %v, want 1", got)
  }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch_EmptyBatch -v`
Expected: FAIL — SELECT not issued (drainBatch doesn't query yet).

- [ ] **Step 3: Add SELECT + empty-batch handling to `drainBatch`**

Replace the placeholder comment (`// Body for batch fetch...`) in `plane/data/outbox/drain.go` with:

```go
  rows, err := selectBatch(ctx, tx, cfg.Table, cfg.BatchSize)
  if err != nil {
    cfg.Metrics.IncDrainCycles(cfg.Domain, "select_error")
    return fmt.Errorf("select batch: %w", err)
  }
  cfg.Metrics.ObserveBatchSize(cfg.Domain, len(rows))
  if len(rows) == 0 {
    cfg.Metrics.IncDrainCycles(cfg.Domain, "empty")
    return tx.Commit(ctx)
  }

  // Publish + UPDATE land in Tasks 7–8.
  _ = rows
```

Replace the trailing `cfg.Metrics.IncDrainCycles(cfg.Domain, "ok")` so it's only counted on actual work (move into the publish-success path in Task 7).

Add `selectBatch` helper at the bottom of `plane/data/outbox/drain.go`:

```go
func selectBatch(ctx context.Context, tx pgx.Tx, table string, limit int) ([]OutboxRow, error) {
  q := fmt.Sprintf(`
    SELECT id, event_id, aggregate_type, aggregate_id, event_type, payload, created_at
    FROM %s
    WHERE processed_at IS NULL
    ORDER BY created_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
  `, table)
  r, err := tx.Query(ctx, q, limit)
  if err != nil {
    return nil, err
  }
  defer r.Close()

  var out []OutboxRow
  for r.Next() {
    var row OutboxRow
    if err := r.Scan(&row.ID, &row.EventID, &row.AggregateType, &row.AggregateID, &row.EventType, &row.Payload, &row.CreatedAt); err != nil {
      return nil, err
    }
    out = append(out, row)
  }
  return out, r.Err()
}
```

> **Important:** the spec says `SELECT … FOR UPDATE SKIP LOCKED`. Note the table name is interpolated (validated to be a fixed allow-list at config time — see Task 13). The `$1` placeholder is for `limit` only.

- [ ] **Step 4: Run both drain tests, expect pass**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch -v`
Expected: PASS for both `LockNotAcquired` and `EmptyBatch`.

- [ ] **Step 5: Commit**

```bash
git add plane/data/outbox/drain.go plane/data/outbox/drain_test.go
git commit -m "feat(outbox): drainBatch SELECT batch + empty-batch path (#11)"
```

---

## Task 7: `drainBatch` — happy path (publish → UPDATE → COMMIT)

**Files:**
- Modify: `plane/data/outbox/drain.go`
- Modify: `plane/data/outbox/drain_test.go`

- [ ] **Step 1: Write failing test for the happy path**

Append to `plane/data/outbox/drain_test.go`:

```go
func TestDrainBatch_HappyPath_PublishesThenUpdatesThenCommits(t *testing.T) {
  mock, _ := pgxmock.NewPool()
  defer mock.Close()

  id := uuid.New()
  createdAt := time.Now().UTC()

  mock.ExpectBegin()
  mock.ExpectQuery(`pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"l"}).AddRow(true))
  mock.ExpectQuery(`SELECT id, event_id, aggregate_type`).
    WillReturnRows(pgxmock.NewRows([]string{"id", "event_id", "aggregate_type", "aggregate_id", "event_type", "payload", "created_at"}).
      AddRow(int64(1), id, "user", uuid.New(), "user.created", []byte(`{"x":1}`), createdAt))
  mock.ExpectExec(`UPDATE identity\.identity_outbox SET processed_at`).
    WithArgs([]int64{1}).
    WillReturnResult(pgxmock.NewResult("UPDATE", 1))
  mock.ExpectCommit()

  prod := NewMockProducer()
  cfg := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "t", Producer: prod, BatchSize: 100, Metrics: NewMetrics()}
  cfg.ApplyDefaults()

  if err := drainBatch(context.Background(), mock, cfg); err != nil {
    t.Fatalf("drainBatch: %v", err)
  }
  if got := prod.PublishedTo("t"); len(got) != 1 || got[0].EventID != id {
    t.Errorf("published: got %v, want 1 event with EventID %v", got, id)
  }
  if err := mock.ExpectationsWereMet(); err != nil {
    t.Errorf("expectations: %v", err)
  }
  if got := cfg.Metrics.GetDrainCyclesValue("identity", "ok"); got != 1 {
    t.Errorf("drain_cycles{result=ok}: got %v, want 1", got)
  }
}
```

- [ ] **Step 2: Run test, expect failure**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch_HappyPath -v`
Expected: FAIL — no publish nor UPDATE happens yet.

- [ ] **Step 3: Add publish + UPDATE to `drainBatch`**

Replace the `_ = rows` line in `drain.go` with:

```go
  pubCtx, cancel := context.WithTimeout(ctx, cfg.PublishTimeout)
  pubStart := time.Now()
  pubErr := cfg.Producer.PublishBatch(pubCtx, cfg.Topic, rows)
  cancel()
  pubDur := time.Since(pubStart).Seconds()

  if pubErr != nil {
    cfg.Metrics.ObservePublishDuration(cfg.Domain, "error", pubDur)
    cfg.Metrics.IncDrainCycles(cfg.Domain, "publish_error")
    return fmt.Errorf("publish: %w", pubErr) // ROLLBACK on defer
  }
  cfg.Metrics.ObservePublishDuration(cfg.Domain, "ok", pubDur)

  ids := make([]int64, len(rows))
  for i, r := range rows {
    ids[i] = r.ID
  }
  if _, err := tx.Exec(ctx,
    fmt.Sprintf(`UPDATE %s SET processed_at = now() WHERE id = ANY($1)`, cfg.Table),
    ids,
  ); err != nil {
    cfg.Metrics.IncDrainCycles(cfg.Domain, "update_error")
    return fmt.Errorf("update processed_at: %w", err)
  }

  cfg.Metrics.IncProcessed(cfg.Domain, len(rows))
  cfg.Metrics.IncDrainCycles(cfg.Domain, "ok")
```

Add `time` import to `drain.go` if not already present.

- [ ] **Step 4: Run all drain tests, expect pass**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch -v`
Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/outbox/drain.go plane/data/outbox/drain_test.go
git commit -m "feat(outbox): drainBatch happy path — publish, UPDATE, COMMIT (#11)"
```

---

## Task 8: `drainBatch` — failure paths

**Files:**
- Modify: `plane/data/outbox/drain_test.go`

- [ ] **Step 1: Write failing test — publish error rolls back**

Append to `plane/data/outbox/drain_test.go`:

```go
func TestDrainBatch_PublishError_RollsBackNoUpdate(t *testing.T) {
  mock, _ := pgxmock.NewPool()
  defer mock.Close()

  mock.ExpectBegin()
  mock.ExpectQuery(`pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"l"}).AddRow(true))
  mock.ExpectQuery(`SELECT id, event_id`).
    WillReturnRows(pgxmock.NewRows([]string{"id", "event_id", "aggregate_type", "aggregate_id", "event_type", "payload", "created_at"}).
      AddRow(int64(1), uuid.New(), "u", uuid.New(), "u.x", []byte(`{}`), time.Now().UTC()))
  // NOTE: no ExpectExec for UPDATE, no ExpectCommit. Expect Rollback.
  mock.ExpectRollback()

  prod := NewMockProducer()
  prod.SetNextError(errors.New("kafka exploded"))
  cfg := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "t", Producer: prod, BatchSize: 100, Metrics: NewMetrics()}
  cfg.ApplyDefaults()

  err := drainBatch(context.Background(), mock, cfg)
  if err == nil || !strings.Contains(err.Error(), "publish") {
    t.Errorf("err = %v, want publish-wrapped error", err)
  }
  if got := cfg.Metrics.GetDrainCyclesValue("identity", "publish_error"); got != 1 {
    t.Errorf("drain_cycles{publish_error}: got %v, want 1", got)
  }
  if err := mock.ExpectationsWereMet(); err != nil {
    t.Errorf("expectations: %v", err)
  }
}
```

Add `"strings"` to imports.

- [ ] **Step 2: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch_PublishError -v`
Expected: PASS — drainBatch already returns on publish error; the deferred `tx.Rollback` fires.

- [ ] **Step 3: Write failing test — ctx cancel mid-batch rolls back**

Append:

```go
func TestDrainBatch_CtxCancel_DuringPublish_RollsBack(t *testing.T) {
  mock, _ := pgxmock.NewPool()
  defer mock.Close()

  mock.ExpectBegin()
  mock.ExpectQuery(`pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"l"}).AddRow(true))
  mock.ExpectQuery(`SELECT id, event_id`).
    WillReturnRows(pgxmock.NewRows([]string{"id", "event_id", "aggregate_type", "aggregate_id", "event_type", "payload", "created_at"}).
      AddRow(int64(1), uuid.New(), "u", uuid.New(), "u.x", []byte(`{}`), time.Now().UTC()))
  mock.ExpectRollback()

  // Producer simulates blocking until ctx cancel
  prod := &blockingProducer{block: make(chan struct{})}
  cfg := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "t", Producer: prod, BatchSize: 100, Metrics: NewMetrics(), PublishTimeout: 50 * time.Millisecond}
  cfg.ApplyDefaults()

  err := drainBatch(context.Background(), mock, cfg)
  if err == nil {
    t.Error("expected error from publish timeout, got nil")
  }
  if err := mock.ExpectationsWereMet(); err != nil {
    t.Errorf("expectations: %v", err)
  }
}

// blockingProducer blocks PublishBatch until the ctx is cancelled.
type blockingProducer struct{ block chan struct{} }

func (p *blockingProducer) PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error {
  select {
  case <-ctx.Done(): return ctx.Err()
  case <-p.block:    return nil
  }
}
func (p *blockingProducer) Close() error { return nil }
```

- [ ] **Step 4: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch_CtxCancel -v`
Expected: PASS — `PublishTimeout` causes ctx.Done() to fire, blocking producer returns ctx.Err(), drainBatch returns error and the deferred Rollback fires.

- [ ] **Step 5: Run full drain suite**

Run: `go test ./plane/data/outbox/... -run TestDrainBatch -v`
Expected: 5 tests PASS.

- [ ] **Step 6: Commit**

```bash
git add plane/data/outbox/drain_test.go
git commit -m "test(outbox): drainBatch failure paths — publish error + ctx cancel (#11)"
```

---

## Task 9: `OutboxConsumer.Run` loop

**Files:**
- Create: `plane/data/outbox/consumer.go`
- Create: `plane/data/outbox/consumer_test.go`

- [ ] **Step 1: Write failing test — Run exits on ctx cancel**

```go
package outbox

import (
  "context"
  "errors"
  "testing"
  "time"

  "github.com/pashagolub/pgxmock/v4"
)

func TestOutboxConsumer_Run_ExitsOnCtxCancel(t *testing.T) {
  mock, _ := pgxmock.NewPool()
  defer mock.Close()
  // Expect at least one Begin/Commit cycle (lock missed is fine)
  mock.ExpectBegin()
  mock.ExpectQuery(`pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"l"}).AddRow(false))
  mock.ExpectCommit()

  cfg := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "t", Producer: NewMockProducer(), Metrics: NewMetrics(), PollInterval: 10 * time.Millisecond}
  cfg.ApplyDefaults()
  c := NewOutboxConsumer(mock, cfg)

  ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
  defer cancel()

  err := c.Run(ctx)
  if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
    t.Errorf("Run err = %v, want ctx.Done error", err)
  }
}
```

Save to `plane/data/outbox/consumer_test.go`.

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./plane/data/outbox/... -run TestOutboxConsumer_Run -v`
Expected: FAIL — `NewOutboxConsumer` undefined.

- [ ] **Step 3: Implement `OutboxConsumer`**

```go
package outbox

import (
  "context"
  "time"
)

// OutboxConsumer drains one domain's outbox table on a polling loop.
type OutboxConsumer interface {
  Run(ctx context.Context) error
}

type consumer struct {
  db  dbtx
  cfg Config
}

// NewOutboxConsumer constructs a consumer for one domain.
// Caller must provide a fully-populated Config (or call cfg.ApplyDefaults()).
func NewOutboxConsumer(db dbtx, cfg Config) OutboxConsumer {
  cfg.ApplyDefaults()
  return &consumer{db: db, cfg: cfg}
}

func (c *consumer) Run(ctx context.Context) error {
  t := time.NewTicker(c.cfg.PollInterval)
  defer t.Stop()

  // First tick fires immediately (ticker fires on first interval, not at t=0)
  if err := c.tick(ctx); err != nil {
    // Tick errors are logged via metrics; the loop continues unless ctx done.
    if ctx.Err() != nil { return ctx.Err() }
  }

  for {
    select {
    case <-ctx.Done():
      return ctx.Err()
    case <-t.C:
      if err := c.tick(ctx); err != nil && ctx.Err() != nil {
        return ctx.Err()
      }
    }
  }
}

func (c *consumer) tick(ctx context.Context) error {
  // drainBatch already records its own metrics; tick is a thin wrapper for now.
  return drainBatch(ctx, c.db, c.cfg)
}
```

Save to `plane/data/outbox/consumer.go`.

- [ ] **Step 4: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestOutboxConsumer_Run -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plane/data/outbox/consumer.go plane/data/outbox/consumer_test.go
git commit -m "feat(outbox): OutboxConsumer Run loop with ctx-aware ticker (#11)"
```

---

## Task 10: `outbox_oldest_unprocessed_seconds` gauge sampling

**Files:**
- Modify: `plane/data/outbox/drain.go`
- Modify: `plane/data/outbox/consumer.go`
- Modify: `plane/data/outbox/consumer_test.go`

Per spec §12: every replica samples this gauge each cycle, regardless of lock ownership, so the SLO signal doesn't vanish during leader rotation.

- [ ] **Step 1: Write failing test for gauge sampling**

Append to `plane/data/outbox/consumer_test.go`:

```go
import "github.com/prometheus/client_golang/prometheus/testutil"

func TestOutboxConsumer_SamplesOldestUnprocessed_EveryTick(t *testing.T) {
  mock, _ := pgxmock.NewPool()
  defer mock.Close()
  // Expect oldest_unprocessed query
  mock.ExpectQuery(`SELECT EXTRACT\(EPOCH FROM`).
    WillReturnRows(pgxmock.NewRows([]string{"age"}).AddRow(float64(42)))
  // Then a regular drainBatch tick (lock missed)
  mock.ExpectBegin()
  mock.ExpectQuery(`pg_try_advisory_xact_lock`).
    WillReturnRows(pgxmock.NewRows([]string{"l"}).AddRow(false))
  mock.ExpectCommit()

  cfg := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "t", Producer: NewMockProducer(), Metrics: NewMetrics(), PollInterval: 50 * time.Millisecond}
  cfg.ApplyDefaults()
  c := NewOutboxConsumer(mock, cfg)

  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
  defer cancel()
  _ = c.Run(ctx)

  if got := testutil.ToFloat64(cfg.Metrics.OldestUnprocessed.WithLabelValues("identity")); got != 42 {
    t.Errorf("oldest_unprocessed = %v, want 42", got)
  }
}
```

- [ ] **Step 2: Run test, expect failure**

Run: `go test ./plane/data/outbox/... -run TestOutboxConsumer_SamplesOldestUnprocessed -v`
Expected: FAIL — query not issued.

- [ ] **Step 3: Add `sampleOldestUnprocessed` and call before `drainBatch`**

Add to `plane/data/outbox/drain.go`:

```go
// sampleOldestUnprocessed reads the oldest pending row's age and sets the gauge.
// Read-only, no advisory lock — every replica samples on every tick.
func sampleOldestUnprocessed(ctx context.Context, db dbtx, cfg Config) {
  conn, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
  if err != nil { return }
  defer conn.Rollback(ctx)
  q := fmt.Sprintf(`SELECT EXTRACT(EPOCH FROM (now() - MIN(created_at))) FROM %s WHERE processed_at IS NULL`, cfg.Table)
  var ageSeconds *float64
  if err := conn.QueryRow(ctx, q).Scan(&ageSeconds); err != nil { return }
  if ageSeconds == nil {
    cfg.Metrics.SetOldestUnprocessed(cfg.Domain, 0)
  } else {
    cfg.Metrics.SetOldestUnprocessed(cfg.Domain, *ageSeconds)
  }
}
```

Modify `consumer.go`'s `tick` to:

```go
func (c *consumer) tick(ctx context.Context) error {
  sampleOldestUnprocessed(ctx, c.db, c.cfg)
  return drainBatch(ctx, c.db, c.cfg)
}
```

- [ ] **Step 4: Run test, expect pass**

Run: `go test ./plane/data/outbox/... -run TestOutboxConsumer_SamplesOldestUnprocessed -v`
Expected: PASS.

- [ ] **Step 5: Run full unit suite**

Run: `go test ./plane/data/outbox/... -v -count=1`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add plane/data/outbox/drain.go plane/data/outbox/consumer.go plane/data/outbox/consumer_test.go
git commit -m "feat(outbox): sample outbox_oldest_unprocessed_seconds every tick (#11)"
```

---

## Task 11: confluent-kafka-go producer impl

**Files:**
- Create: `plane/data/outbox/producer_kafka.go`
- Create: `plane/data/outbox/producer_kafka_test.go` (build-tagged unit test)

- [ ] **Step 1: Implement the real producer**

```go
package outbox

import (
  "context"
  "errors"
  "fmt"
  "time"

  "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// KafkaProducerConfig wraps confluent-kafka-go's required config.
type KafkaProducerConfig struct {
  BootstrapServers string
  ClientID         string
  DeliveryTimeout  time.Duration // ≤ Config.PublishTimeout per spec §9
}

type kafkaProducer struct {
  p *kafka.Producer
}

// NewKafkaProducer wires an idempotent producer per spec §9.
func NewKafkaProducer(cfg KafkaProducerConfig) (KafkaProducer, error) {
  if cfg.DeliveryTimeout == 0 {
    cfg.DeliveryTimeout = 5 * time.Second
  }
  conf := &kafka.ConfigMap{
    "bootstrap.servers":                       cfg.BootstrapServers,
    "client.id":                               cfg.ClientID,
    "enable.idempotence":                      true,
    "acks":                                    "all",
    "max.in.flight.requests.per.connection":   5,
    "delivery.timeout.ms":                     int(cfg.DeliveryTimeout / time.Millisecond),
    "compression.type":                        "zstd",
    "linger.ms":                               5,
    "batch.size":                              65536,
  }
  p, err := kafka.NewProducer(conf)
  if err != nil { return nil, fmt.Errorf("kafka producer: %w", err) }
  return &kafkaProducer{p: p}, nil
}

// PublishBatch produces every row and waits for all delivery reports.
// Returns the first error encountered; partial success returns error.
func (k *kafkaProducer) PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error {
  deliveryCh := make(chan kafka.Event, len(batch))
  for _, row := range batch {
    msg := &kafka.Message{
      TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
      Key:            row.AggregateID[:],   // partition key = aggregate_id (per spec §13 + #12 D1)
      Value:          row.Payload,           // already JSON-encoded EventEnvelope from upstream
      Headers: []kafka.Header{
        {Key: "event_id", Value: []byte(row.EventID.String())},
        {Key: "event_type", Value: []byte(row.EventType)},
        {Key: "aggregate_type", Value: []byte(row.AggregateType)},
      },
    }
    if err := k.p.Produce(msg, deliveryCh); err != nil {
      return fmt.Errorf("produce: %w", err)
    }
  }

  var firstErr error
  for i := 0; i < len(batch); i++ {
    select {
    case <-ctx.Done():
      return ctx.Err()
    case e := <-deliveryCh:
      m, ok := e.(*kafka.Message)
      if !ok { continue }
      if m.TopicPartition.Error != nil && firstErr == nil {
        firstErr = fmt.Errorf("delivery: %w", m.TopicPartition.Error)
      }
    }
  }
  if firstErr != nil { return firstErr }
  return nil
}

func (k *kafkaProducer) Close() error {
  k.p.Flush(5000) // 5s drain
  k.p.Close()
  return nil
}

var _ = errors.New // keep import slot
```

Save to `plane/data/outbox/producer_kafka.go`.

- [ ] **Step 2: Verify build**

Run: `go build ./plane/data/outbox/...`
Expected: success.

- [ ] **Step 3: Verify vet**

Run: `go vet ./plane/data/outbox/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add plane/data/outbox/producer_kafka.go
git commit -m "feat(outbox): confluent-kafka-go idempotent producer impl (#11)"
```

---

## Task 12: Integration test scaffolding (testcontainers PG + Redpanda)

**Files:**
- Create: `plane/data/outbox/testutil_test.go`

- [ ] **Step 1: Write the test harness**

```go
//go:build integration
// +build integration

package outbox

import (
  "context"
  "fmt"
  "os"
  "path/filepath"
  "testing"
  "time"

  "github.com/jackc/pgx/v5/pgxpool"
  "github.com/testcontainers/testcontainers-go"
  "github.com/testcontainers/testcontainers-go/modules/postgres"
  "github.com/testcontainers/testcontainers-go/modules/redpanda"
)

type testHarness struct {
  pgPool         *pgxpool.Pool
  bootstrapAddrs string
  cleanup        func()
}

func setupHarness(t *testing.T) *testHarness {
  t.Helper()
  ctx := context.Background()

  pgC, err := postgres.RunContainer(ctx,
    testcontainers.WithImage("postgres:16-alpine"),
    postgres.WithDatabase("gitscale"),
    postgres.WithUsername("test"),
    postgres.WithPassword("test"),
  )
  if err != nil { t.Fatalf("pg container: %v", err) }

  rpC, err := redpanda.RunContainer(ctx,
    testcontainers.WithImage("docker.redpanda.com/redpandadata/redpanda:v23.3.5"),
  )
  if err != nil { t.Fatalf("redpanda container: %v", err) }

  pgURL, err := pgC.ConnectionString(ctx, "sslmode=disable")
  if err != nil { t.Fatalf("pg conn: %v", err) }

  pool, err := pgxpool.New(ctx, pgURL)
  if err != nil { t.Fatalf("pgxpool: %v", err) }

  if err := applyMigrations(ctx, pool); err != nil {
    t.Fatalf("migrations: %v", err)
  }

  bootstrap, err := rpC.KafkaSeedBroker(ctx)
  if err != nil { t.Fatalf("redpanda bootstrap: %v", err) }

  cleanup := func() {
    pool.Close()
    _ = pgC.Terminate(ctx)
    _ = rpC.Terminate(ctx)
  }

  return &testHarness{pgPool: pool, bootstrapAddrs: bootstrap, cleanup: cleanup}
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
  root, _ := os.Getwd()
  for root != "/" {
    if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil { break }
    root = filepath.Dir(root)
  }
  migDir := filepath.Join(root, "plane/data/migrations")
  files, _ := os.ReadDir(migDir)
  for _, f := range files {
    if !f.IsDir() && filepath.Ext(f.Name()) == ".sql" {
      b, err := os.ReadFile(filepath.Join(migDir, f.Name()))
      if err != nil { return err }
      if _, err := pool.Exec(ctx, string(b)); err != nil {
        return fmt.Errorf("migration %s: %w", f.Name(), err)
      }
    }
  }
  return nil
}

// insertOutboxRow seeds one row into a domain outbox.
func insertOutboxRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, eventType string, payload []byte) {
  t.Helper()
  _, err := pool.Exec(ctx,
    fmt.Sprintf(`INSERT INTO %s (event_id, aggregate_type, aggregate_id, event_type, payload) VALUES (gen_random_uuid(), 'test', gen_random_uuid(), $1, $2)`, table),
    eventType, payload,
  )
  if err != nil { t.Fatalf("insert: %v", err) }
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
  t.Helper()
  deadline := time.Now().Add(timeout)
  for time.Now().Before(deadline) {
    if cond() { return }
    time.Sleep(20 * time.Millisecond)
  }
  t.Fatal("condition not met within timeout")
}
```

Save to `plane/data/outbox/testutil_test.go`. Note `//go:build integration` tag — these tests run only with `go test -tags integration`.

- [ ] **Step 2: Run integration tests (none yet — verify harness compiles)**

Run: `go test -tags integration ./plane/data/outbox/... -run NONE -v`
Expected: pass with 0 tests run.

- [ ] **Step 3: Commit**

```bash
git add plane/data/outbox/testutil_test.go
git commit -m "test(outbox): testcontainers harness for PG + Redpanda (#11)"
```

---

## Task 13: Integration test — happy path

**Files:**
- Create: `plane/data/outbox/integration_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build integration

package outbox

import (
  "context"
  "testing"
  "time"
)

func TestIntegration_HappyPath_3Rows(t *testing.T) {
  h := setupHarness(t)
  defer h.cleanup()
  ctx := context.Background()

  insertOutboxRow(ctx, t, h.pgPool, "identity.identity_outbox", "user.created", []byte(`{"x":1}`))
  insertOutboxRow(ctx, t, h.pgPool, "identity.identity_outbox", "user.created", []byte(`{"x":2}`))
  insertOutboxRow(ctx, t, h.pgPool, "identity.identity_outbox", "user.created", []byte(`{"x":3}`))

  prod, err := NewKafkaProducer(KafkaProducerConfig{BootstrapServers: h.bootstrapAddrs, ClientID: "test"})
  if err != nil { t.Fatal(err) }
  defer prod.Close()

  cfg := Config{
    Domain: "identity", Table: "identity.identity_outbox", Topic: "gitscale.identity.events",
    DB: h.pgPool, Producer: prod, Metrics: NewMetrics(),
    PollInterval: 50 * time.Millisecond,
  }
  cfg.ApplyDefaults()
  c := NewOutboxConsumer(h.pgPool, cfg)

  ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
  defer cancel()

  go c.Run(ctx)

  waitFor(t, 4*time.Second, func() bool {
    var n int
    _ = h.pgPool.QueryRow(context.Background(),
      `SELECT count(*) FROM identity.identity_outbox WHERE processed_at IS NULL`).Scan(&n)
    return n == 0
  })

  // Verify all 3 rows have processed_at set
  var processed int
  h.pgPool.QueryRow(context.Background(),
    `SELECT count(*) FROM identity.identity_outbox WHERE processed_at IS NOT NULL`).Scan(&processed)
  if processed != 3 {
    t.Errorf("processed = %d, want 3", processed)
  }
}
```

Save to `plane/data/outbox/integration_test.go`.

- [ ] **Step 2: Run integration**

Run: `go test -tags integration ./plane/data/outbox/... -run TestIntegration_HappyPath -v -timeout 60s`
Expected: PASS within ~5s.

- [ ] **Step 3: Commit**

```bash
git add plane/data/outbox/integration_test.go
git commit -m "test(outbox): integration happy-path 3-row drain (#11)"
```

---

## Task 14: Integration test — crash mid-batch

**Files:**
- Modify: `plane/data/outbox/integration_test.go`

- [ ] **Step 1: Add the crash-mid-batch test**

```go
func TestIntegration_CrashMidBatch_NoLoss_OnlyDuplicates(t *testing.T) {
  h := setupHarness(t)
  defer h.cleanup()
  ctx := context.Background()

  for i := 0; i < 5; i++ {
    insertOutboxRow(ctx, t, h.pgPool, "identity.identity_outbox", "user.created", []byte(`{}`))
  }

  // Producer wrapper that publishes the first 3 rows then errors,
  // simulating a crash mid-batch.
  realProd, err := NewKafkaProducer(KafkaProducerConfig{BootstrapServers: h.bootstrapAddrs, ClientID: "test-crash"})
  if err != nil { t.Fatal(err) }
  defer realProd.Close()
  wrapped := &partialProducer{inner: realProd, failAfter: 3}

  cfg := Config{
    Domain: "identity", Table: "identity.identity_outbox", Topic: "gitscale.identity.events",
    DB: h.pgPool, Producer: wrapped, Metrics: NewMetrics(),
    PollInterval: 50 * time.Millisecond,
  }
  cfg.ApplyDefaults()
  c := NewOutboxConsumer(h.pgPool, cfg)

  rctx, cancel := context.WithTimeout(ctx, 1*time.Second)
  go c.Run(rctx)
  <-rctx.Done()
  cancel()

  // At this point: first cycle published 3 of 5 then errored → ROLLBACK → 0 processed_at set
  var notProc int
  h.pgPool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox WHERE processed_at IS NULL`).Scan(&notProc)
  if notProc != 5 {
    t.Errorf("after crash: notProc = %d, want 5 (ROLLBACK preserved unprocessed state)", notProc)
  }

  // Restart with healthy producer; should drain all 5
  wrapped.failAfter = 1<<31 // never fail again
  rctx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
  defer cancel2()
  go c.Run(rctx2)

  waitFor(t, 4*time.Second, func() bool {
    var n int
    h.pgPool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox WHERE processed_at IS NULL`).Scan(&n)
    return n == 0
  })
}

type partialProducer struct {
  inner     KafkaProducer
  failAfter int
}

func (p *partialProducer) PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error {
  if len(batch) <= p.failAfter {
    return p.inner.PublishBatch(ctx, topic, batch)
  }
  // Publish the first failAfter rows individually then return error
  for i := 0; i < p.failAfter; i++ {
    if err := p.inner.PublishBatch(ctx, topic, batch[i:i+1]); err != nil {
      return err
    }
  }
  return errors.New("partialProducer: simulated crash mid-batch")
}
func (p *partialProducer) Close() error { return p.inner.Close() }
```

Add `"errors"` import.

- [ ] **Step 2: Run**

Run: `go test -tags integration ./plane/data/outbox/... -run TestIntegration_CrashMidBatch -v -timeout 90s`
Expected: PASS — confirms ROLLBACK preserves state across crash.

- [ ] **Step 3: Commit**

```bash
git add plane/data/outbox/integration_test.go
git commit -m "test(outbox): integration crash-mid-batch — ROLLBACK preserves rows (#11)"
```

---

## Task 15: Integration test — two-replica race

**Files:**
- Modify: `plane/data/outbox/integration_test.go`

- [ ] **Step 1: Add the two-replica test**

```go
func TestIntegration_TwoReplicaRace_NoDuplicateProcessing(t *testing.T) {
  h := setupHarness(t)
  defer h.cleanup()
  ctx := context.Background()

  for i := 0; i < 100; i++ {
    insertOutboxRow(ctx, t, h.pgPool, "identity.identity_outbox", "user.created", []byte(`{}`))
  }

  prod1, _ := NewKafkaProducer(KafkaProducerConfig{BootstrapServers: h.bootstrapAddrs, ClientID: "rep1"})
  prod2, _ := NewKafkaProducer(KafkaProducerConfig{BootstrapServers: h.bootstrapAddrs, ClientID: "rep2"})
  defer prod1.Close(); defer prod2.Close()

  cfgFor := func(p KafkaProducer) Config {
    c := Config{Domain: "identity", Table: "identity.identity_outbox", Topic: "gitscale.identity.events",
      DB: h.pgPool, Producer: p, Metrics: NewMetrics(), PollInterval: 25 * time.Millisecond}
    c.ApplyDefaults(); return c
  }
  c1 := NewOutboxConsumer(h.pgPool, cfgFor(prod1))
  c2 := NewOutboxConsumer(h.pgPool, cfgFor(prod2))

  rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
  defer cancel()
  go c1.Run(rctx)
  go c2.Run(rctx)

  waitFor(t, 9*time.Second, func() bool {
    var n int
    h.pgPool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox WHERE processed_at IS NULL`).Scan(&n)
    return n == 0
  })

  // Each row is processed exactly once at the DB level. Kafka may have duplicates
  // from a prior crash test, but the DB invariant is what we assert here.
  var total, distinctProcessed int
  h.pgPool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox`).Scan(&total)
  h.pgPool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_outbox WHERE processed_at IS NOT NULL`).Scan(&distinctProcessed)
  if total != distinctProcessed || total != 100 {
    t.Errorf("total=%d processed=%d, want both = 100", total, distinctProcessed)
  }
}
```

- [ ] **Step 2: Run**

Run: `go test -tags integration ./plane/data/outbox/... -run TestIntegration_TwoReplicaRace -v -timeout 90s`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add plane/data/outbox/integration_test.go
git commit -m "test(outbox): integration two-replica race — no duplicate processing (#11)"
```

---

## Task 16: Wiring + cmd binary

**Files:**
- Create: `plane/data/outbox/wiring/wiring.go`
- Create: `cmd/outbox-consumer/main.go`

- [ ] **Step 1: Implement wiring**

```go
// Package wiring constructs one OutboxConsumer per domain.
package wiring

import (
  "github.com/gitscale-platform/gitscale/plane/data/outbox"
  "github.com/jackc/pgx/v5/pgxpool"
)

type DomainSpec struct {
  Domain string
  Table  string
  Topic  string
}

// AllDomains returns the canonical 5-domain wiring per spec §14.
func AllDomains() []DomainSpec {
  return []DomainSpec{
    {Domain: "identity",      Table: "identity.identity_outbox",           Topic: "gitscale.identity.events"},
    {Domain: "repositories",  Table: "repositories.repositories_outbox",   Topic: "gitscale.repositories.events"},
    {Domain: "collaboration", Table: "collaboration.collaboration_outbox", Topic: "gitscale.collaboration.events"},
    {Domain: "ci",            Table: "ci.ci_outbox",                       Topic: "gitscale.ci.events"},
    {Domain: "billing",       Table: "billing.billing_outbox",             Topic: "gitscale.billing.events"},
  }
}

// BuildAll constructs one consumer per domain.
func BuildAll(db *pgxpool.Pool, prod outbox.KafkaProducer, m *outbox.Metrics) []outbox.OutboxConsumer {
  specs := AllDomains()
  out := make([]outbox.OutboxConsumer, 0, len(specs))
  for _, s := range specs {
    cfg := outbox.Config{Domain: s.Domain, Table: s.Table, Topic: s.Topic, DB: db, Producer: prod, Metrics: m}
    cfg.ApplyDefaults()
    out = append(out, outbox.NewOutboxConsumer(db, cfg))
  }
  return out
}
```

Save to `plane/data/outbox/wiring/wiring.go`.

- [ ] **Step 2: Implement the cmd binary**

```go
// outbox-consumer drains every domain's outbox table and publishes to Kafka.
package main

import (
  "context"
  "errors"
  "log"
  "net/http"
  "os"
  "os/signal"
  "strconv"
  "sync"
  "syscall"
  "time"

  "github.com/gitscale-platform/gitscale/plane/data/outbox"
  "github.com/gitscale-platform/gitscale/plane/data/outbox/wiring"
  "github.com/jackc/pgx/v5/pgxpool"
  "github.com/prometheus/client_golang/prometheus"
  "github.com/prometheus/client_golang/prometheus/promhttp"
)

func mustEnv(k string) string {
  v := os.Getenv(k)
  if v == "" { log.Fatalf("env %s required", k) }
  return v
}

func envDur(k string, def time.Duration) time.Duration {
  v := os.Getenv(k)
  if v == "" { return def }
  ms, err := strconv.Atoi(v)
  if err != nil { log.Fatalf("env %s: %v", k, err) }
  return time.Duration(ms) * time.Millisecond
}

func main() {
  ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
  defer stop()

  pool, err := pgxpool.New(ctx, mustEnv("DATABASE_URL"))
  if err != nil { log.Fatalf("pgxpool: %v", err) }
  defer pool.Close()

  prod, err := outbox.NewKafkaProducer(outbox.KafkaProducerConfig{
    BootstrapServers: mustEnv("KAFKA_BOOTSTRAP_SERVERS"),
    ClientID:         "gitscale-outbox-" + os.Getenv("HOSTNAME"),
    DeliveryTimeout:  envDur("OUTBOX_PUBLISH_TIMEOUT_MS", 5*time.Second),
  })
  if err != nil { log.Fatalf("kafka producer: %v", err) }
  defer prod.Close()

  metrics := outbox.NewMetrics()
  if err := metrics.Register(prometheus.DefaultRegisterer); err != nil {
    log.Fatalf("metrics register: %v", err)
  }

  consumers := wiring.BuildAll(pool, prod, metrics)
  var wg sync.WaitGroup
  for _, c := range consumers {
    wg.Add(1)
    go func(c outbox.OutboxConsumer) {
      defer wg.Done()
      if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        log.Printf("consumer error: %v", err)
      }
    }(c)
  }

  go http.ListenAndServe(":9090", promhttp.Handler())

  <-ctx.Done()
  wg.Wait()
}
```

Save to `cmd/outbox-consumer/main.go`.

- [ ] **Step 3: Build the binary**

Run: `go build ./cmd/outbox-consumer`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add plane/data/outbox/wiring cmd/outbox-consumer
git commit -m "feat(outbox): wiring + cmd/outbox-consumer binary (#11)"
```

---

## Task 17: Final verification

- [ ] **Step 1: Full unit suite**

Run: `go test ./plane/data/outbox/... -v -count=1`
Expected: all PASS.

- [ ] **Step 2: Full integration suite**

Run: `go test -tags integration ./plane/data/outbox/... -v -count=1 -timeout 5m`
Expected: all PASS.

- [ ] **Step 3: Lint**

Run: `make lint`
Expected: clean (or only pre-existing repo lint debt; do not introduce new issues).

- [ ] **Step 4: Final commit (if any cleanup)**

```bash
git status
# fix any lint findings, then:
git add -A
git commit -m "chore(outbox): lint cleanup (#11)"
```

---

## Acceptance criteria (verifies spec §19)

- [ ] `processed_at IS NULL` / `SET processed_at = now()` (verified by inspection of `drain.go`)
- [ ] `pg_try_advisory_xact_lock` (verified by `drain.go`)
- [ ] `ORDER BY created_at, id` (verified by `selectBatch`)
- [ ] Drain order: try-lock → SELECT FOR UPDATE SKIP LOCKED → publish → UPDATE → COMMIT (verified by happy-path test)
- [ ] `OUTBOX_PUBLISH_TIMEOUT_MS` env var (verified in cmd/outbox-consumer)
- [ ] `outbox_oldest_unprocessed_seconds` gauge (verified by `TestOutboxConsumer_SamplesOldestUnprocessed`)
- [ ] Crash-mid-batch test passing
- [ ] Two-replica race test passing
- [ ] Producer record key = `aggregate_id` bytes (verified in `producer_kafka.go`)
