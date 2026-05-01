# GitScale

A ground-up Git hosting platform built for agentic (AI coding agent) workloads.

Incumbent Git hosts were designed for human developers committing a few times a day. AI coding agents operate at orders-of-magnitude higher request rates, with different identity semantics, quota requirements, and noise filtering needs. GitScale treats agents as the primary traffic class from day one.

## Architecture at a glance

Five independently scalable planes — failures in one must not cascade to others:

| Plane | Responsibility |
|---|---|
| **Edge** | Envoy + WASM gateway, identity resolution, token metering |
| **Git** | Gitaly RPC layer, pack negotiation, object routing, storage tiering |
| **Application** | Go services: repo API, PR engine, webhook delivery |
| **Workflow** | Temporal orchestration, long-running agent session management |
| **Data** | PostgreSQL metadata, Kafka event bus, Redis cache, Vespa search |

## Contributing

See [`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE/) for issue templates (Feature, Spike, ADR) and [`.github/pull_request_template.md`](.github/pull_request_template.md) for the PR checklist.

Branch naming: `type/plane-short-description` — e.g., `spike/data-postgres-partition-strategy`.

Every merged PR must close at least one issue. Design decisions that change behaviour must reference the relevant ADR from `docs/architecture.md §8`.
