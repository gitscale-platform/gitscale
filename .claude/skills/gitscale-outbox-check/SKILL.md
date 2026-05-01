---
name: gitscale-outbox-check
description: Use when adding or modifying SQL writes (INSERT, UPDATE, DELETE) in plane/data, plane/application, or anywhere a PostgreSQL transaction is opened. Triggers when a Kafka producer is added, when a state mutation lacks a paired outbox row, when a consumer is written that processes events from Kafka, or when the user asks "is this an outbox write?", "do I need to publish an event?", "is this idempotent?". Catches dual-write divergence (DB state ≠ event bus) before it ships — the silent failure mode here is invisible until a downstream consumer (search, billing, webhooks, audit) drifts from reality and someone notices weeks later.
---

# GitScale Outbox Check

## Overview

ADR-008 binds GitScale to outbox-based event consistency. State-mutating SQL transactions write the source change AND a row to the `outbox` table in the same transaction. The caller is acknowledged on DB commit, not on Kafka publication. A polling-based outbox consumer (advisory-locked `SELECT ... LIMIT N` loop) drains the outbox and publishes to Kafka asynchronously.

This skill catches three failure modes:

1. **Dual write** — state mutated and Kafka produced separately. One can succeed while the other fails. Forbidden.
2. **Missing outbox row** — state mutated but no event written. Downstream consumers miss the change forever.
3. **Non-idempotent consumer** — consumer doesn't dedupe on `event_id`. Replay or duplicate publish corrupts state.

**Core principle:** the only durable signal that a state change happened is a committed `outbox` row in the same transaction as the change. Everything downstream — Kafka, search, billing, webhooks — derives from that.

## When to Use

Trigger on **any** of:

- An `INSERT`, `UPDATE`, or `DELETE` is added against a table other than `outbox` itself, in `plane/data` or `plane/application`
- A direct `kafka.Produce(...)`, `producer.Send(...)`, or equivalent appears in a code path that also touches the database
- A Kafka consumer is added in `plane/application` or `plane/workflow`
- A new event type is introduced (new value for `outbox.event_type`)
- The user asks "do I need to publish an event for this?", "outbox row?", "is this idempotent?", or "why is X getting double-processed?"

**Don't trigger** for: read-only SQL, schema migrations that don't write data, test fixtures that bypass the outbox deliberately and are flagged as such.

## The three rules

### Rule 1: same transaction or it didn't happen

State mutation and outbox row write must be in the **same** `tx.BeginTx → tx.Commit` block. Not in a `defer`, not in a goroutine, not in a separate function that takes `*sql.DB` instead of `*sql.Tx`.

### Rule 2: never produce to Kafka directly from the write path

If you find `kafka.Produce(...)` next to or after a SQL write in the same code path, that's a dual write. Replace with an outbox row. The polling outbox consumer handles the Kafka publish.

### Rule 3: consumers idempotent on `event_id`

Every consumer must check that `event_id` hasn't already been processed before applying the effect. Either via a `processed_events` table with a unique constraint or via a deduplicating projection.

## Workflow

1. **Scan the diff** for SQL mutations and Kafka producer calls.
2. **For each SQL mutation**, look for an `outbox` insert in the same transaction. If absent, flag.
3. **For each Kafka producer call** in code that also touches SQL, flag as dual write — propose replacing with outbox.
4. **For consumer changes**, look for an idempotency check keyed on `event_id`. If absent, flag.
5. **Output a verdict** with file:line citations.

## Output Format

```
outbox-check: <ok | violations>
Mutations without outbox: <file:line — table → missing event | none>
Direct Kafka producers: <file:line | none>
Non-idempotent consumers: <file:line — event_type | none>
Fix: <concrete suggestion per flag>
```

## Example

**Input diff:**

```go
// plane/application/repos/create.go
func (s *Service) Create(ctx context.Context, r Repo) error {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()

    if _, err := tx.ExecContext(ctx,
        `INSERT INTO repositories (id, owner, name) VALUES ($1, $2, $3)`,
        r.ID, r.Owner, r.Name,
    ); err != nil {
        return err
    }

    if err := tx.Commit(); err != nil { return err }

    return s.kafka.Produce("repo.created", r.ID) // ❌ dual write
}
```

**Verdict:**

```
outbox-check: violations
Mutations without outbox: plane/application/repos/create.go:8 — repositories → missing repo.created event
Direct Kafka producers:   plane/application/repos/create.go:18
Non-idempotent consumers: none
Fix:
  - Move the event into the same transaction by inserting an outbox row
    before tx.Commit():
      INSERT INTO outbox (event_id, event_type, aggregate_id, payload)
        VALUES (gen_random_uuid(), 'repo.created', $1, $2)
  - Delete the s.kafka.Produce call. The polling outbox consumer will publish.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| Outbox row written in a `defer tx.Commit` after the function body | Same function body, before `tx.Commit()`. `defer` runs after, which is too late if the row is meant to be in the same transaction. |
| Outbox row written via a separate `db.Exec` with no `tx` parameter | Pass the `*sql.Tx`, not `*sql.DB`. Without the tx, you've created a separate transaction — back to dual write. |
| "Producing to Kafka directly is fine because the change is non-critical" | Non-critical to whom? The whole point of the outbox is to remove the human judgment of which writes are "important enough" to be observable. All state-mutating writes go through it. |
| Consumer dedupes on `aggregate_id` instead of `event_id` | Two events about the same aggregate are normal (created, then renamed). Dedupe must be on `event_id`. |
| Outbox row payload includes only the ID, not the change content | Consumers shouldn't have to read back from the source table to interpret the event — that creates read-after-write hazards. Include the change content in the payload. |

## Why This Matters

The outbox pattern exists because every dual-write system, eventually, diverges. PostgreSQL might commit and Kafka might be unreachable; or Kafka might receive and the DB transaction might roll back. With both writes in one transaction, the only failure is "transaction rolled back, nothing published" — which is correct, the change didn't happen.

Once divergence sets in, downstream consumers (search index, billing counters, webhook deliveries) silently drift from the source of truth. The drift is unobservable until a customer reports a missing event or an audit finds a stale row. The cost of the drift is hours of manual reconciliation per incident, plus loss of confidence in every counter the system reports.

The outbox is a one-line discipline: write the row before you commit. Everything else is derived.
