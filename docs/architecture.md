# GitScale Architecture

> Living document. Stub revision — sections 1–7 to be filled in as design lands. Section 8 (ADRs) is the source of truth for binding decisions.

## 1. Goals & non-goals

_TODO._ See `CLAUDE.md` for the three core principles (agent-first traffic, plane decoupling, metering as infrastructure) until this section is written.

## 2. Five-plane overview

_TODO._ See `README.md#architecture-at-a-glance`.

## 3. Edge plane

_TODO._

## 4. Git plane

_TODO._

## 5. Application plane

_TODO._

## 6. Workflow plane

_TODO._

## 7. Data plane

_TODO._

## 8. Architecture Decision Records

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

### ADR-007: Adopted CockroachDB for the metadata layer

- **Status:** Accepted
- **Date:** TBD
- **Context:** Metadata layer needs horizontal scale, strong consistency for cross-region writes, and a Postgres-compatible wire protocol so existing tooling and ORMs work.
- **Decision:** CockroachDB is the metadata database for all five schema domains (identity, repositories, collaboration, ci, billing).
- **Consequences:** Engineers write Postgres-flavoured SQL. Foreign keys and serializable transactions are available. CDC changefeeds (see ADR-010) feed downstream consumers.

### ADR-010: Adopted outbox-based event consistency via CockroachDB CDC

- **Status:** Accepted
- **Date:** TBD
- **Context:** State changes must be observable by search, webhooks, billing, and audit consumers without dual-writes that risk divergence between the DB and the event bus.
- **Decision:** State-mutating SQL transactions write the source change AND an `outbox` row in the same transaction. The caller is acknowledged on DB commit, not on Kafka publication. CockroachDB CDC changefeeds tail the outbox and publish to Kafka asynchronously. Consumers must be idempotent on `event_id`.
- **Consequences:** No dual-write race. Slight publish lag on the order of changefeed latency. Consumers carry the burden of idempotency.

### ADR-011: Adopted DragonflyDB for the rate-limit and identity cache

- **Status:** Accepted
- **Date:** TBD
- **Context:** Edge-plane identity resolution and per-agent rate limiting need a Redis-compatible KV with high single-node throughput.
- **Decision:** DragonflyDB serves the rate-limit, identity, and repo-location caches. Redis-compatible client libraries unchanged.
- **Consequences:** Higher per-node throughput vs. vanilla Redis. Operational tooling slightly different.

### ADR-012: Adopted SPIRE/SPIFFE for service identity and mTLS

- **Status:** Accepted
- **Date:** TBD
- **Context:** Cross-plane and cross-service calls need cryptographic identity that's verifiable per request, rotatable without downtime, and auditable.
- **Decision:** SPIRE issues SPIFFE JWT-SVIDs to every workload. Inter-service calls present and verify SVIDs per request.
- **Consequences:** Every service needs a SPIRE workload entry. Clock skew tolerance and revocation paths must be designed.

### ADR-021: Adopted Vespa as primary search; Qdrant scoped to PR deduplication

- **Status:** Accepted
- **Date:** TBD
- **Context:** Customer-facing search spans code, issues, and semantic queries. PR dedup is a narrower vector-similarity problem with a fixed cosine threshold.
- **Decision:** Vespa is the primary search backend. Qdrant is reserved for PR deduplication only, with a cosine similarity threshold of 0.92. Qdrant is not exposed as customer-facing search.
- **Consequences:** Single search backend for end users. PR dedup runs on a narrow, separately-tuned vector store.

---

### Pending / un-numbered

These decisions are referenced in `CLAUDE.md` and code but are not yet ADR-numbered. Promote to numbered ADRs as designs land:

- Storage tiering: hot (3× sync replication, NVMe, 2-of-3 quorum) vs. cold ((10,4) Reed-Solomon on S3-compatible store).
- Gitaly RPC for all Git operations; never call the `git` binary directly.
- Firecracker microVMs for CI isolation; not Docker, not gVisor.
- Envoy + WASM at the edge for identity resolution and token metering.
- Temporal for long-running agent sessions and CI pipelines.
- Kafka topology fed exclusively from CockroachDB CDC.

### Open questions (no ADR yet)

| Question | Decision target |
|---|---|
| Erasure coding library: ISA-L vs. Reed-Solomon Go | June 2026 |
| MCP server protocol version at launch | July 2026 |
| PR reputation model: rule-based vs. ML-based | July 2026 |
| AGENTS.md schema versioning policy | July 2026 |
| Cross-org dedup feature-flag default for Cloud Free | August 2026 |
