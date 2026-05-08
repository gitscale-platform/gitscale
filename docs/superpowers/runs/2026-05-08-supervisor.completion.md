# Supervisor Wave 2 — Completion Report

**Date:** 2026-05-08
**Run prompt:** `docs/superpowers/prompts/2026-05-08-supervisor-wave2.md`
**Spec:** `docs/superpowers/specs/2026-05-08-supervisor-wave2-design.md`
**Iteration log:** `docs/superpowers/runs/2026-05-08-supervisor.log`

## Outcome

All 15 tracked open issues from the wave-2 backlog are MERGED on `main`.

| Issue | Title | PR | Plane | Priority |
|---|---|---|---|---|
| #74 | BillingClient gRPC + billing app-plane service | #87 | application | p1 |
| #75 | KeyProvider Vault HKDF wiring | #92 | workflow | p1 |
| #48 | usage_events partition gap monitoring + runbook | #93 | data | p1 |
| #76 | cmd/workflow-worker ArchiveDeps + EnsureArchiveSchedule wiring | #97 | workflow | p1 |
| #46 | Generic updated_at trigger across 5 schema domains | #90 | data | p2 |
| #47 | created_at/updated_at on identity.org_memberships and oauth_apps | #95 | data | p2 |
| #62 | OTel interceptor + resource attributes for Temporal worker | #94 | workflow | p2 |
| #63 | docker-compose Temporal dev-server entry + .env.example | #91 | meta | p2 |
| #45 | Outbox row TTL expirer per ADR-008 | #96 | workflow | p2 |
| #77 | Glue Data Catalog registration activity | #99 | workflow + data | p2 |
| #78 | Integration test for PartitionArchiveWorkflow | #98 | workflow | p2 |
| #81 | ADR-019 boundary clarification (read/DDL carve-out) | #88 | adr | p2 |
| #49 | Rename outbox consumer metric to outbox_consumer_high_water_lag_seconds | #89 | data | p3 |
| #79 | RestorePartition workflow per ADR-018 | #100 | workflow | p3 |
| #80 | Per-month DEK destruction workflow | #101 | workflow | p3 |

## Wave traversal

- **Wave 0** (no dependencies, parallelisable) — #74, #75, #48, #46, #47, #62, #63, #45, #49, #81. All merged.
- **Wave 1** (depends on #74 + #75) — #76 merged.
- **Wave 2** (depends on #76) — #77, #78, #79, #80 merged.
- **#81 special handling:** position 1 chosen (allow direct import for read/DDL with three explicit guardrails). No follow-up refactor issue filed; ADR-019 amendment shipped as PR #88.

## Notable plan deltas across the run

| Issue | Deviation | Reason |
|---|---|---|
| #74 | Migration numbered `007_billing_partition_archives.sql` (plan said 006) | `006_identity_revocation.sql` already on main |
| #74 | Postgres BillingReader/Writer impl placed at `plane/data/store/postgres/billing.go`, not `plane/data/store/billing/` | `plane/data/store/billing/` already occupied by partitioner/archiver |
| #74 | Added `plane/data/events/billing/partition_archived.schema.json` and fixture (not in plan) | Required by `make lint-events` repo convention |
| #46 | Used existing `setupPostgres(t)` helper instead of plan's hypothetical `postgrestest.NewPool` | Plan helper does not exist |
| #75 | Switched from `transit/datakey/plaintext` to `transit/hmac/<key>/sha2-256` | datakey is non-deterministic per Vault docs; HMAC is a true PRF, satisfying ADR-018's HKDF determinism |
| #62 | Split into `TemporalInterceptor` (`[]ClientInterceptor`) and `TemporalWorkerInterceptor` (`[]WorkerInterceptor`) | Temporal SDK requires distinct slice types for the two registration sites |
| #62 | Pinned `go.temporal.io/sdk/contrib/opentelemetry@v0.6.0` | v0.7.0 transitively required `go.temporal.io/api` versions missing `cloud/cloudservice/v1` |
| #62 | Required rebase against #75-merged main; `go.mod`/`go.sum` conflicts resolved manually + go-mod-tidy fix-up commit | Both #75 (Vault) and #62 (OTel) added top-level go.mod requires; conflict-resolution required `go get` re-add of #62 deps after stripped during conflict |
| #80 | Defect found and fixed during integration: `transit` `/trim` requires both `min_decryption_version` AND `min_encryption_version` to be set; fake-Vault unit suite missed the constraint | Real Vault enforces it (HTTP 400). Safe because workflow iterates `(year, month)` ascending |

Each delta preserved the plan's intent, not its literal text — exactly the
contract documented in the supervisor prompt.

## Supervisor protocol observations

- **Brainstorm-then-implement loop worked.** ONE brainstorm per iteration paced
  the design queue without overwhelming the user, and the subagent dispatch
  cap (4 concurrent in-flight branches) kept review-pressure tractable.
- **Spec+plan files committed locally** to `main` before each impl branch
  forked, then dropped automatically by `git pull --rebase` once the
  subagent's branch (which copied the same files into its worktree) was
  squash-merged. This is the only practical way to keep the docs adjacent
  to the code that implements them while avoiding direct pushes to `main`.
- **Stop-hook re-firing** during multi-iteration waits is non-destructive
  but iteration-budget-expensive; merging in-flight PRs as soon as CI green
  was the dominant lever for staying within budget.
- **Single bottleneck:** the very first `#74` impl took ~24 minutes
  (10-task plan, full proto + schema + service + grpc + cmd binary +
  appclient + tests). Once #74 cleared, parallelism took over.
- **#80 subagent kill** on a shell-guard during `git log` was a non-fatal
  issue; re-dispatching with explicit "the work is partly done; finish
  push + PR" instructions completed the task in one further pass.

## Worktree state at termination

```
$ git worktree list
/home/mitta/clients/gitscale/repos/gitscale-platform/gitscale  <main HEAD>  [main]
```

No feat/chore/docs/test branches outstanding. No open PRs authored by the
supervisor account. All issues #45..#49, #62, #63, #74..#81 closed.

## Open architecture questions (untouched, per scope)

These remain open and unaffected by Wave 2:

- Erasure coding library: ISA-L vs Reed-Solomon Go (June 2026)
- MCP server protocol version target (July 2026)
- PR reputation model: rule-based vs ML-based (July 2026)
- AGENTS.md schema versioning policy (July 2026)
- Cross-org dedup feature-flag default (August 2026)

## Notes for the next wave

- ADR-019's boundary amendment (PR #88) introduced a static-check
  requirement: "no `Transact*`/`Write*`/`Insert*`/`Update*`/`Delete*` method
  reachable from `plane/workflow/*` via `plane/data/store/*`". A small
  follow-up PR should add an `internal/architecture/...` test that asserts
  this; not done in this wave.
- `cmd/workflow-worker/main.go` now hosts substantial boot-time wiring
  (OTel + outbox-TTL expirers + archive deps + DEK destruction). At some
  point this will benefit from extraction into a `plane/workflow/wiring`
  package; cosmetic only.
- The docker-compose `temporal` healthcheck used `tctl` which is unhappy in
  some local Docker DNS configurations (#63 caveat). Consider replacing
  with `wget`/`curl` against `/health` once `temporal-ui` exposes a
  liveness endpoint.

---

Run completes per spec §13 termination criteria: all 15 issues MERGED, no
open supervisor PRs, only the primary worktree remains, this completion
report committed.
