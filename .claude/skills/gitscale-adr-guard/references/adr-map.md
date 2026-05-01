# ADR ↔ code surface map

Living index. Update whenever a new ADR lands or a code surface migrates.

| Code surface | ADR(s) | What the ADR binds |
|---|---|---|
| `plane/data/store/**` (PostgreSQL DDL, `MetadataStore` impl) | ADR-006, ADR-017 | PostgreSQL behind `MetadataStore` interface; hash-partitioned hot tables |
| `plane/data/interfaces/**`, `plane/data/compliance/**` (`MetadataStore`, `CacheStore`, `EventQueue` definitions + compliance suite) | ADR-017 | Go interface abstractions as swap surface; all concrete implementations must pass the shared compliance suite |
| `plane/data/outbox/**`, any state mutation paired with Kafka publish | ADR-008 | Outbox + polling-based outbox consumer; ack on DB commit, not Kafka publish; consumers idempotent on `event_id` |
| `plane/git/storage/hot/**` | ADR-001 | 3× synchronous replication, 2-of-3 quorum writes, NVMe local. No erasure coding on hot path. |
| `plane/git/storage/cold/**`, LFS writers | ADR-001, ADR-011 | (10,4) Reed-Solomon erasure coding on S3-compatible store; per-org DEK encryption |
| `plane/git/gitaly/**` | (pending ADR — Gitaly RPC) | All Git ops via Gitaly RPC; never call `git` binary directly |
| `plane/edge/**` Envoy + WASM | (pending ADR — edge plane) | Identity resolution + token metering at edge |
| `plane/edge/identity/**`, anything stamping SVID/JWT | ADR-010 | SPIRE/SPIFFE service identity; JWT-SVID per request |
| Kafka topology, topic schemas | ADR-004, ADR-008 | Kafka as canonical event bus; fed by polling outbox consumer; not direct producer-consumer from app |
| `plane/data/cache/**` (Redis impl, `CacheStore` impl) | ADR-009, ADR-017 | Redis for repo-location cache + enforcement counters; behind `CacheStore` interface; pub/sub invalidation |
| Gitaly hook metering (`pack_objects`, `pre-receive`) | ADR-012 | Two-tier counters: Redis enforcement + ClickHouse analytics; nightly reconciliation drift ≤ 0.5% |
| `pkg/search/vespa/**`, ranking | ADR-016 | Vespa is primary search: code, issues, semantic |
| `pkg/search/qdrant/**` | ADR-016 | Qdrant only for PR dedup (cosine ≥ 0.92). Not customer-facing search. |
| `plane/workflow/temporal/**` | ADR-003 | Temporal for long-running agent sessions + CI pipelines |
| CI runner sandbox code | ADR-002 | Firecracker microVMs; not Docker, not gVisor |
| GraphQL resolvers + cost analysis | ADR-013 | Named-subset compatibility; persisted-query 0.5× discount; per-principal cost ceilings |
| Plugin interface implementations + sandbox wiring | ADR-014 | Semver per interface; sandbox by input trust (sensitive-input plugins as gRPC sidecars over UDS) |
| Plan-approval policy enforcement | ADR-015 | Signed Policy objects; predicate-based rules; Merkle-chained audit |

## Open architecture questions (pre-ADR)

These are spike territory — *no* ADR exists yet. A change here is filling a gap, not amending. Reference the open question number in the PR description.

| Question | Decision target |
|---|---|
| Erasure coding library: ISA-L vs. Reed-Solomon Go | June 2026 |
| MCP server protocol version at launch | July 2026 |
| PR reputation model: rule-based vs. ML-based | July 2026 |
| AGENTS.md schema versioning policy | July 2026 |
| Cross-org dedup feature-flag default | August 2026 |

When the ADR for one of these lands, update the table above and remove the question from this list.
