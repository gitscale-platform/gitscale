# GitScale sub-agents

Routing index. Main Claude dispatches via the `Task` tool with `subagent_type` matching the agent name.

## Plane specialists (own one directory)

| Agent | Owns | Invoke when |
|---|---|---|
| [edge-plane](edge-plane.md) | `plane/edge/**` | Envoy WASM, identity resolution, token meter, mTLS, SPIRE/SPIFFE |
| [git-plane](git-plane.md) | `plane/git/**` | Gitaly RPC, pack negotiation, hot/cold storage tier, LFS |
| [application-plane](application-plane.md) | `plane/application/**` | Go services, repo API, PR engine, webhook delivery, outbox writes |
| [workflow-plane](workflow-plane.md) | `plane/workflow/**` | Temporal workflows + activities, Firecracker microVM provisioning |
| [data-plane](data-plane.md) | `plane/data/**` | PostgreSQL schema, Kafka topology, polling outbox consumer, Redis keys, Vespa, Qdrant |

## Cross-cutting

| Agent | Role | Read/Write |
|---|---|---|
| [adr-historian](adr-historian.md) | ADR oracle — what does ADR-N say? Does diff conform? | Read-only |
| [spike-researcher](spike-researcher.md) | Investigate open architecture questions, produce evidence + recommendation | Writes only `docs/spikes/*.md` |

## Routing rules

- Edits to `plane/<X>/**` → dispatch to `<X>-plane` agent.
- A change spanning N planes → dispatch N agents in parallel; main Claude reconciles.
- Before any plane agent commits to an architectural choice, it must consult `adr-historian`.
- Open architecture question (per `CLAUDE.md` "Open architecture questions") → dispatch `spike-researcher`. Never commit code that pre-empts a spike.

## Skills referenced by agents

All agents have access to project skills under `.claude/skills/`:

- `gitscale-adr-guard` — ADR conformance check (every plane agent uses this)
- `gitscale-plane-boundary` — block cross-plane imports / shared in-process state
- `gitscale-outbox-check` — verify same-txn source row + outbox row (app + data planes)
- `gitscale-storage-tier-lint` — block erasure coding on hot path (git plane)
- `gitscale-agent-quota-check` — token meter + SPIFFE check (edge plane)
- `gitscale-event-schema` — Kafka topic backwards-compat (data plane)
- `gitscale-issue-pr-link` — every PR closes ≥1 issue, branch pattern (all planes at PR time)
- `gitscale-go-conventions` — golangci-lint + project rules (any agent writing Go)
- `gitscale-temporal-determinism` — workflow purity (workflow plane)
- `gitscale-firecracker-isolation` — block Docker/gVisor in CI runner (workflow plane)
