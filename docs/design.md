# GitScale Design Document

> Living document. Stub revision — sections to be filled in as designs land. ADRs referenced from this document must exist in [`architecture.md §8`](architecture.md#8-architecture-decision-records); the CI check `.github/workflows/adr-check.yml` enforces this.

## Purpose

The design document captures **how** GitScale implements the architecture, mapping each design choice to the ADRs in `architecture.md §8` that bind it. Where this doc references `ADR-NNN`, the binding decision lives in architecture.md; this doc explains the implementation that follows from it.

## Sections

### 1. Goals translated to design constraints

_TODO._

### 2. Edge plane design

_TODO._ Identity resolution, token metering, WASM filter chain. Bound by ADR-012 (SPIFFE).

### 3. Git plane design

_TODO._ Gitaly RPC layer, pack negotiation, storage tier routing, hot/cold migration. Bound by storage-tiering ADR (pending) and Gitaly-RPC ADR (pending).

### 4. Application plane design

_TODO._ Repo API, PR engine, webhook delivery. Bound by ADR-007 (CockroachDB).

### 5. Workflow plane design

_TODO._ Temporal worker topology, workflow / activity boundaries, signal handling, versioning policy.

### 6. Data plane design

_TODO._ CockroachDB schema, outbox, CDC changefeeds → Kafka, DragonflyDB cache layout, Vespa index strategy. Bound by ADR-007, ADR-010 (outbox), ADR-011 (DragonflyDB), ADR-021 (Vespa/Qdrant).

### 7. Cross-cutting

_TODO._ Observability, deploy topology, on-call surfaces, SLOs.

### 8. Open design questions

Open questions awaiting design (ADRs may follow):

- (mirrors the open architecture questions in `architecture.md`; promote to design entries as spikes resolve)

## Convention

Reference an ADR by `ADR-NNN`. The CI check fails if any `ADR-NNN` referenced here is not present in `docs/architecture.md`. This keeps the two documents synchronized — design choices that depend on a decision must surface that decision explicitly.
