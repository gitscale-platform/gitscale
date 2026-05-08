# GitScale Execution Plan — 2026-05-06

Post-Phase-1 closeout + workflow plane bootstrap + Phase-2 launch ramp.

## 1. Current state (snapshot)

**Merged & green** (Phase 1 epic #5, partial):

- All 5 PostgreSQL domain schemas: identity, repositories, collaboration, ci, billing (#19–#23)
- Polling outbox consumer with advisory-lock drain + Kafka publish (#32 / closes #11)
- Kafka topology (10 topics, 5 main + 5 DLQ) + EventEnvelope + `lint-events` (#31 / closes #12)
- Redis `CacheStore`, `RateLimiter`, namespace wrapper, typed helpers, compliance harnesses for cache + ratelimiter + eventqueue (#30 / closes #13)
- ADR-004 amended: partition key = `aggregate_id` (#29 / closes #26)
- ADR-009 amended: repo-location cache contract pinned (#36 / closes #17)
- Project scaffolding, Makefile, pre-commit lint hook, Go module, plane skeletons (#2, #16)

**Plane occupancy**:

- `plane/data/` — populated (cache, ratelimit, kafka, outbox, compliance, migrations).
- `plane/application/`, `plane/edge/`, `plane/git/`, `plane/workflow/` — `doc.go` only.

## 2. Open issues — full inventory

| # | Title | Plane | Pri | Type | Direct deps |
|---|---|---|---|---|---|
| #5 | Phase 1 epic | core | P1 | feature | closes when #14, #15, #35 merge |
| #14 | MetadataStore + Tx interfaces (`plane/data/store/`) | data | P1 | feature | none (compiles standalone; integration tests need #6, merged) |
| #15 | Identity domain service: HumanUser + AgentIdentity CRUD | application | P1 | feature | #14, #35, #6 (merged) |
| #18 | Temporal cron: `usage_events` partition rollover + retention | workflow | P2 | feature | #33; archive arm needs #34 |
| #27 | Identity cache invalidator consumer | application | P2 | feature | #11/#12/#13 (all merged) — **unblocked now** |
| #33 | Workflow plane bootstrap (Temporal worker, registry, determinism lint) | workflow | P1 | feature | none |
| #34 | ADR: billing `usage_events` archival tier + format | data | P2 | adr/decision-pending | none |
| #35 | MetadataStore compliance suite (`plane/data/compliance/metadatastore.go`) | data | P1 | feature | #14 |

No PRs currently open. Last merge: #36 on 2026-05-05.

## 3. Dependency graph

```
                 (merged: #6, #11, #12, #13)
                          │
                          ▼
                   ┌────────────┐
                   │    #14     │  MetadataStore + Tx
                   │   (Data)   │  *unblocker for #15, #35*
                   └─────┬──────┘
                         │
                         ▼
                   ┌────────────┐
                   │    #35     │  MetadataStore compliance suite
                   │   (Data)   │  ADR-017 obligation
                   └─────┬──────┘
                         │
                         ▼
                   ┌────────────┐
                   │    #15     │  Identity domain service
                   │ (Appl.)    │  closes #5 along with #14, #35
                   └────────────┘

   ┌────────────┐                   ┌────────────┐
   │    #33     │                   │    #34     │  ADR billing archival
   │ (Workflow) │  bootstrap        │ (decision) │  tier+format
   └─────┬──────┘                   └─────┬──────┘
         │                                │
         └──────────┬─────────────────────┘
                    ▼
             ┌────────────┐
             │    #18     │  Billing partition rollover
             │ (Workflow) │  archive arm needs #34
             └────────────┘

   ┌────────────┐
   │    #27     │  Identity cache invalidator — independent, fully unblocked
   │ (Appl.)    │
   └────────────┘
```

## 4. Wave-ordered execution plan

### Wave 0 — kicks off in parallel, no cross-deps

| Slot | Issue | Branch | Plane | Why parallel-safe |
|---|---|---|---|---|
| 0a | #14 | `feat/data-store-interfaces` | data | New package `plane/data/store/`; touches no existing files |
| 0b | #33 | `feat/workflow-plane-bootstrap` | workflow | New `cmd/workflow-worker/` + `plane/workflow/` (currently empty except `doc.go`) |
| 0c | #34 | `adr/data-billing-archival-tier` | data (docs) | Doc-only ADR; touches `docs/architecture.md §8` |
| 0d | #27 | `feat/application-identity-cache-invalidator` | application | New package; consumes existing `plane/data/kafka` + `plane/data/cache` interfaces |

**Wave 0 critical-path = max(0a, 0b)** — both are P1 and the longest tickets in this wave.

#34 is decision-only and short. #27 is small (one consumer + one integration test).

### Wave 1 — gated on Wave 0a (#14)

| Slot | Issue | Branch | Plane | Gating |
|---|---|---|---|---|
| 1a | #35 | `feat/data-metadatastore-compliance-suite` | data | needs `MetadataStore` symbols from #14 |

### Wave 2 — gated on Wave 1a (#35)

| Slot | Issue | Branch | Plane | Gating |
|---|---|---|---|---|
| 2a | #15 | `feat/application-identity-domain-service` | application | postgres impl must pass compliance suite before it ships per ADR-017 |

Once #15 merges, `#5` Phase-1 acceptance criteria are met → close epic.

### Wave 3 — gated on Wave 0b (#33) and Wave 0c (#34)

| Slot | Issue | Branch | Plane | Gating |
|---|---|---|---|---|
| 3a | #18 | `feat/workflow-billing-partition-rollover` | workflow | rollover arm needs #33; archive arm needs #34 ADR target |

#18 may ship in two parts:

- **#18-rollover** — only `CreatePartition` activity, depends on #33 only.
- **#18-archive** — adds `DetachAndArchivePartition`; depends on #34.

Recommend split if #34 ADR slips.

## 5. Critical path

`#14 → #35 → #15 → close #5`

Three sequential merges. Everything else is parallel cover.

## 6. Per-issue scope confirmation

### #14 — MetadataStore + Tx (P1, Data)

- `plane/data/store/{metadata.go, identity_reader.go, identity_writer.go, repository_reader.go, repository_writer.go}`
- `plane/data/store/postgres/metadata.go` — `*pgxpool.Pool` impl; `Transact` (serializable, surfaces `40001` to caller per ADR-006); `WriteOutbox` (5-domain allowlist, dispatches to `<domain>_outbox`).
- `plane/data/store/stub/metadata.go` — in-mem test double.
- **Drop** `EventQueue`. Issue body explicitly removes it; outbox consumer keeps using `plane/data/outbox.KafkaProducer` per ADR-008.
- Domain reader/writer bodies: `errNotImplemented` stubs except what #15 needs.
- Acceptance: `go build ./plane/data/store/...` clean; integration test against `identity_outbox` proves transactional WriteOutbox.

### #35 — MetadataStore compliance suite (P1, Data)

- `plane/data/compliance/metadatastore.go` exports `RunMetadataStoreCompliance(t, factory)` + `MetadataStoreFactory`.
- 7 test cases per issue body: Transact basics, WriteOutbox transactional invariant, domain allowlist, table dispatch, serializable retry contract (`40001` race), `event_id` uniqueness, domain reader stubs return sentinel.
- Wiring: `plane/data/store/postgres/compliance_test.go` (testcontainers) + `plane/data/store/stub/compliance_test.go` (skip 40001 race with explicit comment).
- Lands **before** #15 merges so the postgres impl is enforced from day one.

### #15 — Identity domain service (P1, Application)

- `plane/application/identity/{models.go, service.go, postgres_service.go}`.
- Fills `IdentityReader`/`IdentityWriter` bodies in `plane/data/store/postgres/`.
- `CreateUser` and `CreateAgent` MUST call `Transact` and `WriteOutbox` in the same Tx — emits `user.created` / `agent.created`.
- `UpdateAgentReputationScore` clamps to `[0.0, 1.0]`, emits `agent.reputation_updated`.
- Integration test: testcontainers PG; assert source row + outbox row in same Tx; rollback removes both.
- Closes Phase 1 epic.

### #33 — Workflow plane bootstrap (P1, Workflow)

- `cmd/workflow-worker/main.go` — Temporal worker, signal handling, structured logging, OTel.
- `plane/workflow/registry.go` — pluggable workflow + activity registration per task queue.
- Task queues: `billing-maintenance`, `agent-sessions` (placeholder), `ci-pipelines` (placeholder). Namespace: `gitscale`.
- `plane/workflow/adapters/data/` — interfaces wrapping `plane/data/store.MetadataStore` + `plane/data/cache.CacheStore`. Activities receive interfaces, never concretes. Plane boundary contract.
- **State-mutating activities** call `plane/application/<domain>` over HTTP/gRPC (so outbox transaction hits app plane). Read-only activities MAY use `plane/data/store` directly via the adapter.
- `plane/workflow/lint-determinism.sh` — greps for `time.Now()`, `rand.X`, `os.Getenv`, `range` over `map` in workflow files. Config + script in same commit (CLAUDE.md CI rule).
- One canary workflow + integration test against Temporal dev server.

### #18 — Billing partition rollover (P2, Workflow)

- `plane/workflow/billing/partition_rollover_workflow.go` — Temporal scheduled workflow, monthly cadence.
- Activities: `CreatePartition(year, month)` (idempotent), `DetachAndArchivePartition(year, month)` (target = #34 outcome).
- Roll forward: 7 days before EoM, create next month's partition. Retention: detach+archive partitions older than retention horizon (TBD — set by #34).
- Idempotent + portable across PG and CRDB (dialect SQL in adapter, not workflow).
- Recommend split: `#18-rollover` lands first (#33 only); `#18-archive` follows (#34).

### #27 — Identity cache invalidator (P2, Application)

- `plane/application/identity-cache-invalidator/` — small consumer service.
- Subscribes `gitscale.identity.events`, group `gitscale.identity-cache-invalidator`.
- Acts on: `user.disabled`, `user.deleted`, `agent.revoked`, `agent.deleted`, `org.member_removed`, `principal.permissions_changed` → `cache.Delete(IdentityKey<aggregate_id>)`.
- Idempotent on `event_id`. `auto.offset.reset=earliest`. Metrics: `identity_invalidations_total{event_type}`, `identity_invalidator_consumer_lag_seconds`.
- **Note**: #15 is what *emits* `user.created` / `agent.created`, but **revocation** events come later (no current emitter for `user.disabled` / `agent.revoked`). Recommend coordinating with the issue author whether to ship #27 now (correct contract, idle until emitters exist) or defer.

### #34 — Billing archival tier ADR (P2, decision-pending)

Seven open questions per issue body:

1. Storage target (Git cold-tier bucket vs separate analytics-lake bucket)
2. Erasure-coding (10,4) vs 3× replication (small dataset, infrequent reads)
3. Format: Parquet (recommend) vs JSONL
4. Query path: Athena/Trino vs write-and-forget
5. Retention horizon (initial 13 months in PG; legal floor ≥ 7 years for invoices)
6. Restore path (manual workflow vs read-only FDW attach)
7. Encryption scope (ADR-011 inheritance vs separate KMS)

Decision-only PR. No code. Suggested ADR number: ADR-018.

## 7. Open architecture questions reminder

Per `CLAUDE.md`, do not commit to these without a spike:

- Erasure coding library (June 2026)
- MCP server protocol version (July 2026)
- PR reputation model (July 2026)
- AGENTS.md schema versioning (July 2026)
- Cross-org dedup feature-flag default (August 2026)

#34 (billing archival tier) is **adjacent** to "erasure coding library" but distinct: #34 picks a tier shape for a different data class (operational analytics, not Git objects). #34 may *defer* the EC library question or anchor on the Git plane outcome.

## 8. Risks & ordering hazards

| Hazard | Mitigation |
|---|---|
| #14 ships `EventQueue` interface by reflex, contradicting issue body + ADR-008 | Issue #14 explicitly drops it. Reviewer must check no `EventQueue` interface added. |
| #15 starts before #35 merges → postgres impl never validated | Hard-gate: #35 must merge before #15 PR opens. |
| #18 archive arm pulls in #34 prematurely → blocked on ADR | Split into #18-rollover + #18-archive (recommended above). |
| #33 worker imports `plane/data/store/postgres` directly → plane boundary breach | `plane/workflow/adapters/data/` MUST take interfaces only. State mutations MUST go via app-plane HTTP/gRPC, not direct outbox writes. |
| #27 ships before #15 emits `user.created` events → harmless but consumer sees no traffic | Document explicitly. Or defer #27 until at least one identity event type is emitted in production. |
| Determinism violations in #18 workflow code | #33 lint-determinism step + `gitscale-temporal-determinism` skill — run both. |
| Redpanda integration tests in #14/#35 race outbox-consumer integration tests on shared docker-compose | Use distinct testcontainers per test package; avoid global state. |

## 9. Phase-1 completion definition

`#5` closes when:

- ✅ All migrations apply on a fresh DB (already true)
- ✅ All outbox tables co-located with source domain (already true)
- ✅ Polling consumer drains and publishes < 1s (already true via #32)
- ⏳ `MetadataStore`, `CacheStore`, `EventQueue` interfaces compile + stub impls — `CacheStore` ✅, `EventQueue` (= `KafkaProducer`) ✅, `MetadataStore` pending #14
- ⏳ Identity domain integration tests against real PG — pending #15

So #5 closure = #14 + #35 + #15 = 3 sequential merges.

## 10. Suggested commit / PR sequence

```
1. PR #14 → main          (Data — MetadataStore interfaces)
2. PR #35 → main          (Data — compliance suite, depends on #14)
3. PR #15 → main          (Application — identity service, closes #5)

In parallel:
4. PR #33 → main          (Workflow — bootstrap)
5. PR #34 → main          (ADR-018, doc-only)
6. PR #27 → main          (Application — cache invalidator, fully unblocked)

After #33 + #34:
7. PR #18-rollover → main (rollover arm, on #33)
8. PR #18-archive → main  (archive arm, on #33 + #34)
```

## 11. Branch naming check (CLAUDE.md conformance)

All proposed branches conform to `type/plane-short-description`:

- `feat/data-store-interfaces` ✓
- `feat/data-metadatastore-compliance-suite` ✓
- `feat/application-identity-domain-service` ✓
- `feat/workflow-plane-bootstrap` ✓
- `feat/workflow-billing-partition-rollover` ✓
- `feat/application-identity-cache-invalidator` ✓
- `adr/data-billing-archival-tier` ✓ (uses `adr` type, plane = data — consistent with prior `adr/data-cache-payload-and-invalidation` from #36)

Every PR closes ≥ 1 issue (CLAUDE.md invariant). PR titles mirror issue titles.

## 12. Out of scope (explicit)

- Edge plane (Envoy WASM filters) — no open issues; sequenced after Phase 1 is closed.
- Git plane (Gitaly client wrappers, storage tiering code) — no open issues yet.
- MCP server — gated on protocol-version spike (July 2026).
- PR engine, webhooks, CI Firecracker provisioning — out of Phase 1; future epics.

## 13. Review delta — 2026-05-06 expert review

Five reviewers ran in parallel: adr-historian, data-plane, application-plane, workflow-plane, comprehensive-review:architect-review. Convergent findings folded back into the plan as amendments. Sections to update before any PR opens.

### 13.1 Amendments to scope of #14

- **Fill reader/writer bodies in #14, not stubs.** The `IdentityReader.GetUserByID/GetUserByEmail/GetAgentByID` + `IdentityWriter.InsertHumanUser/InsertAgentIdentity/UpdateAgentReputationScore` queries are schema contracts (#19, merged) — keeping them as `errNotImplemented` punts ~300 lines of pgx into #15 as data-plane code reviewed by application reviewers. Fill them in #14; keep #15 a pure application-plane PR.
- **Add `store.IsRetryable(err) bool` helper.** Wraps the pgconn `40001` SQLState check so application code does not import pgconn. Without this, two retry policies will emerge.
- **Typed `Domain` enum + single source of truth.** Domains are duplicated in `outbox/wiring/wiring.go::AllDomains`, `kafka/topics.go`, migration filenames, and now `WriteOutbox`. Introduce `plane/data/store.Domain` (string-typed), constants `DomainIdentity, DomainRepositories, …`, with a `Domain.Topic()` helper. Refactor `wiring.DomainConfig.Domain` to consume it.
- **Compliance suite (#35) must assert UUIDv7 (or equivalent monotonic) generation for `event_id`** — covers collision risk under concurrent inserts.

### 13.2 Amendments to scope of #15

- **Split #15 into 3 pieces** — `service.go` interface + `models.go` + stub-impl + service-level tests can land as a draft PR in parallel with #35. `postgres_service.go` gates on #35. Integration tests run after both. Saves one merge cycle on the critical path.
- **Service interface gaps to add up front:**
  - `GetAgentsByParentUser(ctx, userID)` — uses existing `idx_agent_identities_parent_user_id`; PR engine + edge identity resolution will need it.
  - `LookupIdentityForCache(ctx, principalID) (*IdentityCacheEntry, error)` — loader callback for `cache.GetIdentity`. Without it, edge plane reaches past the service into `IdentityReader`. Real plane-boundary risk.
  - Revocation methods deferred to a sibling issue (see §13.6).
- **Reputation: `SetAgentReputationScore(ctx, agentID, score)` not `UpdateAgentReputationScore(ctx, agentID, delta)`.** Compute outside the Tx, persist inside; payload carries `delta = newScore - oldScore`. Avoids 40001 retry storms on read-modify-write.
- **`credential_hash` policy lives in the service.** Signature `CreateUser(ctx, email string, plaintextCredential string)`; service hashes via argon2id behind a `CredentialHasher` interface (ADR-017 swap surface). Edge plane never sees plaintext post-TLS. May need ADR for argon2id parameters.
- **Metering-ready event envelope.** `agent.created` payload must carry `agent_class`, `rate_bucket`, `quota_account_id` from day one — even with no metering consumer. Avoids schema retrofit when metering plane lands.

### 13.3 Amendments to scope of #33

- **Namespace must be env-derived.** `gitscale-prod`, `gitscale-staging`, `gitscale-dev` — never literal `gitscale`. Namespaces are the retention/RBAC boundary; merging envs forces cross-env ACLs.
- **Delete or split the adapter package.** Two acceptable shapes:
  - **Delete `plane/workflow/adapters/data/`** entirely; activities import `plane/data/store` + `plane/data/cache` interfaces directly (architect verdict).
  - **Split into `plane/workflow/adapters/{store,cache,appclient}/`** so the import graph reflects what each activity actually needs (workflow-plane verdict).
  - Pick one; do not keep an umbrella `adapters/data/`.
- **Determinism lint canon.** Externalize rules to `plane/workflow/determinism-rules.txt` (editable without script changes). Add: `crypto/rand`, `uuid.New*`, bare `go ` in workflow files, `make(chan …)`, `select { case <-ch: }` on Go channels, `sync.*`, `time.After`, `time.NewTimer`, `http.`/`net.` imports. Keep the warning-level `range over map` flag but only when the body contains a `workflow.` call.
- **Schedule API over cron string** for #18 — discoverable, pausable, supports backfill.
- **Canary workflow exercises one read-only activity through the adapter** (not no-op). Add a second fixture under `plane/workflow/testdata/bad/` with a deliberate determinism violation and assert `lint-determinism.sh` fails on it — proves the lint isn't silently passing.
- **Worker-options pinning.** `MaxConcurrentActivityExecutionSize`, `MaxConcurrentWorkflowTaskPollers`, `WorkerStopTimeout` set explicitly in `cmd/workflow-worker/main.go`.
- **OTel interceptor.** Wire `temporal.io/sdk/contrib/opentelemetry` in the registry; activity spans child off workflow spans.
- **Default `RetryPolicy` per activity** — no infinite retries. `CreatePartition`: `MaximumAttempts: 5`.
- **`workflow.GetVersion` discipline** + project default `continue-as-new` threshold helper land in #33.
- **Registry leaves a slot for `ApprovalActivity`** (ADR-015) so first plan-approval workflow does not require a registry refactor.
- **Single-domain activity rule.** No activity spans more than one domain's outbox row. Cross-domain workflows compose per-domain saga steps with explicit compensation.
- **Real reason for the plane-boundary rule.** Update §6.#33 rationale: it is *not* "outbox transaction must hit app plane" (technically false). It is (a) domain invariants live in the app plane (uniqueness, clamping, event-type selection), (b) auth/audit context (`actor_id`, `principal_kind`) is stamped only in app plane, (c) single-writer-per-aggregate keeps schema migrations sane.

### 13.4 Amendments to scope of #18

- **Ship split as planned (#18-rollover + #18-archive).** Architect concurs; workflow-plane confirms split is not artificial.
- **`CreatePartition` activity wraps the DDL in a transactional advisory lock** keyed on `(table, year, month)` — serializes concurrent retries from worker replicas. `IF NOT EXISTS` alone is not always benign on PG with attached-partition checks.
- **Idempotency unit test.** Run the activity twice against the same target; assert no error, no duplicate partition.

### 13.5 ADR posture changes

- **ADR-017 needs a small amendment** clarifying that the named `EventQueue` swap surface is satisfied by `plane/data/outbox.KafkaProducer` (with the matching compliance suite at `plane/data/compliance/eventqueue.go`). Plan wording "drops EventQueue" is misleading — the interface materially exists under a different name. File a one-paragraph amendment alongside #14, OR rename `outbox.KafkaProducer` to `outbox.EventQueue`.
- **New ADR-019: workflow-plane state mutations route via application-plane RPC.** This is a new plane-boundary contract, not a gap-fill — it will affect every future workflow. File before #33 merges.
- **Stop citing ADR-006 for the `40001` retry-surface contract.** ADR-006 doesn't contain that wording. The contract is correct but lives in the PR description, not in ADR text.

### 13.6 New issues to file before / during execution

| New issue | Pri | Plane | Trigger |
|---|---|---|---|
| Identity service revocation methods + emitters (`user.disabled`, `agent.revoked`, `org.member_removed`, `principal.permissions_changed`) | P1 | application | gates #27; required for #27 to ship with a real round-trip test |
| `[Data] Typed Domain enum + SSOT` | P2 | data | rolled into #14 or filed separately |
| `[Workflow] Outbox row TTL expirer per ADR-008` | P2 | workflow | hidden TODO in ADR-008 line 531; no code today |
| `[ADR] Reconcile EventQueue naming with outbox.KafkaProducer` | P2 | docs | §13.5 |
| `[ADR-019] Workflow plane state mutations via app-plane RPC` | P1 | docs | gates #33 |
| `[Data] Generic updated_at trigger across 5 domains` | P2 | data | columns default `now()` on insert; UPDATE does not bump |
| `[Data] Add created_at/updated_at to identity.org_memberships and oauth_apps` | P2 | data | inconsistent with rest of identity domain |
| `[Data] usage_events 2027-05+ partition gap before #18 ships` | P1 | data | calendar bomb — May 2027 inserts will fail until #18-rollover ships |
| `[Observability] Rename outbox metric to outbox_consumer_high_water_lag_seconds` | P3 | data | ADR-008 wording vs current metric name |

### 13.7 Risks added to §8

- **Migration drift across long-lived branches.** #14, #35, #15 all touch `plane/data/store/postgres/` over weeks. CI must run migrations on a fresh DB *and* on a DB seeded by the prior set; rebase-on-main daily.
- **`event_id` collision** under concurrent inserts if generation is naive UUIDv4 or `now()`-based. Compliance suite (#35) asserts UUIDv7 or equivalent.
- **CI minute inflation** from per-package testcontainers + Temporal dev-server + Redpanda. Mitigate via shared fixtures (`testing.M` + `t.Cleanup`).
- **Runtime config skew** between `cmd/workflow-worker` and existing `cmd/outbox-consumer` (Kafka brokers, Redis URL). Centralize config or document the duplication.
- **#27 ships with no producer** — known-broken contract with flat lag metrics and no end-to-end signal. Defer until revocation emitters exist.

### 13.8 Phase-2 landmines flagged

- **Worker registry should be partitioned by traffic class (agent vs human), not by domain.** Phase-2 edge plane will need per-class throttling at workflow layer. Note in #33 README to prevent re-org.
- **Session lifecycle events (`agent.session_started`, etc.) are explicitly Phase-2** — document so the edge plane knows materialized views must wait.
- **#34 archival decision interacts with Git-plane cold-tier bucket.** ADR-018 should pick a separate bucket OR block on Git-plane storage spike to avoid noisy-neighbor.

### 13.9 Final critical path (post-amendment)

```
Wave 0 (parallel):
  #14   data    fills reader/writer bodies + IsRetryable + Domain enum
  #33   workflow  with namespace=env-derived, adapter split, Schedule API, OTel
  #34   adr     billing archival tier (decision-only)
  ADR-019 amendment    plane-boundary rule for workflow writes
  ADR-017 amendment    EventQueue ↔ outbox.KafkaProducer reconciliation
  Identity revocation issue  filed (gate for #27)

Wave 1 (after #14):
  #35   data    compliance suite, UUIDv7 assertion
  #15-stub  application  service interface + models + stub impl + tests (parallel with #35)

Wave 2 (after #35):
  #15-postgres application  postgres_service.go wired against MetadataStore

Wave 3 (after #15-postgres):
  #15-revocation application  Disable/Revoke methods + emitters
  Phase 1 epic #5 closes here

Wave 4 (after #33 + #15-revocation):
  #27   application  identity cache invalidator (now has live producers)

Wave 5 (after #33; archive arm gated on #34):
  #18-rollover   workflow  CreatePartition + Schedule API + advisory lock
  #18-archive    workflow  DetachAndArchivePartition (after #34)
```

Critical path: `#14 → #35 → #15-postgres → #15-revocation → #5 close`. #15-stub overlaps #35.
