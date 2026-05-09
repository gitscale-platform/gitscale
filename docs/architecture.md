# GitScale Architecture

> Living document. Section 8 (ADRs) is the source of truth for binding decisions. Sections 1–7 describe the system at the level of binding shape; component-level implementation lives in [`design.md`](design.md).

## 1. Goals & non-goals

### 1.1 Architecture principles

| Principle | Implication |
|---|---|
| Agents are first-class principals | Identity, rate limiting, and quota systems model agents natively — not as users with tokens |
| Independent scalability per plane | Storage, metadata, compute, and workflow planes scale horizontally without coupling |
| Event-driven at every seam | Services communicate via Kafka events; synchronous RPC only where latency is critical |
| Metering is infrastructure | Token and compute consumption is measured at the gateway layer, not inferred from billing logs |
| No shared fate | A failure in any one service must not cascade; circuit breakers and bulkheads at every boundary |
| Erasure-code cold, replicate hot | Storage strategy is determined by access pattern, not by convenience |

### 1.2 Non-goals

- Bug-for-bug parity with any incumbent host. Ecosystem-compatible REST and GraphQL surfaces (§5 in [`design.md`](design.md)) target schema-shape compatibility for a named subset, not full parity.
- A unified storage tier. Hot and cold tiers have different durability, replication, and access primitives by design (§2.2).
- A unified rate-limit class. Human and agent traffic share the platform but never share a quota bucket.

---

## 2. System architecture

### 2.1 Full system diagram

```
                         ┌──────────────────────────────────────────────┐
                         │                CLIENTS                        │
                         │  Git CLI · Web Browser · AI Agents (MCP)     │
                         └─────────────────┬────────────────────────────┘
                                           │ HTTPS / SSH / MCP
                         ┌─────────────────▼────────────────────────────┐
                         │              EDGE PLANE                       │
                         │                                               │
                         │  ┌────────────┐  ┌─────────────────────────┐ │
                         │  │   Envoy    │  │   Identity Resolver      │ │
                         │  │  Gateway   │  │   (Human | Agent | CI)   │ │
                         │  └─────┬──────┘  └───────────┬─────────────┘ │
                         │        │                      │               │
                         │  ┌─────▼──────────────────────▼────────────┐ │
                         │  │  Rate Limiter + Token Meter              │ │
                         │  │  (Redis token buckets, 5s flush)         │ │
                         │  └─────────────────────┬────────────────────┘ │
                         └────────────────────────┼─────────────────────┘
                                    ┌─────────────┼─────────────┐
                                    │             │             │
                         ┌──────────▼──┐  ┌───────▼──────┐  ┌──▼───────────────┐
                         │  GIT PLANE  │  │  APP PLANE   │  │  WORKFLOW PLANE   │
                         │             │  │              │  │                   │
                         │ Git Proxy   │  │ Modular      │  │ Temporal          │
                         │ (Go)        │  │ Monolith     │  │ Orchestration     │
                         │             │  │ (Go)         │  │                   │
                         │ 3x replica  │  │ ┌──────────┐ │  │ CI Runner Mgr     │
                         │ NVMe hot    │  │ │ identity │ │  │ (Firecracker)     │
                         │             │  │ │ repos    │ │  │                   │
                         │ Gitaly RPC  │  │ │ collab   │ │  │ PR Score          │
                         │ layer       │  │ │ ci       │ │  │ Pipeline          │
                         │             │  │ │ billing  │ │  │                   │
                         │ NVMe file   │  │ └──────────┘ │  │ Agent Session     │
                         │ servers     │  │              │  │ Manager           │
                         └──────┬──────┘  └──────┬───────┘  └────────┬──────────┘
                                │                │                   │
                                │                └─────────┬─────────┘
                                │                          │
                         ┌──────▼──────────────────────────▼──────────────────┐
                         │                    EVENT BUS                         │
                         │       Kafka (fed by polling outbox consumer)         │
                         │   push.created · pr.opened · ci.triggered · ...     │
                         └──────┬──────────────────────┬───────────────────────┘
                                │                      │
                    ┌───────────┴──────┐    ┌──────────┴───────────┐
                    │   DATA PLANE     │    │  ASYNC CONSUMERS      │
                    │                  │    │                        │
                    │ PostgreSQL       │    │ Search Indexer (Vespa) │
                    │ (metadata, SQL)  │    │ Audit Log (ClickHouse) │
                    │                  │    │ Webhook Fanout         │
                    │ Redis            │    │ Billing Aggregator     │
                    │ (cache, rate     │    │ Cold Storage Migrator  │
                    │  limit state)    │    └────────────────────────┘
                    │                  │
                    │ Qdrant           │
                    │ (PR dedup only)  │
                    └──────────────────┘
```

### 2.2 Storage architecture

```
                    WRITE PATH (git push)
                           │
                    ┌──────▼──────┐
                    │  Git Proxy  │  ← resolves repo location from PostgreSQL
                    └──┬───┬───┬──┘    (cached in Redis; ADR-009)
                       │   │   │     3-phase commit (quorum = 2 of 3)
              ┌────────┘   │   └────────┐
              ▼            ▼            ▼
         ┌────────┐   ┌────────┐   ┌────────┐
         │File Srv│   │File Srv│   │File Srv│   ← NVMe, local Git on disk
         │  AZ-1  │   │  AZ-2  │   │  Region│
         └────────┘   └────────┘   └────────┘
              │
              │  Background GC (daily)
              ▼
    ┌─────────────────────┐
    │  Pack Compaction    │  ← objects older than 30d
    └──────────┬──────────┘
               │
    ┌──────────▼──────────┐
    │  Cold Object Store  │  ← (10,4) Reed-Solomon erasure coding
    │  (S3-compatible)    │     10 data + 4 parity shards
    └─────────────────────┘     tolerates 4 simultaneous shard failures
                                ~40% storage overhead vs ~200% for 3x

    READ PATH
    ─────────
    git fetch/clone
      → Git Proxy
      → Bloom filter check: is object in hot tier?
        YES → serve from nearest in-sync replica (< 5ms)
        NO  → fetch 10 shards in parallel from object store
              → reconstruct locally (< 150ms for < 1GB pack)
              → optionally cache in warm tier for 24h
```

### 2.3 Identity architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     PRINCIPAL TYPES                          │
│                                                             │
│  HumanUser                AgentIdentity                     │
│  ────────────             ──────────────────────────────    │
│  id                       id                                │
│  email                    display_name                      │
│  credential_hash          parent_user_id ──────────────┐   │
│  rate_bucket:             permission_scope[]  (subset)  │   │
│    "human_default"        rate_bucket:                  │   │
│  quota_account_id         "agent_{tier}"                │   │
│                           session_quota                  │   │
│                           tokens_per_week_cap            │   │
│                           reputation_score               │   │
│                           quota_account_id               │   │
└─────────────────────────────────────────────────────────────┘
                               │parent_user_id
                               ▼
                    ┌──────────────────────┐
                    │    HumanUser         │  ← agent can never exceed
                    │    permission set    │    owner's permissions
                    └──────────────────────┘

IDENTITY RESOLUTION FLOW (Envoy WASM filter, < 1ms)
──────────────────────────────────────────────────
1. Extract credential from request (Bearer token / SSH key / mTLS cert)
2. Lookup in Redis (hot cache, TTL 60s)
   HIT  → return cached Principal struct
   MISS → query PostgreSQL identity domain → cache result
3. Mint a SPIFFE JWT-SVID (60s lifetime) with claims:
   { sub: principal_id, principal_type, org_id, rate_bucket,
     quota_account_id, aud: <target-service-set>, exp, iat }
4. Inject as Authorization: Bearer <jwt-svid> on the request to the
   downstream service, over mTLS (X.509-SVID workload identity)
5. Downstream verifies the JWT-SVID against the SPIRE trust bundle on
   every request — never trusts raw headers, never re-resolves credentials
6. SPIRE workload-attestation binds service certs to runtime workloads
   so a stolen cert cannot be used outside its attested workload
```

### 2.4 CI/CD architecture

```
┌──────────────────────────────────────────────────────────┐
│                  WORKFLOW PLANE — CI                      │
│                                                          │
│  CI Trigger Event (from Kafka: push.created / pr.opened) │
│         │                                                │
│  ┌──────▼──────────────────────────────────────────┐    │
│  │           Runner Assignment Service              │    │
│  │                                                  │    │
│  │  principal_type == human?                        │    │
│  │    → Hot Pool (Firecracker, sub-1s start)        │    │
│  │  principal_type == agent (no annotation)?        │    │
│  │    → Cold Pool (Firecracker, < 30s start)        │    │
│  │  annotation: "require-hot-pool"?                 │    │
│  │    → Hot Pool (debited from org quota)           │    │
│  └──────────────────────┬───────────────────────────┘    │
│                          │                               │
│         ┌────────────────┼───────────────┐               │
│         ▼                ▼               ▼               │
│    ┌─────────┐      ┌─────────┐    ┌─────────┐           │
│    │Hot Pool │      │ColdPool │    │ColdPool │           │
│    │VM fleet │      │VM fleet │    │VM fleet │           │
│    │(always  │      │(scale-  │    │(scale-  │           │
│    │ warm)   │      │ to-zero)│    │ to-zero)│           │
│    └────┬────┘      └────┬────┘    └────┬────┘           │
│         └────────────────┴──────────────┘                │
│                          │                               │
│  ┌───────────────────────▼──────────────────────────┐    │
│  │         Firecracker MicroVM (per job)            │    │
│  │  - Hardware isolated (no shared kernel)          │    │
│  │  - Ephemeral filesystem, destroyed on completion │    │
│  │  - Egress restricted to declared allowlist       │    │
│  │  - Credentials: short-lived, repo-scoped only    │    │
│  └──────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────┘
```

### 2.5 Agent session architecture (Temporal)

```
Agent (MCP client)
  │
  │  POST /v1/agents/{id}/sessions  { plan: "..." }
  ▼
Envoy → quota check → session created in Temporal
  │
  ▼
┌────────────────────────────────────────────────────────┐
│              Temporal Workflow: AgentSession            │
│                                                        │
│  1. PlanActivity                                       │
│     └── Store plan, return session_id to agent         │
│                                                        │
│  2. ApprovalActivity  (may wait indefinitely)          │
│     └── Human approves via UI / policy auto-approves   │
│                                                        │
│  3. ExecutionActivity[]  (parallel, N sub-tasks)       │
│     ├── ToolCallActivity (checkpointed every 60s)      │
│     │   └── Calls MCP tools: git_clone, search, etc.  │
│     ├── CommitActivity                                 │
│     │   └── git push → Git Plane (same flow as human)  │
│     └── [repeats until task complete or quota hit]     │
│                                                        │
│  4. CompletionActivity                                 │
│     ├── PR scoring pipeline                            │
│     ├── Reputation score update for AgentIdentity      │
│     └── Notify parent user                             │
│                                                        │
│  CHECKPOINT: Temporal persists state every activity.   │
│  Node loss → workflow resumes on new node, no work lost│
└────────────────────────────────────────────────────────┘
```

### 2.6 PR noise filter pipeline

```
Agent creates PR
  │
  ▼
┌────────────────────────────────────────────────────────┐
│              PR SCORING PIPELINE                        │
│                                                        │
│  Stage 1: Semantic Deduplication (Qdrant)              │
│  ─────────────────────────────────────────             │
│  Embed PR title + description (text-embedding model)   │
│  ANN search against open PRs in same repo              │
│  Cosine similarity > 0.92 → auto-close as duplicate    │
│                                                        │
│  Stage 2: Quality Signals                              │
│  ─────────────────────────────────────────             │
│  + CI pass/fail delta                                  │
│  + Test coverage delta (positive = good)               │
│  + Lint violations introduced                          │
│  + Diff size (too large = penalised)                   │
│  + Commit message coherence score                      │
│                                                        │
│  Stage 3: Agent Reputation                             │
│  ─────────────────────────────────────────             │
│  Lookup AgentIdentity.reputation_score                 │
│  (= historical merge_rate × recency_decay)             │
│                                                        │
│  Stage 4: Composite Score → Routing                    │
│  ─────────────────────────────────────────             │
│  Score ≥ 0.7  → Human review queue (normal priority)   │
│  Score 0.4–0.7→ Human review queue (low priority)      │
│               + auto-label: "ai-generated"             │
│  Score < 0.4  → Auto-close with explanation            │
│               + reputation_score decremented           │
└────────────────────────────────────────────────────────┘
```

---

## 3. Edge plane

| Property | Value |
|---|---|
| Technology | Envoy Proxy + custom WASM filters |
| Responsibility | TLS termination, identity resolution, rate limiting, token metering, circuit breaking |
| Identity output | SPIFFE JWT-SVID, 60s lifetime, embedded in `Authorization: Bearer ...` (ADR-010) |
| Rate-limit state | Redis token buckets per principal; 5s flush to ClickHouse for analytics |
| Hard rule | Edge is the only layer that terminates external TLS or resolves credentials. Downstream services verify JWT-SVID; they never re-resolve. |

Human and agent traffic use disjoint rate-limit buckets. Parallel sub-agents share their parent session's quota — never independent buckets — to prevent quota multiplication.

---

## 4. Git plane

| Property | Value |
|---|---|
| Technology | Go replication proxy + Gitaly (open-source from GitLab) + local NVMe file servers |
| Hot tier | 3× full replica per repository, 2-of-3 quorum writes, replicas across 3 failure domains in-region + 1 async DR replica in a second region |
| Cold tier | (10,4) Reed-Solomon EC on S3-compatible store; objects > 30 days; all LFS at write time |
| Independence contract | Repo-location lookup goes through Redis cache (TTL 600s); proxies serve cached repos with last-known location for ~60 min during a metadata-DB outage (ADR-009) |
| Direct `git` binary use | Forbidden. All Git operations go through Gitaly RPC. |

EC is **not** applied to hot data: interactive Git operations touch thousands of small objects (< 1 KB), and EC reconstruction requires 10 round trips vs. 1 for replication. EC is only economical once access patterns shift to occasional large sequential reads.

---

## 5. Application plane

| Property | Value |
|---|---|
| Technology | Go modular monolith |
| Metadata DB | PostgreSQL (ADR-006), single deployable, hard schema-domain boundaries |
| Schema domains | `identity`, `repositories`, `collaboration`, `ci`, `billing` |
| Cross-domain joins/transactions | Forbidden in CI; SQL linter rejects them |
| Event emission | Transactional outbox + polling-based outbox consumer → Kafka (ADR-008) |

PostgreSQL gives mature SQL semantics, serializable transactions, and a deep operational tooling ecosystem. Hot-table contention is managed by partitioning on `repo_id` / `org_id` and routing hot queries to read replicas via PgBouncer. Adapter interfaces (`MetadataStore`, `CacheStore`, `EventQueue`) in `plane/data/` define the swap surface for alternative implementations (ADR-017).

---

## 6. Workflow plane

| Property | Value |
|---|---|
| Technology | Temporal (orchestration) + Firecracker microVMs (CI isolation) + custom PR scoring service |
| Workflow primitives | `AgentSessionWorkflow`, `CIWorkflow`, `PRScoringWorkflow`, `MaintenanceWorkflow` |
| Determinism contract | Workflow code must be deterministic on replay — see `.claude/skills/gitscale-temporal-determinism/` |
| CI tiers | Hot pool (sub-1s start, full price), cold pool (< 30s start, ~40% price) |
| Default tier | Agent jobs → cold pool. Human jobs → hot pool. Annotation can override, debited from org quota |
| Isolation primitive | Firecracker microVM per job. Containers are insufficient for untrusted agent code (ADR-002) |

A session that loses its compute node resumes from the last checkpoint on a new node without losing work. This is the load-bearing answer to the long-running-agent problem.

---

## 7. Data plane

| Property | Value |
|---|---|
| Metadata | PostgreSQL (5 schema domains) |
| Cache + rate-limit state | Redis (key-value with pub/sub for cache invalidation) |
| Event bus | Kafka, fed by a polling-based outbox consumer reading PostgreSQL outbox tables (ADR-008) |
| Search | Vespa for all customer-facing search (code, issues, semantic); Qdrant scoped narrowly to PR dedup at cosine 0.92 (ADR-016) |
| Audit + traces | OpenTelemetry → ClickHouse (per-tenant attribution) |
| Metrics | Prometheus + Grafana (Mimir for long-term retention) |
| Object store | S3-compatible (cold tier; (10,4) Reed-Solomon at write time; per-tenant DEK encryption per ADR-011) |
| Service identity | SPIRE / SPIFFE (ADR-010) |
| Secret management | HashiCorp Vault (short-lived dynamic credentials) |

### 7.1 Event consistency model

Every span carries `principal_id`, `principal_type`, `org_id`, `repo_id`, `tokens_consumed`. Every state-mutating SQL transaction writes the source change AND an `outbox` row in the same transaction. The caller is acknowledged on DB commit, not on Kafka publication. Consumers must be idempotent on `event_id`.

### 7.2 Scaling axes

| Plane | Scaling axis | Unit |
|---|---|---|
| Edge | Horizontal (stateless) | Envoy pod |
| Git (hot) | Horizontal (add file servers) | 3-server replica set |
| Git (cold) | Object store auto-scales | Storage bytes |
| Application | Horizontal (stateless app servers) | Go service pod |
| Metadata | PostgreSQL primary + read replicas; partitioned hot tables | Node |
| Workflow | Temporal worker horizontal scaling | Worker pod |
| CI hot pool | Pre-warmed VM fleet | Firecracker VM |
| CI cold pool | Scale-to-zero, burst on demand | Firecracker VM |

### 7.3 Capacity targets

Sized for a single-region multi-AZ deployment.

| Metric | Target |
|---|---|
| Weekly commits | 2.75B |
| Concurrent agent sessions | 10M |
| Repositories | 100M |
| API requests/sec | 1M |
| CI minutes/week | 10B |
| Storage (hot tier) | 10 PB |
| Storage (cold tier) | 40 PB |

### 7.4 Cold-tier cost (40 PB)

Assuming $0.023/GB/month (S3 Standard) and $0.004/GB/month (S3 Glacier Instant):

| Approach | Monthly cost |
|---|---|
| 3× full replica on S3 Standard | $2.76M |
| (10,4) EC on S3 Glacier Instant | $184K |
| **Saving** | **$2.58M/month (~93%)** |

Hot tier (10 PB, NVMe bare metal) is sized for ~7-day working set and is not subject to EC overhead.

### 7.5 Failure scenarios and responses

| Failure | Impact without mitigation | Mitigation in this architecture |
|---|---|---|
| Single file server failure | Hot repo unavailable | 2-of-3 quorum continues serving; failed server skipped for new reads, still receives writes |
| Auth-DB cluster degraded | Platform-wide outage | Identity cached in Redis (60s TTL); Envoy serves cached principal for short outages |
| Kafka consumer lag | Search/webhooks/billing delayed | Consumers are async; user-facing writes unaffected; consumers catch up from log replay |
| PostgreSQL primary loss | Metadata writes blocked during failover | Streaming replication promotes a sync replica; Patroni-style automated failover; reads continue from replicas |
| PostgreSQL cluster degraded | All metadata writes fail | Git proxy serves push/pull from repo-location cache (~1 h degraded-mode coverage); new repo creation and ACL changes return 503 (ADR-009) |
| Temporal worker failure | In-flight agent sessions lost | Temporal checkpoints every activity; sessions resume on new workers |
| Cold storage shard failure | Object reconstruction degraded | (10,4) EC tolerates 4 simultaneous shard failures; repair queued at low priority |
| CI runner pool exhaustion | Jobs queued | Hot pool auto-scales; cold pool is scale-to-zero with burst ceiling |

### 7.6 What does not cascade

- Git writes never touch the application plane synchronously; commits + ref updates write only to the local replica set and emit an outbox row picked up by the polling consumer (ADR-008)
- The Git proxy depends on the metadata plane only for cache-miss repo-location lookups; cache-hit traffic survives a metadata-DB outage (ADR-009)
- Application plane never calls Git plane synchronously; it reads metadata from PostgreSQL
- Identity claims are signed JWT-SVIDs verified per request; a compromised internal service cannot impersonate other principals to its peers (ADR-010)
- CI jobs run in isolated Firecracker VMs; a runaway job cannot affect neighbouring jobs (ADR-002)
- All Kafka consumers (search, webhooks, billing) are decoupled subscribers; their failure does not block producers

### 7.7 Operational design contracts

Each row below is a design contract — the architecture must support the listed signal, mitigation, and recovery time. Implementation prose (operator click-by-click) lives outside this doc.

| Runbook | Detection signal | Architectural mitigation path | Recovery target |
|---|---|---|---|
| File server evacuation | SMART/hardware error counters; planned-maintenance flag | Mark draining; new writes route to peers; reads continue from in-sync replicas; background re-replication restores 3× factor | < 30 min for a 1 TiB node at 1 GB/s |
| Cold-tier repair storm throttling | > 2 shard failures in a stripe within 1 h; or repair queue > N | Repair scheduler rate-limited at storage migrator; `repair_concurrent_stripes` caps parallel repairs at 5% of cold-tier IOPS budget; CFQ priority 1:10 keeps hot-tier serving SLO intact | Hot-tier read p99 within SLO during repair; (10,4) repair within 24 h |
| Agent quota emergency reduction | Platform CPU > 85% sustained 10 min; or `agent_quota_exhaustion_rate` > 5×/min | Edge plane reads global `quota_pressure_factor` from Redis; multiplied into per-principal effective quota; operator sets factor (0.1× emergency, 1.0× normal); propagates in < 1s via Redis pub/sub | Pressure drop reflected at edge in < 1s |
| PostgreSQL hot-table contention | Table CPU > 90% sustained; per-shard QPS imbalance > 5× | Hash-partitioned hot tables on `repo_id` / `org_id`; PgBouncer routes read queries to replicas; manual `pg_repack` or partition rebalance for chronic hotspots | Hotspot quenches in < 30 min after partition / replica scale-up |
| Temporal worker scale-out | Workflow queue depth > 10K for 5 min | HPA on queue-depth + activity-task age; min replicas sized for steady-state; 4× scale-up cap in 5 min to avoid pool flap | Queue back below 10K within 15 min |
| PR scoring pipeline degraded | Score service p99 > 5s; `scorer_error_rate_total` > 1% over 5 min | Scoring is **advisory** on PR-merge critical path; degraded scorer does not block PR creation; last-known-good score retained, queue routes to medium-priority bucket | Back to p99 < 5s within 30 min; zero customer-visible PR creation impact |
| Outbox consumer lag | `outbox_consumer_high_water_lag_seconds` > 30s for 5 min | Independent polling consumers per outbox table; one lagging consumer does not block others; recovery: scale partition consumers or pause non-critical sinks | Lag back below 5s within 15 min |
| Repo-location cache eviction storm | Cache miss rate > 20% during metadata-DB outage | Per ADR-009: serve cached entries with last-known location for 600s + grace; misses for cold repos return 503; cache absorbs active working set during recovery | Design holds 1 h of metadata-DB unavailability without serving impact for cache-resident set |
| SPIRE issuance backlog | JWT-SVID mint p99 > 100ms; new-cert wait queue > 1000 | Standby SPIRE cluster takes over via DNS failover; cached JWT-SVIDs serve up to expiry (60s); workload re-attestation forced post-recovery | Mint back to p99 < 50ms within 5 min; new-session pause < 60s worst case |
| Vault key rotation stall | Re-encryption queue depth growing for 1 h | Re-encryption runs at `reencrypt_iops_budget`; lower priority vs. serving traffic; durability not at risk because old DEK still decrypts | Queue back to drainable within 4 h of operator action |

**Contract holds across the board:** any architectural change that would prevent meeting one of the recovery-time targets re-opens the relevant ADR, not the runbook.

---

## 8. Architecture Decision Records

> **Renumbering note (May 2026):** ADR numbers were re-allocated in this revision. The former ADR-007 (CockroachDB metadata) was removed; MCP is now ADR-007. Any external artifact (PR descriptions, issue comments) citing the old ADR-007 (CockroachDB) should be read as referring to the superseded decision, not ADR-007 here.

ADRs are binding. New ADRs append; existing ADRs are amended in-place with a dated changelog entry. Code that contradicts an ADR must either match the ADR or ship a same-PR amendment. Silent contradiction is forbidden — see `.claude/skills/gitscale-adr-guard/`.

### ADR template

```
ADR-NNN: <decision in past tense>
Status: Proposed | Accepted | Superseded by ADR-MMM
Date: YYYY-MM-DD
Context: <why the decision was needed>
Decision: <what was decided, in one paragraph>
Consequences: <positive, negative, follow-ups>
```

---

### ADR-001: Adopted hot-tier 3× replica + cold-tier (10,4) Reed-Solomon EC

- **Status:** Accepted
- **Date:** TBD
- **Context:** Storage strategy must serve interactive Git operations (small random reads) and long-tail historical access (occasional large sequential reads) on the same platform. EC everywhere optimises cost but penalises interactive latency; replication everywhere is durable but doubles cold-tier cost.
- **Decision:** Hot tier (< 7 days active) uses 3× full replica on local NVMe with 2-of-3 quorum writes. Cold tier (> 30 days, all LFS) uses (10,4) Reed-Solomon EC on S3-compatible object storage. Tier transitions are background-driven by the storage migrator.
- **Consequences:** ~93% cold-tier storage cost saving vs. naive 3× cold replica. Cold-tier read latency 50–150ms (acceptable for history). Hot-tier remains single-digit-ms p99. Migration window introduces a small window of dual-stored data.

### ADR-002: Adopted Firecracker microVMs for CI isolation

- **Status:** Accepted
- **Date:** TBD
- **Context:** CI runs untrusted agent-generated code at scale. Container-based isolation (Docker, gVisor) shares kernel surface area; one container escape can compromise neighbours and the host.
- **Decision:** Every CI job runs in a Firecracker microVM with its own kernel and ephemeral filesystem, destroyed after job completion. Egress is restricted to a declared allowlist; credentials are short-lived and repo-scoped.
- **Consequences:** Hardware boundary against untrusted code. Sub-second boot keeps interactive CI tractable. Operationally heavier than containers; offset by the security floor it establishes. Docker creep alongside Firecracker is forbidden — see `.claude/skills/gitscale-firecracker-isolation/`. For operational-analytics archives (e.g. billing.usage_events), see ADR-018.

### ADR-003: Adopted Temporal for workflow orchestration

- **Status:** Accepted
- **Date:** TBD
- **Context:** Long-running agent sessions (hours), CI pipelines (parallelisable, retryable), and PR scoring pipelines need durable, checkpointed orchestration. Hand-rolled state machines on Kafka or a job queue lose work on node failure.
- **Decision:** Temporal is the orchestration layer for all long-running and durable workflows. Workflow code is held to determinism constraints; activities are the IO boundary.
- **Consequences:** A 4-hour refactoring agent survives node loss without losing work. Workflow versioning policy is required to evolve in-flight workflows. Determinism rules must be enforced — see `.claude/skills/gitscale-temporal-determinism/`.

### ADR-004: Adopted Kafka as the event bus

- **Status:** Accepted
- **Amended:** 2026-05-09
- **Date:** TBD
- **Context:** Search, webhooks, billing, and audit consumers must observe state changes asynchronously without coupling to the application plane. Synchronous fan-out is a tight-coupling failure mode.
- **Decision:** Kafka is the canonical event bus. Per-partition ordering is preserved per aggregate, keyed on `aggregate_id`
  (UUID) for every topic. Repo-level or org-level ordering is not guaranteed;
  consumers requiring it must reorder by `(aggregate_id, published_at)` after
  consumption. The previous `repo_id`/`org_id` keying was rejected to avoid
  hot-partition risk under agent traffic. Cross-partition ordering is undefined. Topics are fed by the polling-based outbox consumer reading PostgreSQL outbox tables (ADR-008). Brokers run replication factor 3 with `min.insync.replicas=2`; producers use `acks=all` and idempotent producer config. A topic that cannot meet ISR=2 fails writes loudly rather than silently degrading durability.
- **Consequences:** Log semantics enable replay, fan-out, and audit. Consumers must be idempotent on `event_id`. Direct Kafka producer paths from application code are forbidden — see `.claude/skills/gitscale-outbox-check/`. Cross-links: design specs for issues #11 and #12
  (docs/superpowers/specs/2026-05-02-issue-11-outbox-consumer-design.md,
  docs/superpowers/specs/2026-05-02-issue-12-kafka-topology-design.md). Single-broker loss is tolerated; two-broker loss surfaces as producer failure, preserving the no-silent-data-loss contract.

### ADR-005: Adopted Go for the application plane

- **Status:** Accepted
- **Date:** TBD
- **Context:** The application plane needs a language with predictable performance under high concurrency, a rich standard library, and a hireable talent pool.
- **Decision:** Go is the primary language for the application, edge-filter-build-side (WASM compilation tooling), workflow worker, and Git proxy code paths. Conventions are tracked in `.claude/skills/gitscale-go-conventions/`.
- **Consequences:** Predictable GC, good concurrency primitives, fast cold start. Generics ergonomics weaker than alternatives but adequate. Cross-cutting libraries (telemetry, errors, retry) are project-vendored.

### ADR-006: Adopted PostgreSQL for the metadata layer

- **Status:** Accepted
- **Date:** TBD
- **Context:** Metadata layer needs serializable transactions, mature SQL semantics, a deep operational tooling ecosystem, and a swap-friendly interface so alternative engines can plug in. Hot-table contention must be manageable through partitioning and read-replica routing rather than a single-leader bottleneck.
- **Decision:** PostgreSQL is the metadata database for all five schema domains (identity, repositories, collaboration, ci, billing). Access goes through a `MetadataStore` Go interface defined in `plane/data/`; concrete drivers conform to a shared compliance suite. Hot tables (outbox, ref-update logs) are hash-partitioned on `repo_id` / `org_id`; read traffic is routed to replicas via PgBouncer.
- **Consequences:** Engineers write standard PostgreSQL. Foreign keys and serializable transactions are available. The outbox pipeline (ADR-008) is a polling-based consumer rather than a native CDC stream. Alternate `MetadataStore` implementations slot in without touching application code.

### ADR-007: Adopted MCP as the agent-facing tool protocol

- **Status:** Accepted
- **Date:** TBD
- **Context:** Agent traffic needs a tool-shaped surface that is portable across SDKs and that binds policy enforcement (AGENTS.md, plan-approval) to platform-side gates rather than SDK promises.
- **Decision:** A native MCP server exposes Git, PR, issue, CI, search, and quota tools to agent clients. Protocol-version target at launch is tracked as an open question.
- **Consequences:** Agents speak a tool surface, not a REST surface. Enforcement is in the MCP middleware and the Git pre-receive hook, never in the SDK. Standard avoids a proprietary agent protocol.

### ADR-008: Adopted outbox-based event consistency with a polling-based outbox consumer

- **Status:** Accepted
- **Amended:** 2026-05-09
- **Date:** TBD
- **Context:** State changes must be observable by search, webhooks, billing, and audit consumers without dual-writes that risk divergence between the DB and the event bus. The publisher must be portable across metadata-store engines (per ADR-006) without coupling the application code to engine-specific CDC.
- **Decision:** State-mutating SQL transactions write the source change AND an `outbox` row in the same transaction. The caller is acknowledged on DB commit, not on Kafka publication. A polling-based outbox consumer (advisory-locked `SELECT … WHERE processed = false ORDER BY created_at LIMIT N` loop) drains each outbox table and publishes to Kafka with idempotent producer config. Outbox rows TTL-expire 24 h after the consumer high-water mark advances past them. Consumers must be idempotent on `event_id`. The outbox consumer tolerates up to 30 s of PostgreSQL primary→replica lag without skipping events; events past that threshold trigger a paging alert on `outbox_replica_lag_seconds`. Read-replica reads are forbidden for the outbox draining loop — the consumer reads the primary directly.
- **Consequences:** No dual-write race. Publish latency bounded by poll interval (default 1s; tunable). At-least-once delivery contract; consumers carry the idempotency burden. Logical replication or engine-native CDC is the upgrade path if poll latency becomes a constraint, swappable behind the same `EventQueue` interface. See ADR-019 for the workflow-plane variant of this invariant.

### ADR-009: Adopted Redis for the rate-limit and identity cache

- **Status:** Accepted
- **Date:** TBD
- **Context:** Edge-plane identity resolution and per-agent rate limiting need a fast key-value store with pub/sub for cache invalidation. The Git proxy also needs a repo-location cache that survives a metadata-DB outage for ~1 hour of degraded-mode push/pull serving. The cache implementation must sit behind a swap-friendly interface so alternative engines can plug in.
- **Decision:** Redis serves the rate-limit, identity, and repo-location caches behind a `CacheStore` Go interface defined in `plane/data/`. Repo-location cache TTL is 600s with last-known-location grace served during metadata-DB outage; ACL invalidations propagate via Kafka invalidation events that cache nodes also subscribe to. Sub-second enforcement counters are AOF-durable.
- **Consequences:** Decouples push/pull serving from metadata-DB availability for ~1 h of degraded-mode coverage. AOF durability for enforcement counters. Alternate `CacheStore` implementations slot in without touching call sites.
- **Amendment 2026-05-06 — repo-location cache contract.** The original decision named TTL and invalidation propagation but left the payload struct, key shape, and event list undefined. Closed by issue #17.

  **Key shape.** `repo:loc:<repo-uuid>` under the `gitscale:<env>:` namespace prefix applied by `cache.WithNamespace`. Keys never include the namespace prefix at definition time.

  **Payload struct (versioned).** JSON, with a top-level integer `v` field. Current version is 1.

  ```json
  {
    "v": 1,
    "replica_set_id": "<gitaly replica set id>",
    "home_region": "<region anchor>",
    "acl_fingerprint": "<sha-256 hex of canonicalised repo_permissions rows>"
  }
  ```

  - `replica_set_id`, `home_region` — sourced from `repositories.repositories` (#7).
  - `acl_fingerprint` — derived field, computed in the loader from the repo's `repo_permissions` rows; opaque to consumers; used to detect stale ACL state without forcing a full read of the permissions table.
  - Decoders MUST treat any payload whose `v` does not match the expected version as a miss and re-load. This permits forward-incompatible changes without a global flush.

  **Negative-cache sentinel.** A miss for a non-existent repo is cached as `{"v":1,"_miss":true}` with TTL `RepoLocationNotFoundTTL = 30s`. Prevents thundering herd against the metadata DB for paths that scan non-existent repo URLs (404 spam, fuzzers).

  **TTLs.**

  - `RepoLocationTTL = 600s` — matches the original decision; covers ~1 h degraded-mode service via the grace-serve path.
  - `RepoLocationNotFoundTTL = 30s` — short enough that a freshly created repo becomes resolvable quickly; long enough to absorb 404 storms.

  **In-process miss collapsing.** Each pod uses `singleflight` keyed on the cache key, so concurrent cache misses for the same repo result in one loader call per pod per TTL expiry — never one per request.

  **Invalidation triggers (Kafka events on `gitscale.repositories.events`).** Subscribers to the repo-location cache MUST act on these event types. Names are normative — emitters and consumers MUST use these strings exactly:

  - `repo.created` — write-through prime: `SetRepoLocation` (cache ahead of first read).
  - `repo.replica_set_changed` — delete key (location drift).
  - `repo.home_region_changed` — delete key (location drift).
  - `repo.permissions_changed` — delete key (ACL fingerprint stale).
  - `repo.deleted` — delete key; the next miss writes the negative-cache sentinel.

  **Resilience contract.** During a Kafka outage, cache nodes serve last-known location for the full TTL plus a grace window; the original decision's "~1 h degraded-mode coverage" continues to hold because invalidation is best-effort within the TTL window, not synchronous.

  **Implementation reference.** The contract above is implemented in `plane/data/cache/repo_location.go` and `plane/data/cache/keys.go` (#13). A separate consumer that performs the deletes is tracked under [identity-cache-invalidator pattern, #27] and the equivalent repo-location-cache invalidator (future issue, follows the same template).

### ADR-010: Adopted SPIRE/SPIFFE for service identity and end-to-end principal propagation

- **Status:** Accepted
- **Date:** TBD
- **Context:** Cross-plane and cross-service calls need cryptographic identity that is verifiable per request, rotatable without downtime, and auditable. Trusting raw `X-Principal-*` headers stamped by Envoy is a defense-in-depth gap; a compromised internal service could impersonate other principals to its peers.
- **Decision:** SPIRE issues SPIFFE X.509-SVIDs for workload mTLS and JWT-SVIDs for principal identity propagation. JWT-SVIDs carry `principal_id`, `principal_type`, `org_id`, `rate_bucket`, `quota_account_id`, `aud`, `iat`, `exp` (60s lifetime) and are verified per request against the SPIRE trust bundle.
- **Consequences:** Every service needs a SPIRE workload entry. Clock skew tolerance and revocation paths must be designed. A compromised peer cannot replay tokens cross-service or after expiry.

### ADR-011: Adopted per-org encryption with scoped dedup on cold tier

- **Status:** Accepted
- **Date:** TBD
- **Context:** Cold-tier (10,4) EC drives ~93% storage cost savings, but naive content-addressed dedup across orgs enables a confirmation-of-file attack: an adversary who suspects an org holds a specific blob can confirm it by uploading the same content and observing dedup behaviour. A self-hosted deployment may host multiple orgs, and per-org confidentiality must hold regardless of tenancy model.
- **Decision:** Each org has a master key in HashiCorp Vault. Per-repo DEKs are derived from the org master and used to encrypt object content before EC sharding. Object content keys are derived as `HKDF(DEK, object_hash_within_repo)`. Dedup applies within-repo always; within-org across repos behind a feature flag; cross-org never. EC stripes are per-org. Whole-repo deletion is implemented as crypto-shredding the per-repo DEK (O(1) logical deletion).
- **Consequences:** Confirmation-of-file attack structurally impossible across orgs. Right-to-erasure path is fast. Marginal storage overhead for very small orgs is acceptable cost-of-isolation. Billing archive encryption follows a different KEK scope per ADR-018.

### ADR-012: Adopted Git-RPC metering at the Gitaly hook layer with two-tier counters

- **Status:** Accepted
- **Date:** TBD
- **Context:** Gateway-only metering misses the cost of pack-objects build and cold-tier reconstruct that the Git plane incurs but the gateway cannot see. A single counter cannot serve both sub-second enforcement (deny / throttle / 429) and accurate analytics simultaneously.
- **Decision:** Metering captures at Gitaly hook layer (`pack_objects`, `pre-receive`) tagged via JWT-SVID claims. Two-tier counters: Redis enforcement counter (sub-second, AOF-durable, authoritative for "over quota now"); ClickHouse analytics counter (5s flush, idempotent on `event_id`, authoritative for usage reports and audit). Nightly reconciliation bounds drift to ≤ 0.5%.
- **Consequences:** Captures pack-objects + cold-reconstruct cost. Sub-second enforcement prevents quota-window blow-through. Reconciliation contract is load-bearing for usage accuracy.

### ADR-013: Adopted GraphQL with named-subset compatibility, query-cost analysis, and persisted queries

- **Status:** Accepted
- **Date:** TBD
- **Context:** Ecosystem compatibility requires a GraphQL surface; full bug-for-bug parity with any incumbent is unbounded scope and a permanent schema-drift problem. GraphQL's "ask for everything" affordance is the load-bearing capacity risk if not gated.
- **Decision:** GraphQL ships with: a *named-subset* schema-shape compatibility contract (top-level query roots and most-used field/connection shapes); query-cost analysis ahead of execution with operator-configurable per-principal ceilings; Apollo-APQ-compatible persisted queries with a 0.5× cost multiplier; follower-read default; 12-month deprecation window for any field or mutation removal.
- **Consequences:** Predictable capacity envelope. Persisted queries are the approved hot path. Clients targeting compatible GraphQL surfaces work for the named subset, not arbitrarily.

### ADR-014: Adopted plugin interface governance (semver per interface, 12-month deprecation, sandbox by input trust)

- **Status:** Accepted
- **Date:** TBD
- **Context:** Plugin interfaces are public API contracts. Once shipped, breaking changes harm operators and downstream plugin authors simultaneously. In-process Go plugins handling untrusted input (PR text, external feeds, third-party signals) are a confused-deputy risk.
- **Decision:** Each plugin interface carries an independent `vMAJOR.MINOR.PATCH` in its package path. 12-month deprecation window. `MAJOR` bumps require an RFC. Sandbox by input trust: load-bearing plugins (`IdentityProvider`, hot-tier replica coordinator) in-process; capability-scoped plugins (`MeteringSink`, `TieringPolicy`) in-process with handle scoping; sensitive-input plugins (`Scorer`, `SignalSource`, `ReputationProvider`) as gRPC sidecars over UDS with sandboxed filesystem and resource caps.
- **Consequences:** Buggy `Scorer` cannot crash the app plane. Buggy `IdentityProvider` does not pay 50ms per-call sidecar tax. Sidecar mTLS uses the same SPIFFE workload identity as service-to-service.

### ADR-015: Adopted plan-approval / risky-action policy model with predicate-based rules and Merkle-chained audit

- **Status:** Accepted
- **Date:** TBD
- **Context:** "Ask first" in agent SDKs is meaningless without platform-side enforcement. Risky-action approval needs a definition of *who* approves, *what* needs approval, *how long* an approval is valid, *what happens* on no response, and *how* every decision is auditable.
- **Decision:** Signed Policy objects per-org/repo, with rule-based predicates (e.g., `pr_merge:main`, `force_push:*`, `production_deploy`, `bulk_action:N`), named ApproverGroups with required-count, EscalationLadder with `notify` / `auto_deny` / `fall_back` actions. Approvals are bound to a plan hash and re-open on plan mutation. Audit log is Merkle-chained per Policy. Approvers must be HumanUser principals; agents may propose but never approve.
- **Consequences:** SDK-level "ask first" hints can be promoted to platform-enforced policy by org admins. ML-based risk classification is deferred until signal exists. Approval expiry default 24 h; security-review class 4 h. No platform-default `auto_approve` step.

### ADR-016: Adopted Vespa as primary search; Qdrant scoped to PR deduplication

- **Status:** Accepted
- **Date:** TBD
- **Context:** Customer-facing search spans code, issues, and semantic queries. PR dedup is a narrower vector-similarity problem with a fixed cosine threshold and very high write throughput.
- **Decision:** Vespa is the primary search backend for all customer-facing search and the `search_*` MCP tools. Qdrant is reserved for PR deduplication only, with a cosine similarity threshold of 0.92. Qdrant is not exposed as customer-facing search.
- **Consequences:** Single search backend for end users. PR dedup runs on a narrow, separately-tuned vector store with a different operational shape.

### ADR-017: Adopted Go interface abstractions as the swap surface for pluggable backend implementations

- **Status:** Accepted
- **Date:** TBD
- **Context:** ADR-006 chose PostgreSQL and ADR-009 chose Redis as the concrete backend implementations. Future deployments may need to swap these for alternative engines without changing application code. A defined interface boundary also enables compliance testing — all implementations can be verified against the same test suite before production use.
- **Decision:** Three Go interfaces are defined in `plane/data/`: `MetadataStore` (all SQL operations across the five schema domains), `CacheStore` (key-value, pub/sub, and TTL semantics needed by the edge and Git proxy), and `EventQueue` (outbox-to-Kafka publishing). Application code never imports a concrete driver; it receives a concrete implementation injected at startup. Every implementation must pass the shared compliance test suite in `plane/data/compliance/`.
- **Consequences:** Alternative backend implementations slot in without touching call sites. Passing the compliance suite is the production-readiness bar for any implementation. The interface boundary also makes unit testing tractable: a conforming in-memory stub suffices for tests that do not need production semantics. Workflow-plane consumers of these interfaces follow ADR-019 routing.
- **Amendment 2026-05-06 — EventQueue naming reconciliation.** The `EventQueue` swap surface named in the original decision is implemented by `plane/data/outbox.KafkaProducer`. The matching compliance suite lives at `plane/data/compliance/eventqueue.go`. Application code references `outbox.KafkaProducer` at injection sites; the compliance contract enforces the swap-surface invariant. The name `EventQueue` is preserved in ADR text for conceptual clarity; the Go type is `KafkaProducer`.

### ADR-018: Adopted analytics-lake archival for billing `usage_events` (separate bucket, Parquet+zstd, S3 lifecycle, platform-KEK with per-month DEK)

- **Status:** Proposed
- **Date:** 2026-05-06
- **Context:** `billing.usage_events` is a high-volume, append-only operational-analytics table partitioned monthly in PostgreSQL. After the hot retention horizon, partitions must be archived in a queryable, durable, encrypted, retention-managed format. ADR-002's Git cold-tier shape (per-org encryption, EC striping) is a poor fit: usage_events span all orgs in a single time window, reads are broad scans rather than small random reads, and the data set is small relative to Git cold storage.
- **Decision:**

  **Storage target.** Separate S3-compatible bucket `gitscale-analytics-${env}` distinct from the Git cold-tier bucket. Hive-partitioned layout `billing/usage_events/year=YYYY/month=MM/usage_events_YYYY_MM.parquet`, plus matching `.manifest.json` and `.checksum.sha256` siblings.

  **Format.** Parquet with zstd compression. Schema embedded; one file per source PG partition. Writer is the `DetachAndArchivePartition` activity in the billing partition rollover workflow (#18-archive).

  **Durability.** S3 standard 3× replication for ≤90 days, lifecycle-transitioned to Glacier Instant Retrieval 90d–2y, Glacier Deep Archive 2y–7y, expired at 7y+30d. No project-managed erasure coding; S3 native durability suffices for this workload's read profile.

  **Hot retention.** 18 months in PG (active + reconciliation overlap), then archive. Total retention 7 years from partition creation; aligned with US tax / SOC2 / industry billing-audit floors.

  **Query path.** Athena (AWS) or Trino (multi-cloud) is the primary analyst query path. Glue Data Catalog (or Hive metastore) is updated by the archive activity after each upload. DuckDB-on-laptop is an acceptable fallback for ad-hoc work. A separate `RestorePartition` Temporal workflow handles dispute investigations that require SQL parity with live PG (out of scope for #18-archive; deferred).

  **Encryption.** Platform-level KEK in HashiCorp Vault. Per-month DEK derived as `HKDF(platform_billing_master, "year-month")`. Each Parquet file is encrypted with one DEK use. Crypto-shred semantics preserved for post-7y deletion: destroying the month's DEK renders the corresponding archive unrecoverable. ADR-011's per-org encryption pattern is intentionally NOT applied here — usage_events span orgs in a single time window, and per-org files would explode cardinality and break Athena partition pruning.

  **Cross-org dedup.** Not applicable. `usage_events.external_event_id UNIQUE` guarantees row uniqueness; there is no dedup decision to make.

- **Consequences:** Audit and reconciliation queries are SQL-native via Athena/Trino without restore. Storage cost is ~95% lower at year 2+ vs always-on PG. Crypto-shred preserves post-retention erasure guarantees. The decision is decoupled from ADR-002 (Git cold tier): the two cold tiers can evolve independently. `RestorePartition` capability is acknowledged but deferred. The shape locks in for 7 years of data — schema evolution must be backwards-compatible at the Parquet level (Parquet supports column-add gracefully; column-rename or column-drop is a breaking change and requires a v2 directory).

### ADR-019: Adopted application-plane RPC as the only state-mutation path from the workflow plane

- **Status:** Accepted
- **Date:** 2026-05-06 (proposed); 2026-05-07 (accepted after #33 workflow bootstrap and #18-rollover billing partition rollover both shipped against `appclient`)
- **Context:** Temporal workflows in `plane/workflow/` need to mutate metadata-layer state — partition rollover, agent-session lifecycle, CI pipeline state transitions, future automated remediation. Two architectural paths exist: (1) direct path — workflow activities receive `plane/data/store.MetadataStore` and call `Transact` + `WriteOutbox` themselves; (2) app-plane RPC path — workflow activities receive a thin gRPC client (`plane/workflow/appclient/`) into the application plane's per-domain service, which performs the Tx + outbox write. The direct path is technically permitted by the existing interfaces; ADR-008 is preserved either way. The choice is not about transactional integrity; it is about where domain invariants live.
- **Decision:**

  Workflow plane state mutations route exclusively via per-domain gRPC services in `plane/workflow/appclient/` → `plane/application/<domain>/`. The direct path (workflow activities calling `MetadataStore.Transact` for writes) is forbidden.

  Workflow plane reads MAY use `plane/data/store` interfaces directly via the activity adapter. Pure-DDL maintenance activities (e.g. partition rollover) MAY use `MetadataStore` directly, because the operation has no outbox row and no domain invariant.

  **Boundary clarification (amended 2026-05-08, issue #81):** the read/DDL carve-out permits `plane/workflow/<domain>/` to *import* `plane/data/store/<domain>` interface and read-side row types in-process — no obligation to redeclare the interface inside the workflow plane. The carve-out is bounded by three guardrails:

  1. Imports are permitted only for (a) interfaces whose methods are pure-DDL (`CREATE`, `ALTER`, `DROP`, `DETACH`, `ATTACH PARTITION`) or read-only (`SELECT` / cursor scans), and (b) row/result structs consumed read-side.
  2. The moment a workflow activity needs to call a method that emits an outbox row or updates a domain aggregate, the import must be removed from `plane/workflow/<domain>/` and the call routed through `plane/workflow/appclient/` → `plane/application/<domain>/`.
  3. A static check (`go test ./internal/architecture/...` or equivalent lint) asserts no method named `Transact*`, `WriteOutbox*`, `Insert*`, `Update*`, `Delete*` is reachable from `plane/workflow/*` via `plane/data/store/*`. The rule is enforced; the carve-out is not a blanket pass.

  No workflow activity writes more than one domain's outbox row in a single execution. Cross-domain workflows compose per-domain saga steps with explicit compensation activities.

- **Consequences:** Domain invariants live in the application plane only. Auth and audit context is uniform — the application plane stamps `actor_id`, `principal_kind`, `rate_bucket` on every outbox payload; a workflow activity has no human principal and routes through the app plane for a clear audit trail (`actor_kind=service`). Single-writer per aggregate keeps schema migrations sane. Workflow tests gain a clean stub surface. Latency cost: one network hop per write activity (acceptable; Temporal activities are async and retry-tolerant). Pure-DDL exception is narrow: `CreatePartition` writes no row, emits no outbox, has no invariant. Cross-domain saga rule prevents distributed-Tx patterns: two-phase commit across domain outbox tables is forbidden; cross-domain workflows compose per-domain saga steps with explicit compensation.

---

### Pending / un-numbered

- Envoy + WASM at the edge for identity resolution and token metering (described in §3 above; promote to ADR as the WASM filter set stabilises).
- Direct `git` binary use is forbidden; all Git operations go through Gitaly RPC (described in §4 above; promote to ADR if a competing approach is proposed).

### Open questions (no ADR yet)

| Question | Decision target |
|---|---|
| Erasure coding library: ISA-L vs. Reed-Solomon Go | June 2026 |
| MCP server protocol version target | July 2026 |
| PR reputation model: rule-based vs. ML-based | July 2026 |
| AGENTS.md schema versioning policy | July 2026 |
| Cross-org dedup feature-flag default | August 2026 |
