# GitScale Design Document

> Living document. The design document captures **how** GitScale implements the architecture, mapping each design choice to the ADRs in [`architecture.md §8`](architecture.md#8-architecture-decision-records) that bind it. Where this doc references `ADR-NNN`, the binding decision lives in architecture.md; this doc explains the implementation that follows from it. CI check `.github/workflows/adr-check.yml` enforces that every `ADR-NNN` mentioned here is present in `architecture.md`.

## 1. Design philosophy

Three principles govern every decision in this document:

1. **Agents are the primary traffic class.** Every default — rate limits, quota structures, identity models, CI tier selection — is calibrated for machine traffic. Human developers are a special case handled on top of an agent-first foundation.
2. **Loose coupling at every seam.** Every service boundary is an event-driven, asynchronous seam. No service calls another synchronously unless there is no alternative.
3. **Metering is infrastructure, not billing.** Token consumption, compute minutes, storage bytes, and egress are measured at the infrastructure layer and exposed as primitives. Pricing tiers sit on top of metering — they never drive architectural decisions.

---

## 2. High-level design

### 2.1 Conceptual model — five planes

```
┌─────────────────────────────────────────────────────┐
│                    CLIENT PLANE                      │
│   Human devs (Git CLI, Web UI)  │  AI Agents (MCP)  │
└──────────────────┬──────────────────────────────────┘
                   │
┌──────────────────▼──────────────────────────────────┐
│                    EDGE PLANE                        │
│   Envoy Gateway · Rate Limiting · mTLS Termination  │
│   Agent Identity Resolver · Token Metering          │
└────┬──────────────┬─────────────────────────────────┘
     │              │
┌────▼────┐   ┌─────▼───────────────────────────────┐
│  GIT    │   │           APPLICATION PLANE           │
│ PLANE   │   │  Repos · PRs · Issues · Users · Auth  │
│ Gitaly  │   │  (Modular monolith — Go)              │
│ Proxy   │   └─────────────┬───────────────────────┘
│ + 3x    │                 │ Events (Kafka)
│ replica │   ┌─────────────▼───────────────────────┐
└────┬────┘   │           WORKFLOW PLANE              │
     │        │  CI/CD (Firecracker) · Agent Sessions │
     │        │  Temporal Orchestration · PR Scoring  │
     │        └─────────────┬───────────────────────┘
     │                      │
┌────▼──────────────────────▼─────────────────────────┐
│                    DATA PLANE                        │
│  Hot Git Storage (NVMe, 3x replica)                 │
│  Cold Storage (Erasure-coded object store)          │
│  Metadata DB (PostgreSQL)                           │
│  Cache (Redis)   Search (Vespa + Qdrant)            │
└─────────────────────────────────────────────────────┘
```

### 2.2 Traffic flow — human Git push

```
git push
  → Envoy (mTLS, rate limit check, token meter, JWT-SVID stamp)
  → Git Plane proxy (repo lookup: Redis cache first, PostgreSQL on miss)
  → Three-phase commit to 3 NVMe replicas (quorum on 2)
  → Outbox row written in same TX as ref update on metadata plane
  → Ref update acknowledged to client
  → Polling outbox consumer publishes outbox row to Kafka (async, at-least-once)
  → Subscribers (idempotent on event_id): Search indexer, CI trigger,
    Webhook fanout, Audit log, Billing aggregator
```

### 2.3 Traffic flow — agent agentic session

```
Agent submits plan via MCP
  → Envoy (agent identity resolved, agent quota checked)
  → Workflow Plane: plan stored, awaiting approval
  → Approval (human policy / automated policy)
  → Temporal workflow created, execution begins
  → Agent actions metered per token at gateway
  → Each commit/PR goes through same Git push flow above
  → Session checkpointed every 60s; survives infra failures
  → On completion: PR score computed, noise filter applied
  → If score above threshold: enters human review queue
  → If below threshold: auto-labeled, deprioritised
```

---

## 3. Component design

### 3.1 Edge plane

**Technology:** Envoy Proxy + custom WASM filters

The edge plane is the single entry point for all traffic. It is the only layer allowed to terminate external TLS, resolve identity, and enforce rate limits. Identity is propagated to downstream services as a **signed identity envelope** — a SPIFFE JWT-SVID issued by Envoy with the resolved principal as embedded claims. Downstream services verify the SVID's signature against the SPIRE trust bundle on every request; they never trust raw headers and never re-resolve credentials. This is detailed in §6.1 and ADR-010.

**Responsibilities:**
- TLS termination and mTLS enforcement for service-to-service calls
- Identity resolution: maps inbound credentials to a typed principal (HumanUser | AgentIdentity | CIRunner | ServiceAccount)
- Rate limiting via token bucket per principal, applied before any application logic runs
- Token metering: every request is tagged with consumed tokens; counters flush to ClickHouse every 5 seconds
- Circuit breaking: sheds load from misbehaving clients (high error rate, quota exhaustion) at the edge, not deep in the stack

**Agent vs. human rate limits.** Human users share a per-account rate-limit bucket. Agent identities have a separate, higher-ceiling bucket but with a hard weekly cap enforced by a sliding-window counter in Redis. Parallel sub-agents share their parent session's quota, not independent buckets, preventing quota circumvention through session multiplication.

### 3.2 Git plane

**Technology:** Go replication proxy + Gitaly (open source from GitLab) + local NVMe file servers.

The Git plane stores the actual repository data. It is the highest-write-throughput component in the system and must remain serving for any repository whose location-of-record is reachable, even when the application-plane metadata DB is degraded.

**Independence contract.** The Git proxy resolves a repository's location-of-record (home region, replica set, ACL fingerprint) from a **repo-location cache** in Redis (TTL 600s, refreshed asynchronously). The cache is hydrated from PostgreSQL on miss; on metadata-DB outage, the proxy serves all cached repositories with the last-known location and ACL fingerprint, marks writes as "deferred-audit" until the metadata plane recovers, and rejects only the long tail of cold repositories that have aged out of cache. See ADR-009 for the trade-off.

**Storage topology — hot tier:**
- Every active repository lives on 3 file servers chosen pseudo-randomly across the node pool
- Writes go through a Go proxy that streams to all 3 replicas simultaneously and runs a three-phase commit; success requires 2-of-3 acknowledgements
- Reads are routed to the geographically nearest in-sync replica
- Replicas are spread across 3 independent failure domains (racks or AZs) within a region, with at least 1 replica in a second region for DR

**Storage topology — cold tier:**
- Pack files older than 30 days are compacted by a background GC process and pushed to an erasure-coded object store (S3-compatible, (10,4) Reed-Solomon)
- LFS objects bypass hot-tier entirely and go direct to cold-tier object storage at write time
- A Bloom filter on each file server tracks which objects are local vs. cold-tier; cache misses transparently fetch from object store
- Cold-tier reads are served via parallel shard fetches; reconstruction latency is acceptable for infrequently accessed history

**Content deduplication:**
- Git objects are content-addressed by SHA-256; the object store deduplicates within the scopes defined in §4.4.2 (within-repo always; within-org behind a feature flag; never cross-org) (ADR-011)
- Forks and mirrors share the vast majority of object data; storage cost per fork is proportional only to its delta, not the full repository

**Why not EC everywhere.** Interactive Git operations (push, fetch, clone) touch thousands of small objects (< 1 KB). EC reconstruction of small objects requires 10 network round trips vs. 1 for replication. EC is only applied after the object is cold and access patterns shift from "many small random reads" to "occasional large sequential reads".

### 3.3 Application plane

**Technology:** Go modular monolith with clear schema domains; PostgreSQL for metadata via the `MetadataStore` interface; Redis for cache via the `CacheStore` interface (ADR-017).

The application plane handles all business logic: repository metadata, users, organisations, pull requests, issues, permissions. It is a modular monolith — a single deployable unit internally, but with hard schema-domain boundaries enforced by a SQL linter in CI that rejects cross-domain joins and transactions.

**Schema domains (independently extractable later):**
- `identity`: users, organisations, agent identities, OAuth apps
- `repositories`: repo metadata, permissions, topics
- `collaboration`: pull requests, reviews, issues, comments
- `ci`: workflow runs, job logs, runner assignments
- `billing`: quota accounts, usage events, invoices

**Why PostgreSQL.** Mature SQL semantics, serializable transactions, foreign keys, deep operational tooling. Hot-table contention is managed by hash-partitioning on `repo_id` / `org_id` and routing read traffic to replicas via PgBouncer. Access goes through the `MetadataStore` Go interface in `plane/data/store/` (ADR-017); alternative engines slot in by satisfying the same compliance suite.

**Write path — transactional outbox.** Every state-mutating SQL transaction writes both the domain change and a corresponding row into a per-domain `outbox` table within the same PostgreSQL transaction. The transaction either commits both atomically or rolls back both. The caller's response is acknowledged on commit — it does *not* wait for Kafka publication.

A **polling-based outbox consumer** drains each outbox table: an advisory-locked `SELECT ... WHERE processed = false ORDER BY created_at LIMIT N` loop publishes batches to Kafka with idempotent producer config and marks rows processed in a follow-up update. Poll interval defaults to 1 second; outbox rows TTL-expire 24 h after the consumer high-water mark advances past them. Consumers are required to be idempotent on `event_id` (UUID, generated in the writing transaction) and tolerant of out-of-order delivery within a `repo_id`/`org_id` partition.

This avoids the dual-write problem entirely: there is no scenario where the database commits but the event is lost, and no scenario where the event publishes but the database rolls back. Logical replication or engine-native CDC is the upgrade path if poll latency becomes a constraint, swappable behind the same `EventQueue` interface. Detailed in ADR-008.

### 3.4 Workflow plane

**Technology:** Temporal for orchestration; Firecracker microVMs for CI isolation; custom PR scoring service.

#### 3.4.1 CI execution model

Two-tier compute pool:

| Tier | Cold start | Cost | Use case |
|---|---|---|---|
| Hot pool | < 1 second | Higher | Human-triggered CI, interactive agent tasks |
| Cold pool | < 30 seconds | 60–70% lower | Agent batch jobs, scheduled tasks, low-priority CI |

Agents are assigned cold pool by default. A job can request hot pool with an explicit annotation, subject to quota. This single decision dramatically reduces the compute cost of agent-driven CI.

Every CI job runs in a Firecracker microVM: hardware-isolated, sub-second boot, destroyed after job completion. No shared kernel with neighbouring jobs. Containers share too much kernel surface area for untrusted agent-generated code (ADR-002).

#### 3.4.2 Agent session orchestration (Temporal)

```
AgentSessionWorkflow
  ├── PlanActivity        (cheap, fast — just persists the plan)
  ├── ApprovalActivity    (waits for human or policy signal)
  ├── ExecutionActivity[] (one per agent sub-task, parallelisable)
  │     ├── ToolCallActivity (metered, checkpointed every 60s)
  │     └── CommitActivity   (triggers Git push flow)
  └── CompletionActivity  (scores output, files PR, notifies)
```

Temporal handles retries, timeouts, and checkpointing. A session that loses its compute node resumes from the last checkpoint on a new node without losing work.

#### 3.4.3 PR noise filtering

Every agent-created PR passes through a scoring pipeline before entering the human review queue:

1. **Semantic deduplication.** Embedding similarity against open PRs in the same repo (Qdrant); if cosine similarity > 0.92, auto-close as duplicate.
2. **Quality signals.** Test coverage delta, lint pass/fail, size of diff, commit message coherence.
3. **Agent reputation score.** Historical merge rate for this agent identity on this repo.
4. **Combined score → routing.** High → human queue, medium → auto-label and deprioritise, low → auto-close with explanation.

The detailed pipeline diagram lives in [`architecture.md`](architecture.md) §2.6.

#### 3.4.4 Issue noise filtering

Issues are not symmetric to PRs (no diff, no CI delta, no merge rate), but the pipeline shape is similar.

Every agent-created issue (and every issue *comment* posted by an agent) passes through:

1. **Semantic deduplication.** Embed `title + body` and ANN-search against open and recently-closed (≤ 90 days) issues in the same repo. Cosine similarity > 0.95 → auto-close-as-duplicate with a back-link comment to the canonical issue. Threshold tighter than PRs (0.95 vs. 0.92) because issue text is far shorter and semantic collisions are more likely to be coincidental.
2. **Reproduction signal.** Heuristic: presence of stack trace, version string, environment block, code-fence reproduction. Issues that match repo-author-defined `## Issue Template` requirements (parsed from the repo's issue-template directory) carry a positive signal; issues that do not match get a `needs-info` auto-label.
3. **Spam / off-topic classification.** Lightweight rule-based classifier first (regex for known spam patterns, link-only bodies, advertising language); ML classifier is the Enterprise/Cloud premium signal — same `Scorer` interface, different model.
4. **Agent reputation score.** Historical issue-quality score for this agent identity on this repo, weighted by close-as-resolved vs. close-as-spam ratio. New agent identities start at the org's median; reputation moves on outcome.
5. **Composite score → routing.**

| Score | Default routing | Visible to maintainer queue? |
|---|---|---|
| ≥ 0.7 | Human queue, normal priority; CODEOWNERS notified | Yes |
| 0.4–0.7 | Human queue, low priority; auto-label `ai-generated` | Yes (filtered by default) |
| 0.2–0.4 | Auto-label `ai-generated needs-triage`; no notification | Yes (filtered by default) |
| < 0.2 | Auto-close with explanation; reputation_score decremented | No |

Auto-close is reversible by any human user with triage permission; a re-opened auto-closed issue raises a `false-positive` reputation-feedback signal that adjusts the threshold for that agent on that repo over time.

#### 3.4.5 Maintainer queue UX

The maintainer review queue is a first-class platform UI (and API) that aggregates PRs and issues across all repositories the maintainer has review authority on. It is *not* a filter view over the existing repo-by-repo lists; it is a queue with its own model:

- **Queue items** are PRs and issues with positive composite score. One queue per maintainer, materialised view rebuilt on score changes
- **Default ordering:** `score × recency × repo_priority`, with repo_priority configurable per maintainer
- **Per-day capacity hint.** Each maintainer sets a soft daily review budget. When the queue would exceed this budget, additional items are bucketed into "tomorrow"; the platform measures actual vs. budget weekly
- **Bulk-review affordances.** Items with the same authoring-agent identity can be expanded inline; bulk-close, bulk-label, and bulk-comment operations route through the platform with a single approval (and a single audit row)
- **Reviewer pairing.** When a CODEOWNERS rule resolves to a group, the queue load-balances rather than fanning out — a PR is offered to one human first; if not picked up within `pickup_window` (default 24 h), it offers to the next; cycle ends with a configurable fallback
- **Burnout signals.** A maintainer whose review latency climbs above their 30-day baseline gets a private nudge. Maintainers who consistently exceed their declared capacity for 14+ consecutive days are flagged to org admins as a reviewer-capacity risk

**Reviewer capacity model.** When agent-PR submission rate exceeds capacity for 7+ consecutive days, the platform surfaces three remediation options to the org admin:

1. **Tighten reputation thresholds** — raise the score floor that promotes a PR into the queue
2. **Add reviewers** — invite + onboard additional CODEOWNERS, with a reviewer-capacity dashboard showing the projected effect
3. **Throttle agent submissions** — per-agent-per-repo rate limit on PR creation; agent receives a structured "queue at capacity, retry after T" response (a quota, not a denial)

#### 3.4.6 Branch protection and merge queue interaction

PR scoring sits *upstream* of branch protection and merge queue, not in place of them.

- **Branch protection rules** apply to all PRs equally — agent-authored or not. The PR scoring pipeline cannot bypass branch protection
- **Score interacts with required-approver count.** Branch protection rules can specify "require N human approvals where N depends on PR composite score". A PR with score ≥ 0.85 may require 1 approval; 0.7–0.85 may require 2; < 0.7 cannot be merged at all. Per-repo opt-in
- **Merge queue ordering.** When a repo uses the merge queue (serialised merge to main with re-test), agent PRs are queued separately from human PRs by default. Agent queue runs at lower priority during peak human-review hours and at higher priority during off-hours
- **Mergeability re-evaluation.** When a PR's score is updated, branch protection re-evaluates eligibility. A PR that drops below the merge floor mid-queue is ejected with a comment; the agent receives a structured rejection with the failed signal called out
- **Pre-receive hook integration.** AGENTS.md `Never` predicates (§9.1.4) and Policy `deny` rules (§3.7.1) are enforced in the **pre-receive hook**, ahead of branch protection. Branch protection sees only the PRs that survive both filters

Scoring is an **input** to branch protection's decision; the merge queue is the **execution** of that decision.

### 3.5 Observability

**Technology:** OpenTelemetry (traces + metrics + logs) → ClickHouse for traces/logs, Prometheus + Grafana for metrics.

Every span carries: `principal_id`, `principal_type` (human/agent), `org_id`, `repo_id`, `tokens_consumed`. This makes it possible to answer "which agent caused this incident" in seconds and attribute cost to the correct quota account without a separate analytics join.

Key dashboards:
- Per-principal token burn rate (real-time, 5s resolution)
- Storage tier migration lag (how much hot data is pending cold-tier flush)
- CI queue depth by tier (hot vs. cold, human vs. agent)
- PR noise ratio per organisation (agent PRs / total PRs)

### 3.6 Metering

Metering is the platform's load-bearing primitive for quota enforcement, usage attribution, and audit. Wrong counts produce wrong decisions and wrong reports. The design separates **enforcement metering** (sub-second, decision-grade) from **analytics metering** (5-second flush, accuracy-grade) and reconciles them on a defined schedule. Bound by ADR-012.

#### 3.6.1 What is metered

| Surface | Cost vector(s) | Capture point |
|---|---|---|
| API request | Request count; request CPU-ms; LLM token count if inference is invoked | Envoy WASM filter |
| Git RPC: clone / fetch | Bytes served from cache; bytes generated from object store; pack-objects CPU-ms; round-trips | Gitaly `pack_objects` hook (§3.6.4) |
| Git RPC: push | Bytes received; ref-update count; pre-receive hook CPU-ms | Gitaly `pre-receive` hook |
| LFS transfer | Bytes in/out; cold-tier reconstruct cost (data shards fetched) | Git proxy |
| CI minutes | Wall-clock seconds × tier multiplier (hot=1.0, cold=0.4); egress bytes | Firecracker runner controller |
| Agent session | Tokens consumed (in + out, per model); checkpoint storage seconds | MCP server + Temporal worker |
| Storage | Hot-tier GB-hours (NVMe); cold-tier GB-hours (EC, post-overhead); operations/sec | Storage migration service + object store inventory |

#### 3.6.2 Two-tier counter model

**Enforcement counter (Redis).**
- Sub-second, durable across Redis replicas via AOF
- Increments on every metered event in the request path
- Authoritative for "is this principal over quota right now?" — enforcement (deny / throttle / 429) reads only this counter
- Resets per quota window (rolling weekly for tokens, calendar-monthly for compute)

**Analytics counter (ClickHouse).**
- Events flushed from Envoy / Gitaly / runners every 5 seconds in batches
- Authoritative for usage reports and audit
- Per-event rows include: `event_id`, `principal_id`, `org_id`, `repo_id`, `surface`, `cost_vector`, `value`, `ts`
- Idempotent on `event_id` so flush retries do not double-count

**Reconciliation.** A nightly job re-aggregates ClickHouse rows into per-principal totals and compares against Redis end-of-day snapshots. Any drift > 0.5% triggers an investigation alert. Drift < 0.5% is folded into the next-window enforcement counter so cumulative drift cannot exceed 0.5% in any 24-hour period.

#### 3.6.3 Charge semantics for Git operations

| Operation | Charged on | Rationale |
|---|---|---|
| `git clone` (full) | Bytes served, packfile build CPU-ms | Captures both cold-tier reconstruct cost and CPU spent in pack-objects |
| `git fetch` (incremental) | Bytes served (delta only) | Don't charge for objects the client already has |
| `git fetch` (resumed / partial) | Bytes served in this transfer only | Each resume is metered as a fresh transfer; idempotency on retry below |
| `git push` | Bytes received + ref-update count | Both bandwidth and metadata-write cost |
| Cache-hit fetch (warm CDN) | Bytes served at reduced multiplier (0.1×) | Reflects lower marginal cost; incentivises shallow-clone discipline |
| Cold-tier reconstruct | Multiplier 1.5× on bytes served | Reflects higher actual cost (10 shard fetches + reconstruct CPU) |
| Failed transfer (TCP reset, client abort) | Charged for bytes-served-so-far only | No retroactive refund; matches actual platform cost |
| Retry of a failed transfer | Fresh charge with a `retry_of` field linking to prior `event_id` | Operator-side abuse heuristics can detect retry storms |

**Idempotency on retry.** Each Git operation is assigned a `transfer_id` at the proxy on first byte. Client retries with the same `transfer_id` (within 5 minutes, same principal, same repo, same op-type) reuse the partial-transfer counters rather than starting a fresh charge. After 5 minutes or on `transfer_id` mismatch, the retry is metered as a new operation.

#### 3.6.4 Gitaly hook integration

GitScale extends Gitaly's existing instrumentation rather than building a parallel meter:

- `gitaly_pack_objects_generated_bytes_total` — already exists; tagged with `principal_id` / `repo_id` via the JWT-SVID claims surfaced into the gRPC context
- `gitaly_pack_objects_served_bytes_total` — already exists; cache-hit path
- New: `gitscale_pack_objects_cold_reconstruct_bytes_total` — bytes pulled through cold-tier reconstruction; multiplier 1.5× in the cost model
- New: `gitscale_transfer_id` log field — emitted on every pack-objects span; flows through to the analytics row in ClickHouse for retry reconciliation

### 3.7 Approval and policy enforcement

**Why this is its own component.** The plan/execute split (§2.3) is load-bearing for the agent-as-first-class-principal claim — but a "human approves before execution" requirement is hollow without a mechanism that defines *who* approves, *what* needs approval, *how long* an approval is valid, *what happens* when no human is reachable, and *how* every decision is auditable. AGENTS.md's `Ask first` (§9.1.1) is the surface-level signal; this section is the platform-side enforcement model. ADR-015 records the decision.

**Trust boundary.** Approval and policy decisions are made *outside* the agent's runtime — by the platform, against signed Policy objects — and bound to the Temporal workflow at `ApprovalActivity` time. An agent SDK that ignores `Ask first` cannot bypass enforcement: the underlying tool calls (`ci_trigger`, `pr_merge`, push to a protected branch) gate at the platform's MCP middleware and the Git pre-receive hook, not in the SDK.

#### 3.7.1 Policy object model

A **Policy** is a versioned, immutable, signed document attached to an org or repo:

```
Policy {
  id: uuid
  version: monotonically-increasing int
  scope: { org_id, repo_id?, path_glob? }
  rules: Rule[]
  approvers: ApproverGroup[]
  escalation: EscalationLadder
  effective_from: timestamp
  signed_by: principal_id  // org admin
  signature: ed25519
}

Rule {
  predicate: <one of the recognized predicates below>
  decision: "allow" | "deny" | "require_approval"
  approval_class: "self_serve" | "single_approver" | "two_party" | "security_review"
  expiry_seconds: int        // approval validity window
  reason_required: bool
}

ApproverGroup {
  name: string               // e.g., "release_managers"
  members: principal_id[]    // human principals only
  required_count: int        // for two_party, this is 2
}

EscalationLadder {
  steps: [
    { wait_seconds: int, notify: ApproverGroup, action: "notify" | "auto_deny" | "fall_back_to_next" }
  ]
}
```

**Recognized risky-action predicates.** Free-form rules are surfaced to humans but not mechanically enforced.

| Predicate | Example | Enforcement point |
|---|---|---|
| `pr_merge:<branch_pattern>` | `pr_merge:main` | Application plane PR merge handler |
| `force_push:<branch_pattern>` | `force_push:*` | Git pre-receive hook |
| `secret_rotation:<scope>` | `secret_rotation:org_master_key` | Vault integration |
| `production_deploy` | (any tool that calls a registered deploy webhook) | MCP middleware; webhook validation |
| `cross_repo_modification:N` | (a single agent session touching > N repos) | Workflow plane session monitor |
| `package_publish:<registry>` | `package_publish:npm` | CI step that calls `npm publish` |
| `network_egress:<host_pattern>` | `network_egress:*.internal.corp` | Firecracker egress allow-list |
| `grant_permission:<scope>` | (any IAM-mutating tool call) | Identity domain |
| `bulk_action:<count>` | `bulk_action:100` (e.g., bulk-close 100 issues) | Application plane bulk handlers |
| `agents_md_violation:<predicate>` | (linked to AGENTS.md `Never` parse) | Per the AGENTS.md predicate (§9.1.4) |

The **default policy** for every new org includes `pr_merge:protected_branches` requiring `single_approver` and `force_push:*` requiring `two_party`.

#### 3.7.2 Who approves

Approval is restricted to **HumanUser** principals — agents may *propose* and *prepare* for an action but never approve a `require_approval` rule for another agent's action. Audit records reflect the approving human, not the agent that requested approval.

For agent-on-agent workflows, the approver is the **agent's owning human** by default, with optional escalation to an `ApproverGroup`. The owning human can delegate to a group via signed delegation.

#### 3.7.3 Approval lifecycle

```
Agent submits plan
  → Workflow Plane: PlanActivity stores plan
  → ApprovalActivity opens
     ├── Resolve effective Policy (org → repo → path)
     ├── Run plan through Rule predicates → produce required-approval set
     ├── If empty: auto-approve, proceed to ExecutionActivity
     ├── Else: emit approval.requested events (Kafka) to relevant ApproverGroups
     ├── Wait, with EscalationLadder timer running
     │   ├── On approve(s) sufficient for required_count: proceed
     │   ├── On deny: workflow terminates with deny reason
     │   └── On escalation tick: notify next group / auto_deny / fall_back
     └── Approval records persisted with: policy_id, policy_version, approver_id,
         approver_decision, reason, granted_at, expires_at
```

**Approval expiry.** Approvals are bound to the *plan* they approved. A workflow paused longer than `expiry_seconds` re-opens the ApprovalActivity on resume — stale approvals are not valid. Default 24 h; security-review class 4 h.

**Approval mutability.** A plan that materially changes after approval (the executor adds a new repository, exceeds a `bulk_action:N` threshold, or hits a not-previously-evaluated predicate) re-opens approval. Enforced via a **plan hash** carried into ExecutionActivity; mismatch with the approved hash forces re-approval.

#### 3.7.4 Audit semantics

Every `approval.*` event flows through the outbox + polling-consumer pipeline (§3.3) and lands in the audit log. Each approval record is immutable and cryptographically chained to the previous record for the same Policy (Merkle-style hash chain), so retroactive tampering is detectable. Audit retention is per-org configurable; the default is 7 years for compliance-driven deployments. Operators can stream the audit log to an external destination.

```
{
  event_type: "approval.requested" | "approval.granted" | "approval.denied"
            | "approval.expired" | "approval.escalated" | "approval.delegated",
  policy_id: uuid, policy_version: int,
  workflow_id: uuid, plan_hash: bytes,
  requesting_agent_id: uuid, approver_id: uuid?,
  rule_predicates: string[],
  reason: string?,
  decided_at: timestamp,
  parent_event_id: uuid  // hash chain back-pointer
}
```

#### 3.7.5 Escalation and reachability

If no approver in the configured ApproverGroup is reachable within the ladder's wait window, the platform follows the declared `action`:

- `notify` — page the next group; do not yet block or proceed
- `auto_deny` — terminate the workflow; agent receives a structured denial
- `fall_back_to_next` — try the next group in the ladder

The default ladder for production-class predicates is `[5min: notify on-call, 30min: notify on-call manager, 2h: auto_deny]`. The default for self-serve predicates is `[24h: notify, 72h: auto_deny]`. There is **no platform-default `auto_approve` step**.

#### 3.7.6 Policy versioning and rollout

Policies are immutable; a "change" produces a new version. New versions become effective at `effective_from` and apply to plans submitted after that timestamp. In-flight workflows continue to evaluate against the policy version captured in their ApprovalActivity. A **dry-run** mode lets admins evaluate a draft policy against the last 7 days of plans without enforcing.

#### 3.7.7 Relationship to AGENTS.md

AGENTS.md (§9) is the **per-repo, customer-authored** layer. The Policy object is the **org-level, admin-signed** layer. They compose with Policy as scope (1) (org-level). Where they conflict, the higher-precedence layer wins; where they agree, the agent must pass both.

#### 3.7.8 Out of scope

- Real-time human-in-the-loop coding sessions (an agent waiting on every keystroke). Approval is plan-level.
- Probabilistic risk scoring. "Risky" is defined by predicate match, not by an ML model. ADR-015 explicitly defers ML-based risk classification.
- Approver liveness probing. The platform notifies; integrations carry liveness-aware escalation.

---

## 4. Data design

### 4.1 Storage tiering policy

| Condition | Tier | Storage | Replication |
|---|---|---|---|
| Repository active in last 7 days | Hot | Local NVMe | 3× full replica |
| Repository active in last 30 days | Warm | SSD object store | 3× full replica |
| Repository inactive > 30 days | Cold | HDD object store | (10,4) erasure code |
| LFS objects (all ages) | Cold | HDD object store | (10,4) erasure code |
| CI job logs > 90 days | Archive | Glacier-tier | (17,3) erasure code |

Tiering is driven by a background process that runs per-repository GC every 24 hours and emits migration jobs to a Kafka topic consumed by the storage migration service.

### 4.2 Identity model

```
Principal
  ├── HumanUser
  │     ├── id, email, created_at
  │     ├── rate_limit_bucket: "human_default"
  │     └── quota_account_id → BillingAccount
  │
  └── AgentIdentity
        ├── id, display_name, created_at
        ├── parent_user_id (owning human)
        ├── permission_scope[] (subset of owner's permissions)
        ├── rate_limit_bucket: "agent_{tier}"
        ├── quota_account_id → BillingAccount (can be separate)
        ├── session_quota: max_parallel_sessions, max_tokens_per_week
        └── reputation_score: float (updated by completion pipeline)
```

An AgentIdentity can only have permissions that are a strict subset of its owning human's permissions. An agent cannot escalate privilege beyond what the human granted at identity creation time.

### 4.3 Event schema and delivery contract

All state changes publish to Kafka via the transactional outbox + polling-based outbox consumer pipeline (§3.3, ADR-008).

- **Atomicity:** The outbox row is written in the same transaction as the source state change. Either both commit or neither does.
- **Delivery:** At-least-once. Kafka idempotent-producer config provides exactly-once-into-Kafka, but consumers must still dedupe on `event_id` to handle replay during outbox-consumer restarts.
- **Ordering:** Kafka topics are partitioned by `repo_id` (for repo-scoped events) or `org_id` (for org-scoped events). Per-partition order matches commit order. Cross-partition order is undefined.
- **Latency:** Median outbox-to-Kafka latency target < 2s under nominal load; p99 < 5s. Outbox-consumer high-water lag is exposed as an SLO (`outbox_consumer_high_water_lag_seconds`).
- **Replay:** Consumers may rewind to any retained offset. Kafka retention is 7 days for stateless consumers; the audit-log consumer streams to ClickHouse with infinite retention.

Canonical envelope:

```json
{
  "event_id": "uuid",
  "event_type": "repository.push | pr.opened | ci.job_started | ...",
  "occurred_at": "ISO8601",
  "principal": { "id": "...", "type": "agent|human|system" },
  "org_id": "...",
  "repo_id": "...",
  "payload": { /* event-specific data */ },
  "tokens_consumed": 0,
  "compute_seconds": 0
}
```

Consumers never need to call back into the application plane to enrich events. All necessary context is in the envelope.

### 4.4 Cold-tier per-org isolation

Cold-tier object storage uses (10,4) Reed-Solomon EC to drive ~93% storage cost savings. A deployment may host multiple orgs, and per-org confidentiality, deletion, and retention must hold regardless. Naive content-addressed dedup across orgs is **explicitly rejected** because it enables a confirmation-of-file attack — an adversary who suspects an org holds a specific blob can confirm it by uploading the same content and observing dedup-driven storage savings or hash collisions.

The cold-tier design therefore uses **per-org encryption with scoped dedup** (ADR-011).

#### 4.4.1 Per-org key hierarchy

```
Org Master Key (HashiCorp Vault)
  ├── Repo Data Encryption Key (DEK)         ← rotated on org-master rotation
  │     └── per-object content key (HKDF(DEK, object_hash))
  └── Repo Tombstone Key (auxiliary, rotated separately)
```

- Each org has a master key in HashiCorp Vault
- Per-repo DEKs are derived from the org master and used to encrypt object content before EC sharding
- Object content keys are derived from `HKDF(DEK, object_hash_within_repo)` so dedup remains feasible *within an org's content* but is **structurally impossible across orgs** — different DEKs produce different ciphertexts for identical plaintext

#### 4.4.2 Dedup scope

| Scope | Dedup applies | Rationale |
|---|---|---|
| Within a single repo | Yes (full dedup) | Most fork-of-fork and history-rewrite scenarios are intra-repo |
| Within a single org, across repos | Yes, behind a feature flag | Captures the fork-within-org case; opt-in to manage confirmation-of-file risk |
| Across orgs | **Never** | Different DEKs; ciphertext is not comparable; no cross-org inference |

#### 4.4.3 EC stripe tenancy

Reed-Solomon (10,4) stripes are constructed from a **single org's** encrypted shards only. A stripe is never mixed across orgs.

- A stripe with insufficient bytes from one org is padded with that org's other objects (still deduped within scope) or zero-padded with a stored padding-length attribute
- Storage overhead increases marginally for very small orgs; this is acceptable cost-of-isolation
- Stripe metadata records `org_id`; the placement layer rejects any operation that would mix orgs in one stripe

#### 4.4.4 Deletion, retention, legal hold

- **Per-object deletion** removes the encrypted shards (or marks them tombstoned for GC). A separate fast path is **crypto-shredding** the per-repo DEK, which logically deletes all objects encrypted under that DEK in O(1) — this is the deletion path used for whole-repo deletion, account closure, and right-to-erasure requests
- **Legal hold** sets a `hold_until` attribute on the repo's metadata; the cold-tier GC and crypto-shred paths refuse to act on holds. Hold removal is dual-control (security + legal sign-off) and audited
- **Retention windows** are encoded per-org and per-repo; the GC scheduler reads them on every pass. Retention always wins over deletion requests except where compelled by the operator's policy
- **Tombstone propagation** uses the outbox + polling-consumer pipeline (§4.3) so deletion is asynchronously consistent with audit, usage, and search-index purges

---

## 5. API design

### 5.1 Git protocol

Standard Git wire protocol over HTTPS and SSH. No proprietary extensions. Standard Git clients work without modification.

### 5.2 REST API

REST API surface for ecosystem compatibility. Versioned at `/v1`; semver. Additions for agent-specific resources:

```
POST   /v1/agents                          # Create agent identity
GET    /v1/agents/{agent_id}               # Get agent details + quota status
POST   /v1/agents/{agent_id}/sessions      # Create agent session (submit plan)
GET    /v1/agents/{agent_id}/sessions/{id} # Get session status + checkpoint
DELETE /v1/agents/{agent_id}/sessions/{id} # Cancel session

GET    /v1/orgs/{org}/quota                # Current quota consumption
GET    /v1/orgs/{org}/usage                # Usage breakdown by principal
```

### 5.3 MCP interface

A native MCP server exposes the following tool set to agent clients:

```
git_clone, git_push, git_diff, git_log         # Core Git operations
pr_create, pr_list, pr_review, pr_merge        # PR lifecycle
issue_create, issue_list, issue_comment        # Issue lifecycle
ci_trigger, ci_status, ci_logs                 # CI operations
search_code, search_issues, search_semantic    # Search (text + vector)
quota_status, session_checkpoint               # Agent self-management
agents_md_get, agents_md_lint,
agents_md_effective_policy                     # Repo-conventions surfacing
```

### 5.4 GraphQL API

REST is documented above; this section specifies the GraphQL surface and the boundary at which it does (and does not) match an industry baseline. ADR-013 records the decision to ship GraphQL with a deliberately narrow compatibility scope rather than chase line-for-line parity.

#### 5.4.1 Compatibility boundary

GitScale's GraphQL is **schema-shape compatible** for the named query objects below. The compatibility contract:

| Layer | Compatibility level | What this means in practice |
|---|---|---|
| Top-level query roots (`viewer`, `repository`, `organization`, `user`, `node`, `nodes`) | **Full** | Field names and nullability match the industry baseline for the listed objects; clients that issue these queries work without modification |
| Repository, PullRequest, Issue, Discussion, User, Organization, Team, Commit, Ref types | **Field-stable subset** | The fields a typical client uses (id, name, owner, default_branch_ref, pull_requests connection, issues connection, comments connection, reviewers, mergeable state, ...) are present with matching names and types; uncommonly-used fields may be absent at GA and added on demand |
| Pagination | **Full Relay-style** | `first`/`after`/`last`/`before` cursors; `pageInfo`; `edges`/`node` shapes |
| Mutations | **Subset** | The mutations needed for typical agent and CI workflows (createPullRequest, mergePullRequest, addComment, createIssue, addReaction, createRef, updateRef, deleteRef, addLabelsToLabelable, ...) ship at GA. Missing mutations return a structured `NOT_IMPLEMENTED` error with the REST-equivalent endpoint suggested |
| Agent-native types | **GitScale-only** | `AgentIdentity`, `AgentSession`, `AgentApprovalRequest`, `Policy`, `MeteringEvent` types are GitScale extensions; tooled with the `@gitscale` directive so clients can detect them at introspection time |
| Schema directives, custom scalars (URI, GitObjectID, DateTime, HTML) | **Full** | Match the industry-baseline directive and scalar definitions for the listed types |

What this **explicitly is not:**

- A bug-for-bug clone of any incumbent's deprecation history. The schema starts from the industry baseline as of October 2025 and forks forward; deprecated fields are not carried unless they remain in active use
- Schema-stable across versions of any incumbent. Field additions are evaluated case-by-case
- A guarantee that arbitrary clients targeting the baseline work end-to-end without testing — only that the *named-subset* queries work

#### 5.4.2 Auth model

- Same SPIFFE JWT-SVID-on-the-wire as REST (§6.1, ADR-010). The principal claim is the agent or human; rate limits and quota apply identically
- Token grants required: `repo`, `read:org`, `read:user`, `write:repo`, `write:agent`, `admin:agent`. Scopes are checked **per resolver**, not per request — an unauthorized resolver returns `null` with a `FORBIDDEN` error in `errors[]`, so a partial query result is preserved
- All requests pass through Envoy edge metering (§3.6), so GraphQL counts toward the same per-principal budget as REST
- Personal access tokens, OAuth app installations, and fine-grained tokens map to GraphQL exactly as for REST

#### 5.4.3 Performance and cost model

GraphQL's "ask for everything in one request" affordance is the load-bearing capacity risk. Mitigations:

- **Query cost analysis** before execution. Each schema field has a static cost (e.g., `repository(name)` = 1, a `pull_requests(first: N)` connection = N + sub-cost). Total cost computed before any resolver runs; queries above the cost ceiling for the principal are rejected with `MAX_QUERY_COST_EXCEEDED`. Per-principal hourly ceilings are operator-configurable
- **Persisted-query support** (Apollo-APQ-compatible). Clients post a query hash; if the server has the registered query, it runs without re-parsing. Persisted queries unlock a 0.5× cost multiplier
- **Resolver-level concurrency cap.** DataLoader-style batcher fans out per-resolver fetches; each principal has a max-in-flight-resolvers cap (operator-configurable; default 32)
- **Dedicated read replicas.** GraphQL queries route to PostgreSQL read replicas by default. Mutations and queries that explicitly request `consistency: STRONG` route to the primary
- **Connection limit.** Maximum connection nesting depth = 10; maximum aliases per query = 50. Above this, requests are rejected at parse time
- **Per-org global timeout.** A single GraphQL query has a hard 30-second wall-clock cap; nested resolvers that would exceed return partial results with a `TIMEOUT` error

#### 5.4.4 Schema evolution

- Schema is published at `schema.graphql` in each release
- Additive changes (new fields, new mutations, new types) are non-breaking; deprecated fields carry a `@deprecated` directive with a 12-month sunset. Removal requires a 12-month notice and a `Sunset` HTTP header on responses returning the field

#### 5.4.5 Operational surface

- Single endpoint: `POST /graphql` (HTTPS only)
- Introspection enabled; rate-limited the same as queries
- Tracing: each resolver emits an OpenTelemetry span tagged with `principal_id`, `resolver_path`, `cost`. Per-principal slow-resolver dashboards are part of the standard observability surface
- The GraphQL playground at `/graphql/explorer` is available for HumanUser principals only; agent principals get a 403 with a pointer to the documented schema URL

#### 5.4.6 Surface coverage matrix

| Surface | Audience | Strength | Lifecycle binding |
|---|---|---|---|
| Git wire protocol (§5.1) | Git clients (human + agent) | Pack negotiation, ref updates | Standard, no GitScale extensions |
| REST (§5.2) | Tooling, CI, simple integrations | Stable URL paths, easy to cache, easy to webhook | Versioned at `/v1`; semver |
| GraphQL (§5.4) | Apps that need composite reads | Composite query in one round trip; introspectable | Schema-versioned; persisted-query path |
| MCP (§5.3) | Agents | Tool-shaped surface; binds to AGENTS.md and policy enforcement (ADR-007) | Versioned per the MCP protocol spec |

The four surfaces are coordinate, not redundant: an MCP `pr_create` call lands in the same application-plane handler as a REST POST or a GraphQL mutation, and all three pass through the same metering, policy enforcement, and outbox-event emission.

---

## 6. Security design

### 6.1 Zero-trust networking and identity propagation

- All service-to-service communication uses mTLS with X.509-SVID workload certificates issued by SPIRE
- Principal identity is propagated end-to-end as a **JWT-SVID** (RFC 7519, signed by the SPIRE trust domain CA) carried in an `Authorization: Bearer ...` header. Claims:
  - `principal_id`, `principal_type` (human|agent|ci|service)
  - `org_id`, `rate_bucket`, `quota_account_id`
  - `iat` (issued-at), `exp` (issued lifetime: 60 seconds)
  - `aud` (target service or service-set, narrowing what the token can be used for)
- Every service verifies the JWT-SVID signature and `aud`/`exp` claims on every request before honoring identity claims. The token is scoped per request so even a compromised peer cannot replay it cross-service or after expiry. Network-perimeter trust is defense-in-depth, not the primary mechanism.
- Network policies default to deny-all; services declare explicit ingress/egress rules. SPIRE workload-attestation pins service identity to the runtime workload, so a stolen X.509-SVID cannot be used outside its attested workload.
- Token issuance is rate-limited at Envoy to prevent token-mint abuse if Envoy itself is compromised; the SPIRE server enforces the upper bound on issued JWT-SVIDs/sec/identity.

### 6.2 Secrets management

- No secrets in environment variables or config files
- All secrets fetched from Vault at runtime; short-lived leases rotated automatically
- CI jobs receive ephemeral credentials scoped to the specific repository and job duration

### 6.3 Agent sandboxing

- Agent-generated code runs in Firecracker microVMs: no shared kernel, no shared filesystem, hardware-isolated
- Network egress from agent CI jobs is restricted to declared allow-list; no arbitrary outbound connections
- All agent file system writes are journaled; a session can be audited or rolled back completely

---

## 7. Plugin interfaces

GitScale exposes a small set of Go interfaces as public API contracts. Reference implementations ship in-tree; out-of-tree implementations attach without core changes. ADR-014 records the governance.

| Interface | Reference implementation | Purpose |
|---|---|---|
| `Scorer` | Rule-based composite score | PR / issue scoring pipeline |
| `ReputationProvider` | Per-org local reputation | Agent reputation lookup and update |
| `MeteringSink` | Direct ClickHouse write | Analytics-counter sink |
| `IdentityProvider` | Local DB + OAuth | Principal authentication |
| `SignalSource` | Built-in static rules | Quality-signal feed for the scoring pipeline |
| `TieringPolicy` | Time-based (active-in-last-N-days) | Hot/warm/cold migration decisions |
| `ReplicationCoordinator` | Single-region 3× replica | Hot-tier replica placement and quorum |

### 7.1 Versioning

- Semver per interface (`gitscale.dev/plugins/scorer/v2`). Major bumps are not stacked across interfaces
- 12-month deprecation window. A deprecated version remains supported for at least 12 months from announcement, regardless of when the next `MAJOR` ships
- `MAJOR` bumps require an RFC. Load-bearing interfaces (`IdentityProvider`, `MeteringSink`, `ReplicationCoordinator`) require a higher review bar than policy interfaces (`Scorer`, `ReputationProvider`, `SignalSource`, `TieringPolicy`)
- A reference plugin set runs against every release candidate; failing the matrix blocks the release

### 7.2 Sandbox by input trust

| Plugin tier | Where it runs | Why this tier |
|---|---|---|
| `IdentityProvider`, `ReplicationCoordinator` | **In-process** | Performance-critical hot path; operator-installed only |
| `MeteringSink`, `TieringPolicy` | **In-process with capability scoping** | Performance-critical write-side; scoped capability handle (no general DB access) |
| `Scorer`, `SignalSource`, `ReputationProvider` | **Sidecar, gRPC over UDS** | Handles untrusted input (PR text, external feeds, third-party signals); a buggy or malicious plugin must not have access to app-plane process memory |

Sidecar specifics: separate process, sandboxed filesystem, no network egress except to declared allow-list, dropped capabilities, 50ms default timeout per call, 512 MiB / 0.5 CPU caps, mTLS over UDS using SPIFFE workload identity. A sidecar OOM or segfault is logged, the call returns a structured error, and the app plane continues serving.

### 7.3 Distribution

- Reference plugins are vendored in the source tree
- Out-of-tree plugins are distributed as **signed container images** (cosign + transparency log entry); the loader verifies the signature against the operator's trust bundle before loading
- Plugin binaries carry a manifest declaring: interface version required, capability declarations (read DB? write DB? network egress?), and resource caps. The loader enforces the manifest

---

## 8. Open design questions

| Question | Owner | Target Date |
|---|---|---|
| Erasure coding library selection: ISA-L vs. Reed-Solomon Go | Storage | June 2026 |
| MCP server protocol version target | Platform API | July 2026 |
| Reputation score model: rule-based vs. ML-based | ML Platform | July 2026 |
| Cross-org dedup feature-flag default | Security / Storage | August 2026 |
| AGENTS.md schema versioning: track upstream convention or pin a GitScale profile? | Platform API | July 2026 |

---

## 9. Repository conventions

### 9.1 AGENTS.md

`AGENTS.md` is the de facto convention for repository-level agent instructions. GitScale supports it natively from launch as a **first-class repository metadata file** with platform behavior tied to its presence.

#### 9.1.1 File format

Plain Markdown. No JSON, no YAML, no schema validation. The file lives at the repository root; nested `AGENTS.md` files in subdirectories are recognized for scoped instructions and resolved by closest-ancestor at agent task time.

GitScale recognizes a small set of Markdown headings as **structured sections** for UI surfacing and policy enforcement:

| Heading | Behavior |
|---|---|
| `## Always` (or `## Always do`) | Surfaced to MCP clients via `agents_md_get` tool; informational |
| `## Ask first` (or `## Ask before`) | Surfaced; agent SDKs are expected to gate the listed actions on human approval |
| `## Never` (or `## Never do`) | **Enforced.** Listed actions become deny-rules in the agent's effective permission scope (§9.1.4) |
| `## Build` / `## Test` / `## Lint` | Captured into repo metadata as canonical command strings; CI defaults pull from these |
| `## Style` / `## Conventions` | Surfaced to agents; not enforced |

Enforcement on `Never` only — `Always` and `Ask first` encode preferences that legitimately vary by task.

#### 9.1.2 Precedence

Multiple sources may speak to the same agent action. Resolution order (highest wins):

1. Org-level Policy (admin-signed; ADR-015)
2. Repo-level `AGENTS.md` (root file)
3. Path-scoped `AGENTS.md` (closest ancestor of the file the agent is touching)
4. Branch-protection rules (CODEOWNERS-equivalent; orthogonal to AGENTS.md but stacked on top)
5. AgentIdentity `permission_scope` (subset of owning human's; always a hard ceiling)

The agent's effective permission for any action is the **intersection** of (1)–(5). An AGENTS.md cannot grant capabilities the AgentIdentity does not already have; it can only restrict further.

#### 9.1.3 MCP surfacing

```
agents_md_get(repo_id, path?)         # returns merged AGENTS.md content for a path
agents_md_lint(repo_id, content)      # validates structured sections; returns warnings
agents_md_effective_policy(repo_id, agent_id, path?)
                                       # returns the resolved deny/ask/allow set
```

Agent SDKs are expected to call `agents_md_effective_policy` at session start and re-fetch on path change. Result is cached for the session lifetime.

#### 9.1.4 Enforcement of `Never`

Items under `## Never` are parsed as a small set of recognized predicates (the parser is intentionally conservative; unrecognized lines are surfaced as warnings, not rules):

| Predicate | Example line | Enforcement point |
|---|---|---|
| `no-touch:<glob>` | `Never modify files in vendor/` | pre-receive hook; rejects push if changed paths match |
| `no-network:<host-pattern>` | `Never make network calls to *.internal` | CI Firecracker egress allow-list |
| `no-secret:<pattern>` | `Never commit credentials matching aws_*` | pre-receive hook + secret-scan |
| `no-action:<api>` | `Never call ci_trigger directly` | MCP server permission middleware |

Free-form `Never` lines that don't match a recognized predicate are surfaced to the agent and to human reviewers but not mechanically enforced. This avoids both the false-confidence trap (looks enforced, isn't) and the friction trap (every line requires a predicate).

#### 9.1.5 UI exposure

- Web UI renders AGENTS.md on the repo home page next to README, with `Always` / `Ask first` / `Never` highlighted
- PR view shows which AGENTS.md rules applied to the PR's authoring agent and whether any were violated (a violation is a hard block, not a warning)
- Agent reputation score (§3.4) decrements on violation events

GitScale-specific extensions to the AGENTS.md predicate vocabulary are flagged with a `<!-- gitscale: -->` HTML comment so they degrade gracefully on other platforms.

---

## 10. Cross-references

- [`architecture.md`](architecture.md) — system diagrams, ADRs, scalability and failure mode analysis, multi-region topology, operational design contracts
- [`CLAUDE.md`](../CLAUDE.md) — repo guidance, three core principles, technology stack, branch and commit conventions
- `.claude/skills/gitscale-*` — workspace skills that gate against design contradictions (ADR drift, plane boundary, outbox check, storage tier lint, agent quota check, event schema, issue-PR link, Go conventions, Temporal determinism, Firecracker isolation)
