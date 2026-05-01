---
name: gitscale-event-schema
description: Use when adding a new Kafka topic, modifying an existing event schema, adding or changing a value of outbox.event_type, modifying the payload shape any consumer reads, or writing a new consumer. Triggers on changes under plane/data/events, internal/proto/events, any .proto/.avsc/.json schema file referenced by Kafka producers, and on user questions like "is this event change backwards-compatible?", "do I need a new topic?", "can I add this field?". Schema changes ripple to every consumer (search, webhooks, billing, audit) — a non-compatible change ships a silent break in production until a consumer crashes mid-replay.
---

# GitScale Event Schema

## Overview

Per ADR-008, GitScale's event bus is fed by a polling-based outbox consumer that drains the `outbox` table and publishes to Kafka. Consumers (search indexer, webhook delivery, billing, audit) read those topics and must be idempotent on `event_id`.

Event schemas are a contract between producers (one per `event_type`) and many consumers. A breaking schema change without coordination breaks every consumer at once. The cost of getting this right is one schema-evolution rule; the cost of getting it wrong is a multi-team incident.

**Core principle:** event schemas evolve forward only. Optional-additive changes are safe; required fields, removed fields, type changes, and renamed enum values are not.

## When to Use

Trigger on **any** of:

- A new `event_type` value is introduced (new producer)
- An existing event's payload schema (`.proto`, `.avsc`, JSON Schema, or struct used as payload) is modified
- A consumer is added that reads from a topic
- A consumer's deserialization code changes (different field set, different type assumptions)
- The user asks "is this backwards-compatible?", "can I rename this field?", "do I need a new topic?", "what version is this event?"

**Don't trigger** for: changes to non-payload metadata (Kafka headers used only for routing, never for business logic), changes to the `outbox` table's own structural columns (those are platform-level, separate flow).

## The compatibility rules

For each `event_type`, treat the schema as published. Allowed forward-compatible changes:

| Change | Allowed? | Notes |
|---|---|---|
| Add a new optional field | ✅ | Old consumers ignore it. |
| Remove an optional field that no consumer reads | ✅ | Verify no consumer reads it; otherwise this is a break. |
| Add a new event_type | ✅ | Brand-new contract; no existing consumers. |
| Tighten a field's validation (e.g., max length) | ⚠️ | Allowed only if no producer has emitted a value that would now fail validation. Audit historical events. |
| Rename a field | ❌ | Break. Add a new field, deprecate the old one over a release cycle, then remove. |
| Change a field's type | ❌ | Break. Add a new field with the new type. |
| Make an optional field required | ❌ | Break. Old events lack the field. |
| Rename or remove an enum value used in payloads | ❌ | Break. Replay of historical events fails. |
| Reuse a deleted field's tag/number (proto) | ❌ | Break. Old events decode the new field as the old one. |

## The idempotency rule (consumer side)

Every consumer must dedupe on `event_id`. Allowed patterns:

- A `processed_events (event_id PRIMARY KEY)` table with `INSERT ... ON CONFLICT DO NOTHING` before applying the side effect.
- A projection table where the side effect is itself idempotent (e.g., `UPSERT` keyed on `aggregate_id` with the event's `version`).
- A natural-key idempotency where applying the same event twice produces the same end state.

If none of those apply, the consumer is wrong.

## Workflow

1. **Identify the event_type(s) touched** by the diff.
2. **Diff the schema** — what fields changed, what types changed, what enums shifted.
3. **Classify each change** against the table above.
4. **For each consumer reading this event_type**, walk the deserialization to verify it still works under the new schema.
5. **For new consumers**, verify the idempotency pattern is in place.
6. **Output a verdict** including a per-consumer compatibility matrix if the change is non-trivial.

## Output Format

```
event-schema: <ok | break | additive>
Event types touched: <type | type, type>
Schema changes: <field added/removed/typed>
Consumer impact:
  <consumer> — <safe | breaks at file:line — reason>
Idempotency:
  <consumer> — <ok via processed_events | ok via upsert | missing>
Fix: <suggestion: deprecation cycle, new event_type, idempotency table, etc.>
```

## Example

**Input diff:**

```proto
// internal/proto/events/repo_created.proto
message RepoCreatedV1 {
  string event_id = 1;
  string repo_id = 2;
  // string owner = 3;     ❌ removed — was used by webhook consumer
  uint64 created_at = 3;   ❌ field number 3 reused for new field
  string visibility = 4;   ❌ new required field
}
```

**Verdict:**

```
event-schema: break
Event types touched: repo.created
Schema changes:
  - removed field owner (string, tag 3) — read by webhook delivery consumer
  - reused tag 3 for created_at (uint64) — historical events decode owner→created_at
  - added required field visibility (no default) — old events lack it
Consumer impact:
  webhook delivery — breaks at plane/application/webhooks/render.go:42
    (reads .Owner; would now read garbage as the wire bytes for tag 3 are
    now interpreted as a uint64)
  search indexer — safe (doesn't read owner)
  audit log — breaks at plane/data/audit/sink.go:18 (visibility is required,
    historical events have no value)
Idempotency: existing dedupe table — ok
Fix:
  - Don't reuse tag 3. Use a new tag (e.g., 5) for created_at.
  - Either keep owner with tag 3 (as deprecated) for one release cycle, or
    publish a new event_type repo.created.v2 alongside repo.created and
    migrate consumers explicitly.
  - Make visibility optional with a documented default (e.g., "private")
    until all historical events are out of the retention window, then promote
    to required in a follow-up release.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| "It's just a rename, the field means the same thing" | Wire-level bytes don't know what fields mean. Renames are breaks. |
| Reusing a deleted proto tag for a new field | Reserve the tag (`reserved 3;` in proto). Pick a fresh number. |
| Adding a required field with no default | Old events have no value; consumers crash on deserialize. Add as optional with a default. |
| Treating the schema-registry warning as informational | If the registry rejects, listen. The registry knows the consumer surface. |
| Rolling out producer change before consumer change | Reverse it for additive changes (consumers tolerate the new field), keep it producer-last for removals. |
| Skipping idempotency because "the producer only emits once" | At-least-once is the floor. Re-deploys, replays, partition rebalances all duplicate. Always dedupe. |

## Why This Matters

Events are the loosest seam in the system. Producers don't know which consumers exist; consumers don't know how producers will evolve. The schema is the only contract. A break in the schema bypasses every type system, every CI test that runs in the producer's repo, every staging environment that doesn't run all consumers.

The replay scenario is the worst case: a consumer is rebuilt or restarted and reads a year of events at once. A schema change that "worked fine for new events" breaks loudly during replay, often during an incident when someone is trying to restore service.

Five minutes of compatibility audit at edit time prevents the multi-team page.
