# Spec: ADR-019 — Workflow plane state mutations route via application-plane RPC

**Date:** 2026-05-06
**Status:** Proposed (ready to file as ADR-019 in `docs/architecture.md §8`)
**Branch:** `adr/workflow-app-plane-boundary`
**Blocks:** #33 (workflow bootstrap) — #33 cannot merge until this ADR lands.

## 1. Why this is an ADR, not a PR-description gap-fill

Per CLAUDE.md: *"If a proposal fills in an implementation detail not covered by any ADR, no new ADR is required — a PR description is sufficient."*

This rule is **not** a gap-fill. It is a load-bearing plane-boundary contract that:

1. Will affect every workflow PR henceforth (#18, future agent-session, future CI pipeline workflows).
2. Constrains imports across two planes (`plane/workflow/` and `plane/application/`) for the project lifetime.
3. Has a defensible alternative (workflow plane writes directly via `MetadataStore.Transact`) that ADRs must explicitly reject so the rejected path doesn't sneak back in.
4. Architect review and adr-historian review both flagged it as needing ADR-level governance.

CLAUDE.md two principles affected: (2) loose coupling at every seam — formal boundary contract; (3) metering is infrastructure — the boundary is what lets metering attach to write paths uniformly.

## 2. Proposed ADR text

The text below drops verbatim into `docs/architecture.md §8` as ADR-019.

---

### ADR-019: Adopted application-plane RPC as the only state-mutation path from the workflow plane

- **Status:** Proposed
- **Date:** 2026-05-06
- **Context:** Temporal workflows in `plane/workflow/` need to mutate metadata-layer state — partition rollover, agent-session lifecycle, CI pipeline state transitions, future automated remediation. Two architectural paths exist:
  1. **Direct path.** Workflow activities receive `plane/data/store.MetadataStore` and call `Transact` + `WriteOutbox` themselves.
  2. **App-plane RPC path.** Workflow activities receive a thin gRPC client (`plane/workflow/appclient/`) into the application plane's per-domain service; the service performs the Tx + outbox write.

  The direct path is technically permitted by the existing interfaces. ADR-008 (outbox) is preserved either way, because both paths converge on `MetadataStore.Transact` + `WriteOutbox` in the same Tx. The choice is **not** about transactional integrity; it is about where domain invariants live.

- **Decision:**

  Workflow plane state mutations route exclusively via per-domain gRPC services in `plane/workflow/appclient/` → `plane/application/<domain>/`. The direct path (workflow activities calling `MetadataStore.Transact` for writes) is forbidden.

  Workflow plane reads MAY use `plane/data/store` interfaces directly via the activity adapter. Pure-DDL maintenance activities (e.g. partition rollover) MAY use `MetadataStore` directly, because the operation has no outbox row and no domain invariant.

  No workflow activity writes more than one domain's outbox row in a single execution. Cross-domain workflows compose per-domain saga steps with explicit compensation activities.

- **Consequences:**

  **Domain invariants live in the application plane only.** `CreateUser` validation (uniqueness, email shape), reputation clamping, event-type selection, payload assembly are not duplicated across `plane/application/identity/` and `plane/workflow/<domain>/activities/`. A domain rule change ripples through one codebase, not two.

  **Auth / audit context is uniform.** The application plane stamps `actor_id`, `principal_kind`, `rate_bucket` on every outbox payload from JWT-SVID claims (ADR-010). A workflow activity has no human principal; routing through the app plane gives the workflow's *system* SPIFFE identity a clear audit trail (`actor_kind=service`, `actor_id=<workflow-worker-spiffe>`).

  **Single-writer per aggregate keeps schema migrations sane.** Adding a column to `agent_identities` requires updating one writer, not two.

  **Workflow tests gain a clean stub surface.** `appclient.IdentityClient` is mockable; activities test against the interface, not against a postgres testcontainer.

  **Latency cost: one network hop per write activity.** Acceptable. Temporal activities are async and retry-tolerant; this is not the latency-sensitive path.

  **Operational cost: each app-plane domain ships a gRPC server.** Identity (#15-revocation) is the first; future PR-engine, CI-state, billing-aggregator services follow the same pattern. The cost is amortised across human-facing API traffic that needs gRPC anyway.

  **Read-only fast path preserved.** Read activities skip the gRPC hop and call `MetadataStore` directly through the adapter — the rule does not penalise read-mostly workflows.

  **Pure-DDL exception is narrow.** `CreatePartition` (#18) writes no row, emits no outbox, has no invariant. The single-domain rule still applies: a DDL activity touches one schema only.

  **Cross-domain saga rule prevents distributed-Tx daydreams.** Two-phase commit across `identity_outbox` + `billing_outbox` is forbidden. A "create agent + provision billing account" workflow becomes two activities (`identityClient.CreateAgent` then `billingClient.ProvisionAccount`) with a compensation activity on failure.

---

## 3. Enforcement

| Mechanism | Location | What it catches |
|---|---|---|
| Code review | every workflow PR | direct `MetadataStore` write from activity |
| `gitscale-plane-boundary` skill | hook on edits to `plane/workflow/**/*.go` | `MetadataStore.Transact` import in activity files (`activity*.go`) |
| `gitscale-outbox-check` skill | hook on outbox writes | dual-write paths |
| Lint (future) | `plane/workflow/lint/` | `grep` for `Transact(` in activity files outside `*_readonly_*.go` allowlist |

The lint script can ship in a follow-up PR after #33; for the bootstrap PR, code review + the skill hook are sufficient.

## 4. Rejected alternatives

### Alt 1 — Direct path (workflow plane writes via `MetadataStore.Transact`)

**Pros:** Simpler, one fewer service to deploy, no gRPC layer, lower latency.

**Rejected because:**

- Domain invariant duplication is the root cause of "two implementations of `CreateUser` drift in 6 months" anti-pattern.
- Auth / audit context becomes ad-hoc — workflow has to manually stamp fields the app plane fills automatically.
- Schema migrations now have two writers to update.
- The latency win is illusory — Temporal activities have heartbeat overhead measured in tens of milliseconds; one local gRPC hop adds <1ms in a co-located cluster.
- The "simpler" claim is wrong: the simpler architecture is the one with one writer per aggregate, not the one with fewer service binaries.

### Alt 2 — Hybrid (some domains direct, some via RPC)

**Rejected because:** rule complexity. Engineers must remember which domains are direct-writable; mistakes are hard to catch in review. A uniform rule with one narrow DDL exception is easier to enforce.

### Alt 3 — Workflow plane has its own outbox tables, separate from app plane

**Rejected because:** doubles the polling-consumer surface (two outbox tables per domain), invents a new aggregate type for "workflow-initiated change," and breaks ADR-008's "single source of truth per aggregate" principle.

## 5. Out-of-scope clarifications

- **Read-only activities** are NOT covered by this ADR. They MAY import `plane/data/store` interfaces directly. Distinction: a method that does not call `Transact` or `WriteOutbox` is read-only.
- **Activity-to-activity orchestration** within the workflow plane is unaffected. Activities can call other activities through the workflow context; the rule governs *external state mutations*.
- **`cache.Set`** from a workflow activity is permitted — cache writes are not authoritative state and have no outbox.
- **Metering counters** (Phase-2) — the rule applies. Workflow-initiated metering events go through the metering app-plane service, not directly into ClickHouse / Redis enforcement counters.

## 6. Migration guidance

This ADR is filed before any workflow code exists, so there is no migration. Future workflow PRs that try the direct path are caught at code review or by the boundary skill.

If a future workflow needs an aggregate-level invariant (rare), the resolution is to add a method to the app-plane domain service, not to bypass the rule.

## 7. Implementation plan

This ADR ships as a documentation-only PR.

### Files to modify

- `docs/architecture.md` — append ADR-019 text (§ 2 above) after ADR-017 in §8.
- `docs/architecture.md` §8 ADR-008 — add a one-line cross-reference: *"See ADR-019 for the workflow-plane variant of this invariant."*
- `docs/architecture.md` §8 ADR-017 — add a one-line cross-reference: *"Workflow-plane consumers of these interfaces follow ADR-019 routing."*
- `.claude/skills/gitscale-plane-boundary/SKILL.md` (if the skill has a doc — verify) — add ADR-019 to the reference list so the skill cites it on workflow-plane edits.
- `.claude/skills/gitscale-outbox-check/SKILL.md` — add ADR-019 to the reference list.

### Acceptance criteria

- [ ] ADR-019 appears in `docs/architecture.md §8` with `Status: Proposed`.
- [ ] `make lint-md` introduces zero NEW errors in the modified range.
- [ ] ADR-008 and ADR-017 carry the cross-reference line.
- [ ] Skill references updated.

### Promotion to `Accepted`

After #33 merges and at least one workflow (`#18-rollover`) ships against the rule, the ADR status flips from `Proposed` to `Accepted` in a follow-up doc PR.

## 8. Risk mitigations

| Risk | Mitigation |
|---|---|
| ADR text drifts from #33's actual implementation | #33 PR description cross-links ADR-019; reviewer compares wording |
| Future engineer reads ADR-019 and decides "my case is the exception" | "Pure-DDL exception is narrow" wording is explicit; `CreatePartition` is the only example. Adding a new exception requires an ADR amendment. |
| Single-domain rule blocks a legitimate cross-domain need | Saga pattern is the documented escape hatch; ADR-019 §Consequences names it |
| ADR-019 conflicts with future ADR-007 (MCP) when MCP server proxies workflow tools | MCP server is in the application plane; tool calls go through it the same way. No conflict. |

## 9. Cross-references

- ADR-003 (Temporal) — establishes activities as the I/O boundary; ADR-019 specifies *which* I/O.
- ADR-008 (outbox) — preserved by both paths; ADR-019 picks the path that keeps domain invariants single-sourced.
- ADR-010 (SPIFFE/JWT-SVID) — the auth context that flows through the app-plane gRPC hop.
- ADR-015 (plan approval) — `ApprovalActivity` slot in #33 registry uses the app-plane RPC pattern.
- ADR-017 (interface swap surface) — `MetadataStore` and `CacheStore` remain the underlying swap surfaces; ADR-019 governs *who calls them from where*.
- CLAUDE.md core principles 2 (loose coupling) and 3 (metering as infrastructure).
- `gitscale-plane-boundary` skill, `gitscale-outbox-check` skill — enforcement.
