# Multi-Topology Test Harness — Design Spec

**Issue:** [#132](https://github.com/gitscale/gitscale-platform/issues/132)
**Status:** Proposed
**Date:** 2026-05-09
**Authors:** Neeraj Mittal (drafted with expert agent review: adr-historian, architect-review, test-automator, performance-engineer)

---

## 1. Problem

Today the repo has two test tiers — unit (`go test ./...`) and integration (`//go:build integration`, single-node compose). This catches logic and contract bugs but cannot exercise the load-bearing distributed invariants in `CLAUDE.md` and the ADRs:

- ADR-008 outbox idempotency under PostgreSQL replica lag
- 2-of-3 quorum write on hot-tier storage (CLAUDE.md §Storage tiering)
- Kafka durability under broker loss (`min.insync.replicas`)
- Plane independence (Principle 2 — no cascading failure across plane boundaries)
- Polling outbox consumer fanout to N idempotent consumers

We need a single repository-of-truth test strategy that exercises GitScale across three deployment topologies — single-node, 3-node quorum, plane-multiplexed full — with both functional and performance scenarios, all runnable on a single physical host (developer laptop or CI runner).

## 2. Non-goals

- Absolute SLO validation (requires multi-host infra; tracked separately)
- Geo-distributed scenarios
- Full (10,4) Reed-Solomon reconstruction proof — needs ≥14 hosts; only degraded read is provable here
- E2E browser tests (no UI repo)
- Per-PR perf gating — perf is nightly-only

## 3. Topologies

### 3.1 Why three

| Topology | Proves | Compose target |
|---|---|---|
| **single** | Logic, contracts, schema, fast feedback | `test/topology/single/compose.yaml` |
| **quorum** (3-node) | Outbox under replica lag, 2-of-3 quorum, Kafka ISR, Temporal HA, failover | `test/topology/quorum/compose.yaml` |
| **full** (plane-multiplexed) | Plane isolation, cross-plane blast-radius containment, polling outbox at fanout, EC degraded read | `test/topology/full/compose.yaml` |

### 3.2 Reframing "9-node" as plane-multiplexed full

The issue originally framed the third topology as "8-9-node". Node count is incidental — the real signal is **one container per plane** so cross-plane blast radius is observable. Calling it "9-node" overstates fidelity (9 hosts cannot prove (10,4) RS reconstruction). The topology name is `full`; the container count varies with how many planes × how many quorum members each plane gets.

Concretely `full` runs:

- 3× postgres (quorum)
- 3× kafka brokers + 3× zookeeper/kraft
- 3× temporal frontend + matching/history
- 1× edge (Envoy)
- 1× git plane (Gitaly)
- 1× application plane
- 1× workflow plane
- 1× redis (single replica is fine for cache class)
- toxiproxy + pumba

### 3.3 What 3-node already covers

If `quorum` passes with chaos and replica lag injected, `full`'s marginal value is plane-boundary regression and outbox-consumer-fanout. Run `full` nightly, not per-PR.

## 4. Build-tag taxonomy (two-axis)

The original proposal mixed axes (`integration`, `topology_quorum`, `topology_full`, `perf`, `chaos`). `integration` is currently used as the catch-all "not unit", which overlaps every topology tag. Two-axis fixes the collision.

### 4.1 Topology axis (mutually exclusive)

| Tag | Meaning |
|---|---|
| (none) | Unit test |
| `topo_single` | Runs against `test/topology/single` |
| `topo_quorum` | Runs against `test/topology/quorum` |
| `topo_full` | Runs against `test/topology/full` |

### 4.2 Kind axis (orthogonal, multi-select)

| Tag | Meaning |
|---|---|
| (none) | Functional |
| `perf` | Performance regression scenario |
| `chaos_link` | Network-fault chaos (toxiproxy + netem) |
| `chaos_blast` | Process-fault chaos (pumba kill/pause) |

`chaos` is split into `chaos_link` (credible: link control via toxiproxy/netem) and `chaos_blast` (partially credible: shared kernel dilutes blast-radius proof). Documented caveat in the spec, not an apology in test code.

### 4.3 Composition rule

`go test -tags=integration,topo_quorum,chaos_link ./test/scenarios/quorum/...` selects functional + chaos_link tests targeting quorum.

Every scenario file MUST declare both axes explicitly:

```go
//go:build integration && topo_quorum
```

A `lint-test-tags` Make target rejects files that declare a kind tag without a topology tag.

### 4.4 Migration of existing `//go:build integration`

`integration` remains the umbrella "needs Docker" tag. Existing tests are tagged `//go:build integration && topo_single` in a single mechanical pass (sub-issue 2). The acceptance criterion "existing `//go:build integration` tests continue to pass unchanged" is satisfied because the existing build constraint stays — we only **add** the topology axis.

## 5. Single-host fidelity — what we can and cannot prove

We document this prominently so green CI is not mistaken for production-readiness.

### 5.1 Provable on single host

- Logic and contract correctness across all planes
- Outbox idempotency under simulated replica lag (`tc netem` on the postgres-replica veth)
- 2-of-3 quorum write semantics (one of three postgres containers killed via pumba)
- Kafka publish under simulated broker loss
- Temporal HA failover
- Polling outbox consumer fanout to ≥4 idempotent consumers (search, webhooks, billing, audit) with `event_id` dedup
- Plane network-policy contract: e.g. `plane/workflow` cannot reach `plane/data` postgres directly (negative test on docker network)

### 5.2 NOT provable on single host

| Invariant | Why not | Tracked where |
|---|---|---|
| Real fsync durability | Shared kernel page cache; loop devices lie the same way | Real-infra epic (TBD) |
| Gray-failure asymmetry (slow-but-not-dead) | Single NIC, symmetric netem only | Real-infra epic |
| Lease/fencing-token correctness under clock skew | `LD_PRELOAD` faketime does not affect kernel timers | Real-infra epic |
| Torn-write / power-loss recovery | No real disk | Real-infra epic |
| (10,4) Reed-Solomon full reconstruction | Need ≥14 hosts | Real-infra epic |
| Absolute SLO validation | Single-host throughput is meaningless | Real-infra epic |
| Multi-tenant class isolation under independent network sources | Loopback shares NIC + scheduler | Real-infra epic |

`docs/design.md` will carry a one-paragraph reference to this list so contributors don't claim more than the harness proves.

## 6. Latency / chaos primitives

- **`tc qdisc netem`** per veth — fixed 1ms intra-AZ default; sweep deferred to L5 perf
- **`cpus:` / `mem_limit:`** per container — observable contention
- **Per-node loop devices** — fsync isolation (best-effort; see §5.2)
- **`faketime`** — Temporal/saga timeout edges (best-effort; userspace only)
- **Toxiproxy** — partition / latency / bandwidth narrowing (network plane)
- **Pumba** — kill-node / pause-node (process plane)

Toxiproxy + pumba both: they compose cleanly (toxiproxy owns the network plane, pumba owns the process plane) and using only one loses either link-level or process-level control.

## 7. Performance gate (L5)

### 7.1 Framing

Perf is a **regression smoke gate**, not an SLO gate. It runs nightly only, never per-PR.

### 7.2 Methodology

- Open-loop fixed-rate arrival at 60–70% of measured single-host capacity
- k6 with `constant-arrival-rate` executor; ghz with `--rps` not `--concurrency`
- 30s warm-up discarded, 3–5 min steady-state measurement
- Closed-loop throughput is rejected (coordinated omission masks tail latency)

### 7.3 Statistical gate, not fixed thresholds

GitHub-hosted runners have p99 CV 20–40% — fixed thresholds will flap and get muted within a month.

- 5 runs per commit minimum
- Compare against rolling-median of last 7 nightly main-branch medians
- Mann-Whitney U test, α=0.01
- A regression fails the gate only if 2-of-3 reruns confirm it

### 7.4 Required runner

L5 perf jobs MUST run on **self-hosted, pinned-CPU runners** with `cpuset` and `intel_pstate=performance`. GHA shared runners are explicitly disallowed for L5.

### 7.5 Budget format

`budgets/perf.yaml` schema:

```yaml
schema_version: 1
scenarios:
  - name: repo_clone_small
    workload:
      executor: constant-arrival-rate
      rate_rps: 200
      duration: 5m
      warmup: 30s
    topology: topo_single   # or topo_quorum
    runs_per_commit: 5
    baseline:
      source: rolling-median
      window_runs: 7
    tolerances:
      p50_ms:   { rel_pct: 15, abs_floor_ms: 5  }
      p99_ms:   { rel_pct: 25, abs_floor_ms: 20 }
      err_rate: { abs_max: 0.001 }
    gate:
      test: mann-whitney-u
      alpha: 0.01
      min_runs: 5
    required_runner: self-hosted-perf
```

Load-bearing fields: `runs_per_commit`, `baseline.source`, `tolerances.{p50,p99}.{rel_pct, abs_floor}`, `required_runner`. Without these four, the gate is theatre.

### 7.6 Working-set discipline

Pin working-set-to-RAM ratio (`working_set_gb: 2× container_mem`) so the test stays in the regime that exercises the storage path, not the page cache.

### 7.7 What the perf gate does NOT prove

The original issue claimed L5 could prove "agent vs human traffic class isolation (Principle 1)". This claim is removed. Loopback shares NIC, scheduler, and page cache — class isolation under contention requires multi-host load gen with independent network sources. The L5 gate is reframed as a **smoke test for class labelling**, not isolation.

## 8. CI tiers

| Tier | Trigger | Topology | Runner | Time budget |
|---|---|---|---|---|
| L1 unit | every push | none | GHA std | 60s |
| L2 integration | every push | `topo_single` | GHA std | 5 min |
| L3 quorum | merge to main + nightly | `topo_quorum` + chaos | GHA std (4-core) | 20 min |
| L4 full | nightly | `topo_full` | GHA std (4-core) | 60 min |
| L5 perf | nightly + tag | `topo_single` OR `topo_quorum` | **self-hosted-perf** | 30 min |

L3 was originally per-PR; downgraded to merge-to-main + nightly because chaos tests at PR-time will retrain reviewers to ignore failures.

## 9. Repository structure

```
test/
  topology/
    single/compose.yaml                 # 1× pg, 1× kafka, 1× temporal, etc.
    quorum/compose.yaml                 # 3× pg, 3× kafka, 3× temporal, netem, toxiproxy
    full/compose.yaml                   # plane-multiplexed: 1 container per plane + quorums
    common/                             # shared service definitions (extends:)
  scenarios/
    functional/                         # tag: integration && topo_single
    quorum/                             # tag: integration && topo_quorum
    full/                               # tag: integration && topo_full
    perf/                               # tag: integration && perf && (topo_single|topo_quorum)
    chaos_link/                         # tag: integration && chaos_link && topo_quorum
    chaos_blast/                        # tag: integration && chaos_blast && topo_quorum
  fixtures/
    repos/{small,medium,large}/         # git bundles, not full directories
    seed/                               # deterministic generator scripts, not raw .sql dumps
  budgets/
    perf.yaml                           # see §7.5
  Makefile.topo                         # included by root Makefile
```

### 9.1 Fixture discipline

- Golden repos as `git bundle create` output (binary-stable, diff-friendly): <5 MB / <50 MB / <200 MB for small/medium/large
- SQL seeds as a deterministic generator with a fixed PRNG seed and pinned schema version; CI caches the generated dump as a workflow artifact keyed on the script's SHA. **Never check in raw 10M-row `.sql`**.

### 9.2 Port allocation (static)

| Topology | Postgres | Kafka | Temporal | Redis | Other |
|---|---|---|---|---|---|
| single | 5432 | 9092 | 7233 | 6379 | (single port each) |
| quorum | 5440-5442 | 9100-9102 | 7240-7242 | 6390 | netem control 8474, toxiproxy 8475 |
| full | 5450-5452 | 9110-9112 | 7250-7252 | 6395 | per-plane 8500-8520 |

Static ports beat random-port (port `0`) — log correlation across topologies stays sane.

### 9.3 Container lifecycle

`make topo-up-{single,quorum,full}` runs `docker compose down -v --remove-orphans` as a **pre-step** (post-step is skipped on failure). Volumes are named with the topology prefix and pruned on pre-step.

## 10. Sub-issues to spawn

Issue #132 itself lands the spec, ADR amendments, scaffolding, and sub-issue scaffolding. The following sub-issues do real implementation:

1. **`[Meta] Topology compose files — single/quorum/full + Make targets`** — concrete compose YAML, healthchecks, Make targets
2. **`[Meta] Build-tag taxonomy migration — add topo_single to existing integration tests`** — mechanical pass + `lint-test-tags` Make target
3. **`[Meta] Toxiproxy + netem latency injection harness`** — Go test helpers wrapping toxiproxy API, netem qdisc setup
4. **`[Meta] Pumba kill-node chaos primitives`** — Go test helpers for `chaos_blast`
5. **`[Meta] Perf regression gate — k6/ghz harness + budgets/perf.yaml`** — gate runner, statistical comparison, baseline storage
6. **`[Meta] CI tiers L3/L4/L5 — GH Actions wiring`** — workflow files, self-hosted runner provisioning doc
7. **`[Meta] Quorum scenarios — outbox-under-lag, 2-of-3 quorum write, Kafka ISR`** — first real `topo_quorum` scenarios
8. **`[Meta] Full-topology scenarios — plane isolation + polling-outbox fanout`** — first real `topo_full` scenarios

## 11. Acceptance for #132 itself

- [ ] ADR-004 amended with Kafka durability invariants (RF=3, `min.insync.replicas=2`, `acks=all`)
- [ ] ADR-008 amended with outbox max-tolerable-replica-lag SLO
- [ ] This spec landed at `docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md`
- [ ] Cross-reference section added to `docs/design.md`
- [ ] `test/topology/{single,quorum,full}/` directories created with placeholder `compose.yaml` and per-dir README explaining intent
- [ ] `make topo-up-single`, `make topo-up-quorum`, `make topo-up-full` Make targets added (stubs that exit with a "not yet implemented — see issue N" message until sub-issues land)
- [ ] `make lint-test-tags` target added (rejects files with kind tag but no topology tag)
- [ ] 8 sub-issues opened on GitHub with this spec linked

## 12. Open questions resolved

| Question | Resolution |
|---|---|
| Inter-node latency profile (fixed vs sweep) | **Fixed 1ms intra-AZ.** Sweep belongs in L5 perf, not functional chaos |
| Erasure coding simulation on 9 hosts | **Skip.** Only degraded-read provable; full RS reconstruction needs ≥14. Add a clearly-named `t.Skip` in the topology with a link to the real-infra tracking issue |
| Perf budgets in-repo YAML vs external service | **In-repo YAML.** External service is operational overhead that doesn't pay off until SLO gates exist (out of scope) |
| Chaos lib choice | **Toxiproxy + pumba.** Network plane + process plane both required; using only one loses control of the other axis |
| Migration of existing integration tests | **Coexist initially, migrate in sub-issue 2.** Add `topo_single` to existing tests; clean cut leaves nothing behind because we only add a tag |

## 13. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Single-host green CI mistaken for production-ready | §5.2 invariant table referenced from `docs/design.md` |
| Perf gate flapping on GHA shared runners | Self-hosted-perf runner required (§7.4); statistical gate (§7.3); 2-of-3 rerun confirmation |
| Tag-axis collision with existing `integration` | Two-axis taxonomy (§4); `lint-test-tags` Make target rejects misuse |
| Chaos test flake > 0.5% | Assert observable state (outbox drain event, workflow signal), not sleeps; toxiproxy latency ≥3× polling interval |
| Sub-issue scope creep | Each sub-issue ships a slice that compiles and tests pass standalone |

## 14. Cross-references

- ADR-001 (storage tiers), ADR-004 (Kafka), ADR-008 (outbox), ADR-009 (Redis cache)
- `CLAUDE.md` Principle 1 (agents primary traffic), Principle 2 (loose coupling)
- `docs/architecture.md` §7.6 (failure scenarios), §8 (ADR registry)
