# ADR ↔ code surface map

Living index. Update whenever a new ADR lands or a code surface migrates.

| Code surface | ADR(s) | What the ADR binds |
|---|---|---|
| `plane/data/schema/**` (CockroachDB DDL) | ADR-007 | CockroachDB chosen over Vitess/Spanner for metadata layer |
| `plane/data/outbox/**`, any state mutation paired with Kafka publish | ADR-010 | Outbox + CDC changefeed; ack on DB commit, not Kafka publish; consumers idempotent on `event_id` |
| `plane/git/storage/hot/**` | ADR (storage tiering) | 3× synchronous replication, 2-of-3 quorum writes, NVMe local. No erasure coding on hot path. |
| `plane/git/storage/cold/**`, LFS writers | ADR (storage tiering) | (10,4) Reed-Solomon erasure coding on S3-compatible store |
| `plane/git/gitaly/**` | ADR (Gitaly RPC) | All Git ops via Gitaly RPC; never call `git` binary directly |
| `plane/edge/**` Envoy + WASM | ADR (edge plane) | Identity resolution + token metering at edge |
| `plane/edge/identity/**`, anything stamping SVID/JWT | ADR-012 | SPIRE/SPIFFE service identity; JWT-SVID per request |
| Kafka topology, topic schemas | ADR-010 | Kafka fed via CockroachDB CDC from `outbox`; not direct producer-consumer |
| `pkg/cache/dragonfly/**` | ADR-011 | DragonflyDB for repo-location cache (Redis-compatible) |
| `pkg/search/vespa/**`, ranking | ADR-021 | Vespa is primary search: code, issues, semantic |
| `pkg/search/qdrant/**` | ADR-021 | Qdrant only for PR dedup (cosine ≥ 0.92). Not customer-facing search. |
| `plane/workflow/temporal/**` | ADR (workflow plane) | Temporal for long-running agent sessions + CI pipelines |
| CI runner sandbox code | ADR (CI isolation) | Firecracker microVMs; not Docker, not gVisor |

## Open architecture questions (pre-ADR)

These are spike territory — *no* ADR exists yet. A change here is filling a gap, not amending. Reference the open question number in the PR description.

| Question | Decision target |
|---|---|
| Erasure coding library: ISA-L vs. Reed-Solomon Go | June 2026 |
| MCP server protocol version at launch | July 2026 |
| PR reputation model: rule-based vs. ML-based | July 2026 |
| AGENTS.md schema versioning policy | July 2026 |
| Cross-org dedup feature-flag default for Cloud Free | August 2026 |

When the ADR for one of these lands, update the table above and remove the question from this list.
