# CLAUDE.md

This file provides guidance to Claude Code when working in this repository.

## Purpose

This is the **implementation repository** for the GitScale platform. Code lives here.

## Three core principles

1. **Agents are the primary traffic class.** Every design decision should assume agent request rates, not human ones.
2. **Loose coupling at every seam.** Planes must be independently deployable. Cross-plane calls go through well-defined interfaces; no shared in-process state across plane boundaries.
3. **Metering is infrastructure.** Rate limits, quota accounting, and billing counters are first-class concerns, not afterthoughts.

## Technology stack

| Component | Technology | Notes |
|---|---|---|
| Application plane | **Go** | Primary language |
| Metadata DB | **CockroachDB** | 5 schema domains: identity, repositories, collaboration, ci, billing |
| Git RPC | **Gitaly** (GitLab OSS) | Runs on file servers; do not call Git binaries directly |
| Workflow orchestration | **Temporal** | Long-running agent sessions, CI pipelines |
| Edge gateway | **Envoy + WASM** | Identity resolution, token metering at the edge |
| Rate limit / identity cache | **DragonflyDB** | Redis-compatible; repo-location cache (ADR-011) |
| Search | **Vespa** | Primary search: code, issues, semantic (ADR-021) |
| Vector similarity (PR dedup only) | **Qdrant** | Cosine similarity threshold 0.92 (ADR-021); not for customer-facing search |
| CI isolation | **Firecracker microVMs** | Hardware boundary; not Docker or gVisor |
| Service identity / mTLS | **SPIRE/SPIFFE** | JWT-SVID, per-request verification (ADR-012) |
| Event bus | **Kafka** | Fed via CockroachDB CDC changefeeds from the outbox table (ADR-010) |

## Five planes — directory structure (target)

```
plane/
  edge/        # Envoy WASM filters, token metering, identity resolution
  git/         # Gitaly client wrappers, storage tier routing, pack negotiation
  application/ # Repo API, PR engine, webhook delivery (Go services)
  workflow/    # Temporal workers and workflow definitions
  data/        # CockroachDB schema, migrations, Kafka topology, DragonflyDB config
```

Failures in one plane must not cascade to others. Cross-plane calls must go through the service API — no direct DB access from the wrong plane.

## Event consistency model (ADR-010)

State-mutating SQL transactions write the source change **and** an `outbox` row in the same transaction. The caller is acknowledged on DB commit, not on Kafka publication. CockroachDB CDC changefeeds tail the outbox and publish to Kafka asynchronously. Consumers (search, webhooks, billing, audit) must be idempotent on `event_id`.

## Storage tiering

- **Hot** (< 7 days active): local NVMe, 3× synchronous replication, 2-of-3 quorum writes
- **Cold** (> 30 days / all LFS): (10,4) Reed-Solomon erasure coding on S3-compatible object store
- Do **not** apply erasure coding to hot data — small random reads make reconstruction prohibitively expensive.

## ADRs

ADRs are tracked in `docs/architecture.md §8`. When code changes contradict an ADR, flag the conflict and open a `type/adr` issue before proceeding. If a proposal fills in an implementation detail not covered by any ADR, no new ADR is required — a PR description is sufficient.

## Open architecture questions (as of May 2026)

Avoid committing to these until the spike is resolved:

- Erasure coding library: ISA-L vs. Reed-Solomon Go (decision: June 2026)
- MCP server protocol version at launch (July 2026)
- PR reputation model: rule-based vs. ML-based (July 2026)
- AGENTS.md schema versioning policy (July 2026)
- Cross-org dedup feature-flag default for Cloud Free (August 2026)

## Branch and commit conventions

| Artifact | Pattern | Example |
|---|---|---|
| Branch | `type/plane-short-description` | `spike/data-cockroachdb-vs-vitess` |
| PR title | Mirror the issue title | `[Git] Design hot-tier replication quorum protocol` |
| ADR title | `ADR-NNN: Decision in past tense` | `ADR-007: Adopt CockroachDB for metadata layer` |

Every merged PR must close at least one issue.
