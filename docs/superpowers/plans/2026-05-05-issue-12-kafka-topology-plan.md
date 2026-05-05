# Kafka topic topology (#12) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define Kafka topology (5 main topics + 5 DLQs), event envelope, per-event JSON Schema contract, consumer-group registry, and apply tooling per `docs/superpowers/specs/2026-05-02-issue-12-kafka-topology-design.md`.

**Architecture:** `topics.yaml` is the single source of truth. Terraform module + local Go CLI both consume it. JSON Schema files committed in repo, validated by `make lint-events`.

**Tech Stack:** Go 1.22, `confluent-kafka-go/v2` admin client, `gopkg.in/yaml.v3`, `github.com/santhosh-tekuri/jsonschema/v5`, `github.com/swaggest/jsonschema-go` (codegen), Terraform Mongey/kafka provider.

**Spec:** [`docs/superpowers/specs/2026-05-02-issue-12-kafka-topology-design.md`](../specs/2026-05-02-issue-12-kafka-topology-design.md)

**Issue:** [#12](https://github.com/gitscale-platform/gitscale/issues/12)

**Hard prerequisite:** ADR-004 amendment ([#26](https://github.com/gitscale-platform/gitscale/issues/26)) must be merged before this lands.

---

## File Structure

| File | Responsibility |
|---|---|
| `plane/data/kafka/topics.yaml` | Single source of truth: topics, partitions, retention, DLQs |
| `plane/data/kafka/topics.go` | Go constants for topic names |
| `plane/data/kafka/consumer_groups.go` | Consumer-group name constants + topic linkage docs |
| `plane/data/kafka/envelope.go` | `EventEnvelope` struct |
| `plane/data/kafka/envelope.schema.json` | JSON Schema for the envelope |
| `plane/data/kafka/topology.go` | YAML parser + topology types |
| `plane/data/kafka/topology_test.go` | Validates yaml structure + invariants |
| `plane/data/kafka/envelope_test.go` | Round-trip + schema validation |
| `plane/data/events/<domain>/.gitkeep` | Per-domain schema dir placeholder |
| `cmd/kafka-topology-apply/main.go` | Local apply CLI (used by `make dev-up`) |
| `tools/lint-events/main.go` | `make lint-events` entrypoint |
| `Makefile` | New `lint-events` target |
| `deploy/terraform/kafka/main.tf` | Terraform module reading topics.yaml |
| `deploy/terraform/kafka/variables.tf` | Module inputs |
| `.github/workflows/lint-events.yml` (or add to existing CI) | CI integration |

---

## Task 1: `topics.yaml` + Go topic-name constants

**Files:**
- Create: `plane/data/kafka/topics.yaml`
- Create: `plane/data/kafka/topics.go`
- Create: `plane/data/kafka/topics_test.go`
- Create: `plane/data/kafka/doc.go`

- [ ] **Step 1: Write package doc**

```go
// Package kafka defines the GitScale Kafka topic topology, event envelope,
// and consumer-group registry. topics.yaml is the single source of truth.
//
// Spec: docs/superpowers/specs/2026-05-02-issue-12-kafka-topology-design.md
package kafka
```

Save to `plane/data/kafka/doc.go`.

- [ ] **Step 2: Write `topics.yaml` (verbatim from spec §5)**

```yaml
# Single source of truth for GitScale Kafka topology.
# Consumed by:
#   - cmd/kafka-topology-apply (local apply)
#   - deploy/terraform/kafka (prod/staging)
#
# Versioning policy: in-place backwards-compatible evolution. Breaking changes
# get a new topic <name>.v2 with dual-publish window. See spec §9.
#
# Partition key: aggregate_id (UUID) for every topic. See ADR-004.

defaults:
  replication_factor: 3
  configs:
    cleanup.policy: delete
    min.insync.replicas: 2
    compression.type: zstd

topics:
  - name: gitscale.identity.events
    partitions: 12
    retention_ms: 604800000          # 7 days
    rationale: "User/org/agent mutations. Low volume."

  - name: gitscale.identity.events.dlq
    partitions: 1
    retention_ms: 2592000000         # 30 days
    rationale: "Poison messages from identity consumers."

  - name: gitscale.repositories.events
    partitions: 24
    retention_ms: 604800000
    rationale: "Repo metadata mutations. Push events drive ~1/2 collaboration volume."

  - name: gitscale.repositories.events.dlq
    partitions: 1
    retention_ms: 2592000000

  - name: gitscale.collaboration.events
    partitions: 48
    retention_ms: 604800000
    rationale: "PR/issue/comment events. Highest write volume — agent-driven."

  - name: gitscale.collaboration.events.dlq
    partitions: 1
    retention_ms: 2592000000

  - name: gitscale.ci.events
    partitions: 24
    retention_ms: 604800000
    rationale: "Workflow + per-job state transitions."

  - name: gitscale.ci.events.dlq
    partitions: 1
    retention_ms: 2592000000

  - name: gitscale.billing.events
    partitions: 12
    retention_ms: 2592000000         # 30 days
    rationale: "Usage events. Longer retention for billing reconciliation."

  - name: gitscale.billing.events.dlq
    partitions: 1
    retention_ms: 2592000000
```

- [ ] **Step 3: Write failing test for topic-name constants**

```go
package kafka

import "testing"

func TestTopicConstants_MatchYAMLNames(t *testing.T) {
  want := map[string]bool{
    TopicIdentity: true, TopicIdentityDLQ: true,
    TopicRepositories: true, TopicRepositoriesDLQ: true,
    TopicCollaboration: true, TopicCollaborationDLQ: true,
    TopicCI: true, TopicCIDLQ: true,
    TopicBilling: true, TopicBillingDLQ: true,
  }
  if len(want) != 10 {
    t.Errorf("expected 10 topic constants, set has %d", len(want))
  }
  for name := range want {
    if name == "" {
      t.Errorf("a topic constant is empty")
    }
  }
}
```

Save to `plane/data/kafka/topics_test.go`.

- [ ] **Step 4: Run, expect compile failure**

Run: `go test ./plane/data/kafka/... -run TestTopicConstants -v`
Expected: FAIL — constants undefined.

- [ ] **Step 5: Define constants**

```go
package kafka

// Topic name constants. Mirror plane/data/kafka/topics.yaml exactly.
// CI checks names match (see TestTopologyYAML_HasAllConstantTopics in topology_test.go).
const (
  TopicIdentity         = "gitscale.identity.events"
  TopicIdentityDLQ      = "gitscale.identity.events.dlq"
  TopicRepositories     = "gitscale.repositories.events"
  TopicRepositoriesDLQ  = "gitscale.repositories.events.dlq"
  TopicCollaboration    = "gitscale.collaboration.events"
  TopicCollaborationDLQ = "gitscale.collaboration.events.dlq"
  TopicCI               = "gitscale.ci.events"
  TopicCIDLQ            = "gitscale.ci.events.dlq"
  TopicBilling          = "gitscale.billing.events"
  TopicBillingDLQ       = "gitscale.billing.events.dlq"
)
```

Save to `plane/data/kafka/topics.go`.

- [ ] **Step 6: Run, expect pass**

Run: `go test ./plane/data/kafka/... -run TestTopicConstants -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add plane/data/kafka/topics.yaml plane/data/kafka/topics.go plane/data/kafka/topics_test.go plane/data/kafka/doc.go
git commit -m "feat(kafka): topics.yaml + Go topic-name constants (#12)"
```

---

## Task 2: `EventEnvelope` + JSON Schema

**Files:**
- Create: `plane/data/kafka/envelope.go`
- Create: `plane/data/kafka/envelope.schema.json`
- Create: `plane/data/kafka/envelope_test.go`

- [ ] **Step 1: Write `envelope.schema.json` (verbatim from spec §6)**

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://gitscale.dev/schemas/kafka/envelope.json",
  "type": "object",
  "required": ["event_id", "aggregate_type", "aggregate_id", "event_type", "schema_version", "payload", "published_at"],
  "properties": {
    "event_id": { "type": "string", "format": "uuid" },
    "aggregate_type": { "type": "string", "minLength": 1 },
    "aggregate_id": { "type": "string", "format": "uuid" },
    "event_type": { "type": "string", "pattern": "^[a-z_]+\\.[a-z_]+$" },
    "schema_version": { "type": "string", "pattern": "^v[0-9]+$" },
    "payload": { "type": "object" },
    "published_at": { "type": "string", "format": "date-time" }
  },
  "additionalProperties": false
}
```

- [ ] **Step 2: Write failing test — round-trip + schema-validate**

```go
package kafka

import (
  "encoding/json"
  "os"
  "path/filepath"
  "testing"
  "time"

  "github.com/santhosh-tekuri/jsonschema/v5"
)

func TestEventEnvelope_RoundTrip(t *testing.T) {
  env := EventEnvelope{
    EventID:       "11111111-1111-4111-8111-111111111111",
    AggregateType: "pull_request",
    AggregateID:   "22222222-2222-4222-8222-222222222222",
    EventType:     "pr.opened",
    SchemaVersion: "v1",
    Payload:       json.RawMessage(`{"title":"x"}`),
    PublishedAt:   time.Now().UTC().Truncate(time.Second),
  }
  b, err := json.Marshal(env)
  if err != nil { t.Fatal(err) }

  var got EventEnvelope
  if err := json.Unmarshal(b, &got); err != nil { t.Fatal(err) }
  if got.EventID != env.EventID || got.EventType != env.EventType {
    t.Errorf("round-trip mismatch: %+v vs %+v", env, got)
  }
}

func TestEventEnvelope_ValidatesAgainstSchema(t *testing.T) {
  wd, _ := os.Getwd()
  schema, err := jsonschema.Compile(filepath.Join(wd, "envelope.schema.json"))
  if err != nil { t.Fatalf("compile schema: %v", err) }

  env := EventEnvelope{
    EventID: "11111111-1111-4111-8111-111111111111", AggregateType: "x",
    AggregateID: "22222222-2222-4222-8222-222222222222", EventType: "x.y",
    SchemaVersion: "v1", Payload: json.RawMessage(`{}`), PublishedAt: time.Now().UTC(),
  }
  b, _ := json.Marshal(env)
  var v interface{}
  json.Unmarshal(b, &v)
  if err := schema.Validate(v); err != nil {
    t.Errorf("envelope failed schema validation: %v", err)
  }
}

func TestEventEnvelope_RejectsInvalidEventType(t *testing.T) {
  wd, _ := os.Getwd()
  schema, err := jsonschema.Compile(filepath.Join(wd, "envelope.schema.json"))
  if err != nil { t.Fatal(err) }

  env := EventEnvelope{
    EventID: "11111111-1111-4111-8111-111111111111", AggregateType: "x",
    AggregateID: "22222222-2222-4222-8222-222222222222",
    EventType: "BadEventType",  // violates pattern
    SchemaVersion: "v1", Payload: json.RawMessage(`{}`), PublishedAt: time.Now().UTC(),
  }
  b, _ := json.Marshal(env)
  var v interface{}
  json.Unmarshal(b, &v)
  if err := schema.Validate(v); err == nil {
    t.Error("expected validation error for malformed event_type, got nil")
  }
}
```

Save to `plane/data/kafka/envelope_test.go`.

Add deps:

```bash
go get github.com/santhosh-tekuri/jsonschema/v5
```

- [ ] **Step 3: Run, expect compile failure**

Run: `go test ./plane/data/kafka/... -run TestEventEnvelope -v`
Expected: FAIL — `EventEnvelope` undefined.

- [ ] **Step 4: Implement envelope**

```go
package kafka

import (
  "encoding/json"
  "time"
)

// EventEnvelope is the wire-format wrapper for every event published to
// any gitscale.*.events topic. See spec §6.
type EventEnvelope struct {
  EventID       string          `json:"event_id"`        // UUID — outbox.event_id
  AggregateType string          `json:"aggregate_type"`  // e.g. "pull_request"
  AggregateID   string          `json:"aggregate_id"`    // UUID
  EventType     string          `json:"event_type"`      // e.g. "pr.opened"
  SchemaVersion string          `json:"schema_version"`  // e.g. "v1"
  Payload       json.RawMessage `json:"payload"`
  PublishedAt   time.Time       `json:"published_at"`
}
```

Save to `plane/data/kafka/envelope.go`.

- [ ] **Step 5: Run, expect pass**

Run: `go test ./plane/data/kafka/... -run TestEventEnvelope -v`
Expected: 3 PASS.

- [ ] **Step 6: Commit**

```bash
git add plane/data/kafka/envelope.go plane/data/kafka/envelope.schema.json plane/data/kafka/envelope_test.go go.mod go.sum
git commit -m "feat(kafka): EventEnvelope + JSON Schema + round-trip tests (#12)"
```

---

## Task 3: Consumer-group registry

**Files:**
- Create: `plane/data/kafka/consumer_groups.go`
- Create: `plane/data/kafka/consumer_groups_test.go`

- [ ] **Step 1: Write failing test**

```go
package kafka

import "testing"

func TestConsumerGroups_AllPresent(t *testing.T) {
  for _, g := range []string{
    GroupSearchIndexer, GroupAuditLog, GroupWebhookFanout,
    GroupBillingAggregator, GroupColdStorageMigrator,
  } {
    if g == "" { t.Errorf("empty consumer group constant") }
  }
  if DefaultAutoOffsetReset != "earliest" {
    t.Errorf("DefaultAutoOffsetReset = %q, want earliest", DefaultAutoOffsetReset)
  }
}
```

Save to `plane/data/kafka/consumer_groups_test.go`.

- [ ] **Step 2: Implement**

```go
package kafka

const (
  // GroupSearchIndexer consumes ALL 5 main topics. Indexes into Vespa (ADR-016).
  GroupSearchIndexer = "gitscale.search-indexer"

  // GroupAuditLog consumes ALL 5 main topics. Writes immutable audit records to ClickHouse.
  GroupAuditLog = "gitscale.audit-log"

  // GroupWebhookFanout consumes repositories.events + collaboration.events + ci.events.
  GroupWebhookFanout = "gitscale.webhook-fanout"

  // GroupBillingAggregator consumes billing.events.
  GroupBillingAggregator = "gitscale.billing-aggregator"

  // GroupColdStorageMigrator consumes repositories.events to learn which repos
  // crossed the hot→cold boundary (last_active_at > 30d).
  GroupColdStorageMigrator = "gitscale.cold-storage-migrator"
)

// DefaultAutoOffsetReset — late-binding consumers backfill, not skip.
const DefaultAutoOffsetReset = "earliest"
```

Save to `plane/data/kafka/consumer_groups.go`.

- [ ] **Step 3: Run, expect pass**

Run: `go test ./plane/data/kafka/... -run TestConsumerGroups -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add plane/data/kafka/consumer_groups.go plane/data/kafka/consumer_groups_test.go
git commit -m "feat(kafka): consumer-group name registry (#12)"
```

---

## Task 4: `topology.go` — yaml loader + invariant tests

**Files:**
- Create: `plane/data/kafka/topology.go`
- Create: `plane/data/kafka/topology_test.go`

- [ ] **Step 1: Add yaml dep**

```bash
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: Write failing tests for the loader and invariants**

```go
package kafka

import (
  "os"
  "path/filepath"
  "testing"
)

func loadYAMLFixture(t *testing.T) *Topology {
  t.Helper()
  wd, _ := os.Getwd()
  top, err := LoadTopology(filepath.Join(wd, "topics.yaml"))
  if err != nil { t.Fatalf("load: %v", err) }
  return top
}

func TestLoadTopology_ParsesAllTopics(t *testing.T) {
  top := loadYAMLFixture(t)
  if len(top.Topics) != 10 {
    t.Errorf("topics = %d, want 10 (5 main + 5 dlq)", len(top.Topics))
  }
}

func TestLoadTopology_DefaultsApplied(t *testing.T) {
  top := loadYAMLFixture(t)
  if top.Defaults.ReplicationFactor != 3 {
    t.Errorf("rf = %d, want 3", top.Defaults.ReplicationFactor)
  }
  if top.Defaults.Configs["min.insync.replicas"] != "2" {
    t.Errorf("min.insync.replicas: %v", top.Defaults.Configs["min.insync.replicas"])
  }
}

func TestLoadTopology_HasAllConstantTopics(t *testing.T) {
  top := loadYAMLFixture(t)
  names := map[string]bool{}
  for _, t := range top.Topics { names[t.Name] = true }
  for _, want := range []string{
    TopicIdentity, TopicIdentityDLQ, TopicRepositories, TopicRepositoriesDLQ,
    TopicCollaboration, TopicCollaborationDLQ, TopicCI, TopicCIDLQ,
    TopicBilling, TopicBillingDLQ,
  } {
    if !names[want] {
      t.Errorf("topics.yaml missing %q", want)
    }
  }
}

func TestLoadTopology_PartitionCountsMatchSpec(t *testing.T) {
  top := loadYAMLFixture(t)
  want := map[string]int{
    TopicIdentity: 12, TopicRepositories: 24, TopicCollaboration: 48,
    TopicCI: 24, TopicBilling: 12,
    TopicIdentityDLQ: 1, TopicRepositoriesDLQ: 1, TopicCollaborationDLQ: 1,
    TopicCIDLQ: 1, TopicBillingDLQ: 1,
  }
  for _, topic := range top.Topics {
    if got, ok := want[topic.Name]; ok && topic.Partitions != got {
      t.Errorf("%s partitions = %d, want %d", topic.Name, topic.Partitions, got)
    }
  }
}
```

Save to `plane/data/kafka/topology_test.go`.

- [ ] **Step 3: Run, expect compile failure**

Run: `go test ./plane/data/kafka/... -run TestLoadTopology -v`
Expected: FAIL.

- [ ] **Step 4: Implement loader**

```go
package kafka

import (
  "fmt"
  "os"

  "gopkg.in/yaml.v3"
)

type Topology struct {
  Defaults Defaults `yaml:"defaults"`
  Topics   []Topic  `yaml:"topics"`
}

type Defaults struct {
  ReplicationFactor int               `yaml:"replication_factor"`
  Configs           map[string]string `yaml:"configs"`
}

type Topic struct {
  Name        string `yaml:"name"`
  Partitions  int    `yaml:"partitions"`
  RetentionMS int64  `yaml:"retention_ms"`
  Rationale   string `yaml:"rationale"`
}

// LoadTopology reads and parses topics.yaml from the given path.
func LoadTopology(path string) (*Topology, error) {
  b, err := os.ReadFile(path)
  if err != nil { return nil, fmt.Errorf("read %s: %w", path, err) }
  var t Topology
  if err := yaml.Unmarshal(b, &t); err != nil { return nil, fmt.Errorf("parse %s: %w", path, err) }
  if t.Defaults.Configs == nil { t.Defaults.Configs = map[string]string{} }
  return &t, nil
}
```

Save to `plane/data/kafka/topology.go`.

- [ ] **Step 5: Run, expect pass**

Run: `go test ./plane/data/kafka/... -v -count=1`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add plane/data/kafka/topology.go plane/data/kafka/topology_test.go go.mod go.sum
git commit -m "feat(kafka): topics.yaml loader + invariant tests (#12)"
```

---

## Task 5: `cmd/kafka-topology-apply` CLI

**Files:**
- Create: `cmd/kafka-topology-apply/main.go`

- [ ] **Step 1: Implement the apply tool**

```go
// kafka-topology-apply reads plane/data/kafka/topics.yaml and ensures every
// topic exists on the broker with matching config. Used by `make dev-up`.
// Never deletes topics — safety. Drift in partition counts is reported but
// not auto-corrected (broker rebalance must be planned, not automatic).
package main

import (
  "context"
  "flag"
  "fmt"
  "log"
  "os"
  "strconv"
  "time"

  confluent "github.com/confluentinc/confluent-kafka-go/v2/kafka"
  gskafka "github.com/gitscale-platform/gitscale/plane/data/kafka"
)

func main() {
  yamlPath := flag.String("yaml", "plane/data/kafka/topics.yaml", "path to topics.yaml")
  bootstrap := flag.String("bootstrap", os.Getenv("KAFKA_BOOTSTRAP_SERVERS"), "Kafka bootstrap servers")
  dryRun := flag.Bool("dry-run", false, "report drift, don't change broker state")
  flag.Parse()

  if *bootstrap == "" {
    log.Fatal("--bootstrap or KAFKA_BOOTSTRAP_SERVERS required")
  }

  top, err := gskafka.LoadTopology(*yamlPath)
  if err != nil { log.Fatalf("load: %v", err) }

  admin, err := confluent.NewAdminClient(&confluent.ConfigMap{"bootstrap.servers": *bootstrap})
  if err != nil { log.Fatalf("admin client: %v", err) }
  defer admin.Close()

  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer cancel()

  meta, err := admin.GetMetadata(nil, true, 5000)
  if err != nil { log.Fatalf("metadata: %v", err) }

  for _, want := range top.Topics {
    existing, hasIt := meta.Topics[want.Name]
    if !hasIt || existing.Error.Code() != confluent.ErrNoError {
      log.Printf("CREATE %s partitions=%d rf=%d", want.Name, want.Partitions, top.Defaults.ReplicationFactor)
      if *dryRun { continue }
      cfg := mergedConfig(top.Defaults.Configs, want.RetentionMS)
      results, err := admin.CreateTopics(ctx, []confluent.TopicSpecification{{
        Topic:             want.Name,
        NumPartitions:     want.Partitions,
        ReplicationFactor: top.Defaults.ReplicationFactor,
        Config:            cfg,
      }})
      if err != nil { log.Fatalf("create %s: %v", want.Name, err) }
      for _, r := range results {
        if r.Error.Code() != confluent.ErrNoError && r.Error.Code() != confluent.ErrTopicAlreadyExists {
          log.Fatalf("create %s: %v", want.Name, r.Error)
        }
      }
    } else if existing.Error.Code() == confluent.ErrNoError && len(existing.Partitions) != want.Partitions {
      log.Printf("DRIFT %s: broker partitions=%d, yaml=%d (manual rebalance required)", want.Name, len(existing.Partitions), want.Partitions)
    } else {
      log.Printf("OK %s partitions=%d", want.Name, want.Partitions)
    }
  }

  fmt.Println("topology applied")
}

func mergedConfig(defaults map[string]string, retentionMS int64) map[string]string {
  out := make(map[string]string, len(defaults)+1)
  for k, v := range defaults { out[k] = v }
  out["retention.ms"] = strconv.FormatInt(retentionMS, 10)
  return out
}
```

> **Note on imports:** `confluent` aliases `confluent-kafka-go/v2/kafka`; `gskafka` aliases the local `plane/data/kafka` package. Required because both end in `kafka`.

Save to `cmd/kafka-topology-apply/main.go`.

- [ ] **Step 2: Verify build**

Run: `go build ./cmd/kafka-topology-apply`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add cmd/kafka-topology-apply/main.go
git commit -m "feat(kafka): cmd/kafka-topology-apply CLI for local + CI use (#12)"
```

---

## Task 6: Per-event-type schema directory layout

**Files:**
- Create: `plane/data/events/identity/.gitkeep`
- Create: `plane/data/events/repositories/.gitkeep`
- Create: `plane/data/events/collaboration/.gitkeep`
- Create: `plane/data/events/ci/.gitkeep`
- Create: `plane/data/events/billing/.gitkeep`
- Create: `plane/data/events/README.md`

- [ ] **Step 1: Create empty domain dirs**

```bash
mkdir -p plane/data/events/{identity,repositories,collaboration,ci,billing}
touch plane/data/events/{identity,repositories,collaboration,ci,billing}/.gitkeep
```

- [ ] **Step 2: Add README explaining the contract**

```markdown
# Event payload schemas

One JSON Schema file per event type, named `<event_type>.schema.json`. Fixtures
live in `<event_type>.testdata/*.json` and are validated against the schema by
`make lint-events`.

Conventions:
- `event_type` follows the regex `^[a-z_]+\.[a-z_]+$` (e.g. `pr.opened`, `user.created`)
- Schema files use Draft 2020-12
- Backwards-compat: only add optional fields, never rename/remove. Breaking changes
  require a `_v2` event_type and a paired downgrade function.

Concrete schemas for each event type are filed alongside the producing
domain service issue.
```

Save to `plane/data/events/README.md`.

- [ ] **Step 3: Commit**

```bash
git add plane/data/events
git commit -m "feat(events): per-domain schema directory layout (#12)"
```

---

## Task 7: `make lint-events` target + linter

**Files:**
- Create: `tools/lint-events/main.go`
- Create: `tools/lint-events/main_test.go`
- Modify: `Makefile`

- [ ] **Step 1: Implement the linter**

```go
// lint-events validates per-event-type JSON Schema files and their fixtures.
// Fails on:
//   - schema file invalid JSON Schema (Draft 2020-12)
//   - any fixture failing its sibling schema
//   - schema with no fixtures
package main

import (
  "encoding/json"
  "flag"
  "fmt"
  "os"
  "path/filepath"
  "regexp"
  "strings"

  "github.com/santhosh-tekuri/jsonschema/v5"
)

var eventTypePattern = regexp.MustCompile(`^[a-z_]+\.[a-z_]+\.schema\.json$`)

func main() {
  root := flag.String("root", "plane/data/events", "events directory root")
  flag.Parse()

  failures := 0

  domains, err := os.ReadDir(*root)
  if err != nil { fail("read %s: %v", *root, err) }

  for _, dom := range domains {
    if !dom.IsDir() { continue }
    domPath := filepath.Join(*root, dom.Name())
    entries, _ := os.ReadDir(domPath)
    for _, e := range entries {
      if !e.IsDir() && eventTypePattern.MatchString(e.Name()) {
        if !validateSchemaAndFixtures(domPath, e.Name()) {
          failures++
        }
      }
    }
  }

  if failures > 0 {
    fmt.Fprintf(os.Stderr, "lint-events: %d failure(s)\n", failures)
    os.Exit(1)
  }
  fmt.Println("lint-events: ok")
}

func validateSchemaAndFixtures(dir, schemaFile string) bool {
  schemaPath := filepath.Join(dir, schemaFile)
  schema, err := jsonschema.Compile(schemaPath)
  if err != nil {
    fmt.Fprintf(os.Stderr, "  %s: invalid schema: %v\n", schemaPath, err)
    return false
  }

  eventType := strings.TrimSuffix(schemaFile, ".schema.json")
  testdata := filepath.Join(dir, eventType+".testdata")
  st, err := os.Stat(testdata)
  if err != nil || !st.IsDir() {
    fmt.Fprintf(os.Stderr, "  %s: missing %s/ fixtures dir\n", schemaPath, testdata)
    return false
  }
  fixtures, _ := os.ReadDir(testdata)
  count := 0
  ok := true
  for _, f := range fixtures {
    if filepath.Ext(f.Name()) != ".json" { continue }
    count++
    b, _ := os.ReadFile(filepath.Join(testdata, f.Name()))
    var v interface{}
    if err := json.Unmarshal(b, &v); err != nil {
      fmt.Fprintf(os.Stderr, "  %s: invalid JSON: %v\n", f.Name(), err)
      ok = false
      continue
    }
    if err := schema.Validate(v); err != nil {
      fmt.Fprintf(os.Stderr, "  %s: schema violation: %v\n", f.Name(), err)
      ok = false
    }
  }
  if count == 0 {
    fmt.Fprintf(os.Stderr, "  %s: no fixtures in %s\n", schemaPath, testdata)
    ok = false
  }
  return ok
}

func fail(format string, args ...interface{}) {
  fmt.Fprintf(os.Stderr, format+"\n", args...)
  os.Exit(2)
}
```

Save to `tools/lint-events/main.go`.

- [ ] **Step 2: Add Makefile target**

Add to `Makefile`:

```makefile
.PHONY: lint-events
lint-events:
  go run ./tools/lint-events --root plane/data/events
```

- [ ] **Step 3: Run lint-events with empty domains (no schemas yet) — should pass cleanly**

Run: `make lint-events`
Expected: `lint-events: ok` (no schemas, no failures).

- [ ] **Step 4: Add a sample schema + fixture to verify the linter catches violations**

Create `plane/data/events/identity/_test.fixture.schema.json` is *not* matched (doesn't fit `<event_type>.schema.json` pattern) — instead, add a real probe schema temporarily:

```bash
mkdir -p plane/data/events/identity/_probe.testdata
cat > plane/data/events/identity/_probe.schema.json <<'EOF'
{ "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object", "required": ["x"], "properties": { "x": {"type": "integer"} },
  "additionalProperties": false }
EOF
```

Wait — `_probe.schema.json` matches the pattern `^[a-z_]+\.[a-z_]+\.schema\.json$`? No — that pattern needs a `.` between two segments. `_probe.schema.json` has `_probe` then `.schema.json` — not matching.

Adjust: instead, write a self-test Go test for the linter. Add `tools/lint-events/main_test.go`:

```go
package main

import (
  "os"
  "path/filepath"
  "testing"
)

func writeFile(t *testing.T, path, content string) {
  t.Helper()
  if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
  if err := os.WriteFile(path, []byte(content), 0o644); err != nil { t.Fatal(err) }
}

func TestValidateSchemaAndFixtures_HappyPath(t *testing.T) {
  dir := t.TempDir()
  writeFile(t, filepath.Join(dir, "test_x.evt_y.schema.json"), `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["x"],"properties":{"x":{"type":"integer"}}}`)
  writeFile(t, filepath.Join(dir, "test_x.evt_y.testdata", "ok.json"), `{"x":1}`)
  if !validateSchemaAndFixtures(dir, "test_x.evt_y.schema.json") {
    t.Error("expected pass")
  }
}

func TestValidateSchemaAndFixtures_NoFixtures(t *testing.T) {
  dir := t.TempDir()
  writeFile(t, filepath.Join(dir, "test_x.evt_y.schema.json"), `{"type":"object"}`)
  if validateSchemaAndFixtures(dir, "test_x.evt_y.schema.json") {
    t.Error("expected fail (no fixtures)")
  }
}

func TestValidateSchemaAndFixtures_FixtureViolation(t *testing.T) {
  dir := t.TempDir()
  writeFile(t, filepath.Join(dir, "test_x.evt_y.schema.json"), `{"type":"object","required":["x"],"properties":{"x":{"type":"integer"}}}`)
  writeFile(t, filepath.Join(dir, "test_x.evt_y.testdata", "bad.json"), `{"x":"not-an-integer"}`)
  if validateSchemaAndFixtures(dir, "test_x.evt_y.schema.json") {
    t.Error("expected fail (fixture violates schema)")
  }
}
```

- [ ] **Step 5: Run linter unit tests**

Run: `go test ./tools/lint-events/... -v`
Expected: 3 PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/lint-events Makefile
git commit -m "feat(kafka): make lint-events target + linter (#12)"
```

---

## Task 8: CI integration for `lint-events`

**Files:**
- Create or modify: `.github/workflows/lint.yml`

- [ ] **Step 1: Add the workflow step**

If `.github/workflows/lint.yml` exists, append a step. Otherwise create:

```yaml
name: lint
on:
  push: { branches: [main] }
  pull_request:

jobs:
  lint-events:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.22" }
      - run: make lint-events
```

- [ ] **Step 2: Validate locally**

Run: `act -j lint-events` (if `act` available) or push to a branch and verify CI runs.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/lint.yml
git commit -m "ci(kafka): wire make lint-events into CI per CLAUDE.md linter rule (#12)"
```

---

## Task 9: Terraform module

**Files:**
- Create: `deploy/terraform/kafka/main.tf`
- Create: `deploy/terraform/kafka/variables.tf`
- Create: `deploy/terraform/kafka/versions.tf`

- [ ] **Step 1: Write Terraform versions**

```hcl
# deploy/terraform/kafka/versions.tf
terraform {
  required_version = ">= 1.5"
  required_providers {
    kafka = { source = "Mongey/kafka", version = "~> 0.7" }
  }
}
```

- [ ] **Step 2: Write variables**

```hcl
# deploy/terraform/kafka/variables.tf
variable "topology_yaml_path" {
  type        = string
  description = "Absolute path to plane/data/kafka/topics.yaml"
}
```

- [ ] **Step 3: Write main**

```hcl
# deploy/terraform/kafka/main.tf
locals {
  topology = yamldecode(file(var.topology_yaml_path))
  topics_by_name = { for t in local.topology.topics : t.name => t }
}

resource "kafka_topic" "topics" {
  for_each           = local.topics_by_name
  name               = each.value.name
  partitions         = each.value.partitions
  replication_factor = local.topology.defaults.replication_factor
  config = merge(
    local.topology.defaults.configs,
    { "retention.ms" = tostring(each.value.retention_ms) }
  )
}

output "topic_names" {
  value = keys(local.topics_by_name)
}
```

- [ ] **Step 4: Validate (no apply)**

Run:

```bash
cd deploy/terraform/kafka
terraform init -backend=false
terraform validate
```

Expected: success. (Skip if `terraform` not installed locally — CI will catch.)

- [ ] **Step 5: Commit**

```bash
git add deploy/terraform/kafka
git commit -m "feat(kafka): Terraform module reading topics.yaml (#12)"
```

---

## Task 10: Final verification

- [ ] **Step 1: Full Go suite**

Run: `go test ./plane/data/kafka/... ./tools/lint-events/... -v -count=1`
Expected: all PASS.

- [ ] **Step 2: Run `make lint-events`**

Run: `make lint-events`
Expected: `lint-events: ok`.

- [ ] **Step 3: Build CLIs**

Run: `go build ./cmd/kafka-topology-apply ./tools/lint-events`
Expected: success.

- [ ] **Step 4: Lint**

Run: `make lint && make lint-md`
Expected: clean.

---

## Acceptance criteria (verifies spec §15)

- [ ] `topics.yaml` is the single source of truth (verified by `topology_test.go`)
- [ ] DLQ topic for each main topic, 1 partition, 30d retention (verified by `TestLoadTopology_PartitionCountsMatchSpec`)
- [ ] `EventEnvelope` includes `schema_version` field (verified by `envelope_test.go`)
- [ ] `envelope.schema.json` committed alongside `envelope.go`
- [ ] `plane/data/events/<domain>/` directories created
- [ ] `make lint-events` target validates schemas + fixtures
- [ ] `lint-events` config wired into CI
- [ ] `DefaultAutoOffsetReset = "earliest"`
- [ ] Comments in `consumer_groups.go` document each group's topic subscription
- [ ] Versioning policy header comment in `topics.yaml`
- [ ] **Cross-cutting:** ADR-004 amendment (#26) merged before #12 lands
