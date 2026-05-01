---
name: gitscale-adr-guard
description: Use when editing GitScale code, schema, infra, or design docs and the change might contradict an existing ADR. Triggers on edits that touch storage tiering, event bus, plane boundaries, identity/auth, search backend, CI isolation, or anything referenced in docs/architecture.md §8. Also triggers when the user asks to "check ADR impact", "is this an ADR change", or fills the PR template's ADR-impact box. Catches silent ADR drift before merge — the cost of a contradicting commit landing is high (architectural integrity, retrospective reasoning).
---

# GitScale ADR Guard

## Overview

Architecture Decision Records (ADRs) live in `docs/architecture.md §8` and capture binding choices: PostgreSQL for metadata, Gitaly for Git RPC, outbox-based event consistency, hot/cold storage tiering, SPIRE/SPIFFE for service identity, Vespa for search, Firecracker for CI isolation, etc.

A contradicting code change without ADR amendment creates silent drift — future readers see an ADR claiming X while code does Y. Guard against this *at edit time*, not at review time.

**Core principle:** Code that contradicts an ADR must either (a) match the ADR or (b) ship together with an ADR amendment in the same PR. Silent contradiction is the failure mode this skill prevents.

## When to Use

Trigger when **any** of these are true for a pending edit:

- Touches `plane/data/**` (PostgreSQL schema, Kafka topology, Redis config)
- Touches `plane/git/**` (Gitaly client wrappers, storage tier code, pack negotiation)
- Touches `plane/edge/**` (Envoy WASM filters, identity resolution, token metering)
- Touches `plane/workflow/**` (Temporal worker, workflow definitions)
- Adds or modifies search code (Vespa, Qdrant)
- Adds or modifies identity/auth (SPIRE, SPIFFE, JWT-SVID)
- Adds or modifies CI runner isolation (Firecracker)
- User asks "does this affect ADRs?", "ADR impact?", "check ADRs", or completes the PR template ADR-impact section

**Don't trigger** for: pure test additions, doc typo fixes, formatting-only changes, dependency version bumps without behavior change.

## Workflow

1. **Read the ADR list.** Open `docs/architecture.md`, jump to §8. If the file is missing, the repo is too early — flag this and stop.
2. **Map the diff to ADRs.** For each modified file/symbol, identify which ADRs (if any) constrain it. Use the table in [references/adr-map.md](references/adr-map.md) as a starting index.
3. **Classify the change.**
   - **Conforming:** Change matches the ADR. Proceed.
   - **Filling a gap:** No ADR covers this detail. PR description sufficient — no new ADR needed.
   - **Amending:** Change extends an ADR within the spirit of the original decision. Update the ADR in the same PR.
   - **Contradicting:** Change reverses an ADR's decision. Stop. Open a `type/adr` issue first; do not merge code that pre-empts the discussion.
4. **Write the verdict.** Output a one-paragraph verdict naming the ADRs touched, the classification, and the next step.
5. **If contradicting:** Provide the issue body (problem, why ADR is now wrong, proposed replacement) so the user can open the issue immediately.

## Quick Reference

| Diff signal | Likely ADR cluster |
|---|---|
| `CREATE TABLE`, `ALTER TABLE` in `plane/data/migrations/` | ADR-006 (PostgreSQL), ADR-008 (outbox) |
| Kafka producer/consumer, `outbox` table | ADR-008 (event consistency) |
| Erasure coding, replication factor, `s3://` writes | ADR-001 (storage tiering), ADR-011 (cold-tier encryption) |
| `gitaly.Client`, raw `git` exec | (pending ADR — Gitaly RPC layer) |
| `spiffe`, `spire`, JWT-SVID, mTLS cert | ADR-010 (service identity) |
| `qdrant`, `vespa`, embedding, `cosine` | ADR-016 (search backend) |
| `firecracker`, `runc`, `gvisor`, container exec | ADR-002 (CI isolation) |
| `envoy` filter, WASM module, edge token meter | (pending ADR — edge plane) |
| `temporal.Workflow`, `workflow.Execute` | ADR-003 (workflow orchestration) |
| GraphQL resolver, schema field, persisted query | ADR-013 (GraphQL named-subset) |
| Plugin registration, plugin sandbox, `Scorer`/`SignalSource`/`ReputationProvider` | ADR-014 (plugin governance) |
| Approval policy, plan-hash gating, `ApprovalActivity` | ADR-015 (plan-approval policy) |
| Gitaly hook metering, two-tier counter, reconciliation | ADR-012 (Gitaly hook metering) |

## Output Format

Return a single block with this shape:

```
ADR-impact: <none | conforming | gap | amending | contradicting>
ADRs touched: <ADR-NNN, ADR-MMM | none>
Reasoning: <one sentence per ADR — what the diff does vs. what the ADR says>
Next step: <proceed | update ADR-NNN in this PR | open type/adr issue first>
```

If `contradicting`, append a draft issue body under a `## Issue draft` heading.

## Example

**Input diff (excerpt):**

```go
// plane/git/storage/hot_tier.go
func (h *HotTier) Write(ctx context.Context, obj []byte) error {
    return h.encoder.EncodeReedSolomon(obj, 10, 4) // (10,4) RS
}
```

**Verdict:**

```
ADR-impact: contradicting
ADRs touched: ADR-001
Reasoning: ADR-001 mandates 3× synchronous replication on hot tier; diff applies
  (10,4) Reed-Solomon erasure coding which is reserved for cold tier per
  the same ADR.
Next step: open type/adr issue first; do not merge.
```

```
## Issue draft
**Problem:** plane/git/storage/hot_tier.go applies (10,4) RS to hot writes.
**Why ADR is now wrong (or isn't):** ADR is correct — small random reads on
  hot data make RS reconstruction prohibitively expensive. Code is wrong.
**Proposed action:** revert to 3× sync replication; route RS to cold tier writer.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| Treating "the change is small" as a reason to skip the check | Small contradictions are the silent ones. Run the check anyway. |
| Confusing "filling a gap" with "amending" | Gap = ADR silent on this detail. Amend = ADR speaks, change extends it. If unsure, treat as amending. |
| Writing a verdict without naming a specific ADR number | Forces precision. If you can't name the ADR, the mapping in references/adr-map.md is incomplete — fix it. |
| Merging the contradicting code while opening the issue "in parallel" | The whole point is to discuss before code lands. Don't pre-empt. |

## Why This Matters

ADRs are the project's binding architectural memory. The five-plane separation, agent-first traffic class, and metering-as-infrastructure principles are upheld by ADRs, not by code review vibes. When an ADR drifts from code, every future decision built on the ADR's reasoning is on sand. Catch the drift in the diff window, not six months later.
