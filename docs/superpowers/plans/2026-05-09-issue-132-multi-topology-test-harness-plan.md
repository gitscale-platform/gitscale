# Multi-Topology Test Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land issue #132's umbrella deliverable — design spec, ADR amendments, repository scaffolding, and 8 sub-issue stubs — so subsequent sub-issues have a stable foundation to implement against.

**Architecture:** Pure scaffolding + docs PR. No test code or compose contents land here; sub-issues do that. This PR creates: amended ADRs, spec doc, `docs/design.md` cross-reference, `test/topology/{single,quorum,full}/` skeletons, Make target stubs that fail loudly, lint target, and 8 GitHub sub-issues linked back to the spec.

**Tech Stack:** Markdown only (docs), Make (stubs), Bash (lint script), `gh` CLI (sub-issue creation). No Go code in this PR.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md`

**Branch:** `feat/meta-multi-topology-test-harness-scaffold`

---

### Task 1: Amend ADR-004 (Kafka durability invariants)

**Files:**
- Modify: `docs/architecture.md` (ADR-004 block, ~line 487-501)

- [ ] **Step 1: Add durability invariants to ADR-004 Decision section**

Edit ADR-004 in `docs/architecture.md`. After the existing `Decision:` text, append the following sentence to the same paragraph (before the cross-references):

```
Brokers run replication factor 3 with `min.insync.replicas=2`; producers use
`acks=all` and idempotent producer config. A topic that cannot meet ISR=2
fails writes loudly rather than silently degrading durability.
```

Update the `Amended:` line to `2026-05-09`.

- [ ] **Step 2: Add a Consequences sentence**

Append to the Consequences paragraph of ADR-004:

```
Single-broker loss is tolerated; two-broker loss surfaces as producer failure,
preserving the no-silent-data-loss contract.
```

- [ ] **Step 3: Verify markdown lint**

Run: `make lint-md`
Expected: no errors on `docs/architecture.md`.

- [ ] **Step 4: Commit**

```bash
git add docs/architecture.md
git commit -m "docs(adr): ADR-004 — pin Kafka durability (RF=3, ISR=2, acks=all)

Issue #132 multi-topology test harness asserts these invariants;
they were previously implicit. Refs #132."
```

---

### Task 2: Amend ADR-008 (outbox replica-lag SLO)

**Files:**
- Modify: `docs/architecture.md` (ADR-008 block, ~line 526-532)

- [ ] **Step 1: Add replica-lag invariant to ADR-008 Decision section**

Edit ADR-008 in `docs/architecture.md`. Append to the existing `Decision:` paragraph:

```
The outbox consumer tolerates up to 30 s of PostgreSQL primary→replica lag
without skipping events; events past that threshold trigger a paging alert
on `outbox_replica_lag_seconds`. Read-replica reads are forbidden for the
outbox draining loop — the consumer reads the primary directly.
```

Add a line to the ADR header: `Amended: 2026-05-09`.

- [ ] **Step 2: Verify lint**

Run: `make lint-md`
Expected: pass.

- [ ] **Step 3: Commit**

```bash
git add docs/architecture.md
git commit -m "docs(adr): ADR-008 — pin outbox max replica lag (30s, primary-only read)

Issue #132 harness asserts outbox idempotency under replica lag;
the threshold was previously unstated. Refs #132."
```

---

### Task 3: Land design spec doc

**Files:**
- Already created: `docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md`

- [ ] **Step 1: Verify spec lints clean**

Run: `make lint-md`
Expected: pass.

- [ ] **Step 2: Commit spec**

```bash
git add docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md
git commit -m "docs(meta): design spec for issue #132 multi-topology test harness

Three topologies (single/quorum/full), two-axis tag taxonomy
(topo_* x {perf,chaos_link,chaos_blast}), perf as nightly regression
smoke gate on self-hosted-perf runners with statistical (MWU) gate.
Refs #132."
```

---

### Task 4: Cross-reference section in docs/design.md

**Files:**
- Modify: `docs/design.md` — insert new `## 11. Test topology strategy` section before the existing `## 10. Cross-references` section.

- [ ] **Step 1: Insert §11 before existing §10**

Open `docs/design.md`. Find the line `## 10. Cross-references` (~line 797). Immediately before it, insert:

```markdown
## 11. Test topology strategy

GitScale tests run in three topologies, all on a single host (laptop or CI
runner): `single`, `quorum` (3-node), and `full` (plane-multiplexed).
Functional and performance scenarios are tagged on two orthogonal axes —
topology (`topo_single` / `topo_quorum` / `topo_full`) and kind (`perf`,
`chaos_link`, `chaos_blast`). Performance is a nightly regression smoke
gate, not an SLO gate, and runs only on self-hosted pinned-CPU runners
with a Mann-Whitney U statistical comparison against a rolling 7-run
main-branch baseline.

Single-host green CI does NOT prove: real fsync durability, gray-failure
asymmetry, lease/fencing under clock skew, torn-write recovery, full
(10,4) Reed-Solomon reconstruction, absolute SLO, or multi-tenant class
isolation under independent network sources. Each of these is tracked
against a separate real-infra epic.

Full design: [`docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md`](superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md).
```

Then renumber the existing `## 10. Cross-references` to `## 12. Cross-references`.

- [ ] **Step 2: Verify lint and section numbering**

Run: `make lint-md && grep -n "^## " docs/design.md | tail -5`
Expected: lint passes; sections end at `## 12. Cross-references`.

- [ ] **Step 3: Commit**

```bash
git add docs/design.md
git commit -m "docs: design.md §11 test topology strategy cross-ref

Names what single-host CI cannot prove so contributors don't claim
more than the harness delivers. Refs #132."
```

---

### Task 5: Create test/topology/ skeleton

**Files:**
- Create: `test/topology/single/compose.yaml`
- Create: `test/topology/single/README.md`
- Create: `test/topology/quorum/compose.yaml`
- Create: `test/topology/quorum/README.md`
- Create: `test/topology/full/compose.yaml`
- Create: `test/topology/full/README.md`
- Create: `test/topology/common/README.md`
- Create: `test/scenarios/README.md`
- Create: `test/budgets/perf.yaml`
- Create: `test/fixtures/README.md`

- [ ] **Step 1: Create directory tree with placeholder compose files**

Each `compose.yaml` is a stub that fails loudly until its sub-issue lands. Each README points to the sub-issue.

`test/topology/single/compose.yaml`:

```yaml
# Placeholder — real contents land in sub-issue 1.
# Until then `make topo-up-single` exits non-zero with a pointer.
# This file exists only so the path is a real location reviewers can comment on.
```

`test/topology/single/README.md`:

```markdown
# Topology: single

1-node compose for L2 integration. Proves logic, contracts, schema,
fast feedback. Implemented in sub-issue 1 of #132.

See `docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md` §3, §9.
```

`test/topology/quorum/compose.yaml`:

```yaml
# Placeholder — sub-issue 1 of #132 lands real contents.
# Target: 3x postgres, 3x kafka, 3x temporal, netem, toxiproxy.
```

`test/topology/quorum/README.md`:

```markdown
# Topology: quorum

3-node compose for L3. Proves outbox-under-replica-lag, 2-of-3 quorum
writes, Kafka ISR, Temporal HA failover. Implemented in sub-issue 1.
```

`test/topology/full/compose.yaml`:

```yaml
# Placeholder — sub-issue 1 of #132 lands real contents.
# Target: plane-multiplexed (1 container per plane) over quorum services.
```

`test/topology/full/README.md`:

```markdown
# Topology: full

Plane-multiplexed compose for L4 nightly. Proves plane isolation,
cross-plane blast-radius containment, polling-outbox fanout to >=4
idempotent consumers (search, webhooks, billing, audit), EC degraded
read. NOT a 9-host topology — node count is incidental; plane-per-
container is the signal.

Implemented in sub-issue 1 of #132.
```

`test/topology/common/README.md`:

```markdown
# Shared compose service definitions

Reused by single/quorum/full via `extends:`. Populated in sub-issue 1.
```

`test/scenarios/README.md`:

```markdown
# Test scenarios

Build-tag taxonomy (two-axis):

- Topology (mutex): `topo_single`, `topo_quorum`, `topo_full`
- Kind (orthogonal): `perf`, `chaos_link`, `chaos_blast`

Every scenario file declares both axes:

    //go:build integration && topo_quorum

`make lint-test-tags` rejects files with a kind tag but no topology tag.

Layout (populated by sub-issues 7 and 8):

    functional/    integration && topo_single
    quorum/        integration && topo_quorum
    full/          integration && topo_full
    perf/          integration && perf && (topo_single|topo_quorum)
    chaos_link/    integration && chaos_link && topo_quorum
    chaos_blast/   integration && chaos_blast && topo_quorum
```

`test/budgets/perf.yaml`:

```yaml
# Placeholder. Schema and first scenario land in sub-issue 5.
schema_version: 1
scenarios: []
```

`test/fixtures/README.md`:

```markdown
# Test fixtures

- `repos/{small,medium,large}/`: golden git bundles (`git bundle create`),
  not full directories. <5 MB / <50 MB / <200 MB. Populated in sub-issue 1.
- `seed/`: deterministic generator scripts with fixed PRNG seed and pinned
  schema version. Output cached as CI workflow artifact keyed on script SHA.
  Never check in raw .sql dumps. Populated in sub-issue 7.
```

- [ ] **Step 2: Verify markdown lints**

Run: `make lint-md`
Expected: pass.

- [ ] **Step 3: Commit scaffolding**

```bash
git add test/
git commit -m "feat(meta): test/ scaffold for multi-topology harness (issue #132)

Empty topology dirs + READMEs that point at sub-issues. Compose files
are placeholders so reviewers have a stable path to comment on. Refs #132."
```

---

### Task 6: Make target stubs

**Files:**
- Modify: `Makefile`
- Create: `scripts/lint-test-tags.sh`

- [ ] **Step 1: Add Make targets to Makefile**

Open `Makefile`. Add to the `.PHONY:` line at the top: ` topo-up-single topo-up-quorum topo-up-full topo-down-single topo-down-quorum topo-down-full lint-test-tags`.

Append to the end of the file:

```makefile
# --- Multi-topology test harness (issue #132) ---
# Stubs land here in #132. Real compose contents arrive in sub-issue 1.

TOPO_DIR := test/topology

define _topo_not_ready
	@echo "ERROR: topology '$(1)' not yet implemented."; \
	echo "       See sub-issue 1 of #132 (topology compose files)."; \
	exit 1
endef

topo-up-single:
	$(call _topo_not_ready,single)

topo-up-quorum:
	$(call _topo_not_ready,quorum)

topo-up-full:
	$(call _topo_not_ready,full)

topo-down-single topo-down-quorum topo-down-full:
	@true   # no-op until topo-up exists; safe to run on fresh checkouts

lint-test-tags:
	@bash scripts/lint-test-tags.sh
```

- [ ] **Step 2: Create `scripts/lint-test-tags.sh`**

```bash
#!/usr/bin/env bash
# Reject test files that declare a kind-axis tag (perf, chaos_link, chaos_blast)
# without also declaring a topology-axis tag (topo_single, topo_quorum, topo_full).
# Issue #132 §4.3.

set -euo pipefail

KIND_TAGS='perf|chaos_link|chaos_blast'
TOPO_TAGS='topo_single|topo_quorum|topo_full'

violations=0
while IFS= read -r -d '' f; do
  head -n 5 "$f" | grep -qE "^//go:build .*($KIND_TAGS)" || continue
  if ! head -n 5 "$f" | grep -qE "^//go:build .*($TOPO_TAGS)"; then
    echo "lint-test-tags: $f declares a kind tag without a topology tag"
    violations=$((violations+1))
  fi
done < <(find test/scenarios -name '*_test.go' -print0 2>/dev/null)

if [ "$violations" -gt 0 ]; then
  echo "lint-test-tags: $violations violation(s). See test/scenarios/README.md."
  exit 1
fi
echo "lint-test-tags: ok"
```

Make it executable: `chmod +x scripts/lint-test-tags.sh`.

- [ ] **Step 3: Verify Make targets behave**

Run all four:

```bash
make topo-up-single 2>&1 | head -3
make topo-up-quorum 2>&1 | head -3
make topo-up-full 2>&1 | head -3
make lint-test-tags
```

Expected:
- First three: print `ERROR: topology '<name>' not yet implemented.` and exit code 1.
- `lint-test-tags`: prints `lint-test-tags: ok` and exits 0 (test/scenarios is empty).

- [ ] **Step 4: Commit**

```bash
git add Makefile scripts/lint-test-tags.sh
git commit -m "feat(meta): make targets topo-up-{single,quorum,full} + lint-test-tags

Stubs for #132. topo-up-* fail loudly with a pointer to sub-issue 1
until real compose lands. lint-test-tags enforces the two-axis tag
rule (kind tag without topology tag = error). Refs #132."
```

---

### Task 7: Spawn 8 sub-issues

**Files:**
- None (gh CLI only)

- [ ] **Step 1: Verify gh auth**

Run: `gh auth status`
Expected: authenticated.

- [ ] **Step 2: Create sub-issues**

Run each command and record the issue numbers:

```bash
SPEC="docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md"
PARENT=132

gh issue create --title "[Meta] Topology compose files — single/quorum/full + Make targets" \
  --label "type/design,p2" \
  --body "$(cat <<EOF
Sub-issue 1 of #${PARENT}. Land real contents for \`test/topology/{single,quorum,full}/compose.yaml\`, the shared services in \`test/topology/common/\`, healthchecks, and wire \`make topo-up-{single,quorum,full}\` to \`docker compose up -d\` (replacing the stubs from #${PARENT}).

Spec: ${SPEC} §3, §9.

Acceptance:
- [ ] All three compose files boot cleanly
- [ ] Healthchecks gate scenario execution
- [ ] Static port allocation per spec §9.2
- [ ] \`docker compose down -v --remove-orphans\` runs as pre-step
EOF
)"

gh issue create --title "[Meta] Build-tag taxonomy migration — add topo_single to existing integration tests" \
  --label "type/design,p2" \
  --body "$(cat <<EOF
Sub-issue 2 of #${PARENT}. Add \`topo_single\` to every existing \`//go:build integration\` test file. Mechanical pass; preserves existing build behavior because we only add a tag.

Spec: ${SPEC} §4.4.

Acceptance:
- [ ] All existing integration tests now declare both axes
- [ ] \`make lint-test-tags\` passes
- [ ] \`go test -tags=integration,topo_single ./...\` selects the same tests as today's \`-tags=integration\`
EOF
)"

gh issue create --title "[Meta] Toxiproxy + netem latency injection harness" \
  --label "type/design,p2" \
  --body "Sub-issue 3 of #${PARENT}. Go test helpers wrapping toxiproxy API + netem qdisc setup for \`chaos_link\` scenarios. Spec: ${SPEC} §6."

gh issue create --title "[Meta] Pumba kill-node chaos primitives" \
  --label "type/design,p2" \
  --body "Sub-issue 4 of #${PARENT}. Go test helpers for \`chaos_blast\` (kill-node, pause-node) via pumba. Spec: ${SPEC} §6."

gh issue create --title "[Meta] Perf regression gate — k6/ghz harness + budgets/perf.yaml" \
  --label "type/design,p2" \
  --body "$(cat <<EOF
Sub-issue 5 of #${PARENT}. Land the L5 perf gate runner: open-loop k6/ghz, statistical comparison (Mann-Whitney U, α=0.01), rolling-7 baseline storage, self-hosted-perf runner requirement.

Spec: ${SPEC} §7.

Acceptance:
- [ ] First scenario in \`test/budgets/perf.yaml\`
- [ ] Gate fails only on 2-of-3 reruns
- [ ] No GHA shared runners (enforced via runner label gate)
EOF
)"

gh issue create --title "[Meta] CI tiers L3/L4/L5 — GH Actions wiring" \
  --label "type/design,p2" \
  --body "Sub-issue 6 of #${PARENT}. GH Actions workflow files for L3 (merge-to-main + nightly), L4 (nightly), L5 (nightly + tag, self-hosted). Spec: ${SPEC} §8."

gh issue create --title "[Meta] Quorum scenarios — outbox-under-lag, 2-of-3 quorum write, Kafka ISR" \
  --label "type/design,p2" \
  --body "Sub-issue 7 of #${PARENT}. First real \`topo_quorum\` scenarios. Spec: ${SPEC} §5.1."

gh issue create --title "[Meta] Full-topology scenarios — plane isolation + polling-outbox fanout" \
  --label "type/design,p2" \
  --body "Sub-issue 8 of #${PARENT}. First real \`topo_full\` scenarios — plane network-policy negative tests + polling-outbox fanout to ≥4 idempotent consumers. Spec: ${SPEC} §5.1, §10."
```

- [ ] **Step 3: Record sub-issue numbers in the parent**

Run: `gh issue list --label type/design --state open --search "[Meta]" --limit 20 --json number,title`

Note the 8 new issue numbers. Then update the parent:

```bash
gh issue comment 132 --body "Sub-issues spawned per spec §10:

- 1. Compose files: #<N1>
- 2. Tag migration: #<N2>
- 3. Toxiproxy/netem: #<N3>
- 4. Pumba: #<N4>
- 5. Perf gate: #<N5>
- 6. CI tiers: #<N6>
- 7. Quorum scenarios: #<N7>
- 8. Full scenarios: #<N8>

Spec: docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md
Plan: docs/superpowers/plans/2026-05-09-issue-132-multi-topology-test-harness-plan.md"
```

(Substitute actual issue numbers for the placeholders.)

---

### Task 8: Open PR

**Files:**
- None

- [ ] **Step 1: Push branch**

```bash
git push -u origin feat/meta-multi-topology-test-harness-scaffold
```

- [ ] **Step 2: Open PR**

```bash
gh pr create --title "[Meta] Multi-topology test harness — design spec + scaffold (#132)" \
  --body "$(cat <<'EOF'
## Summary

Lands the umbrella deliverable for issue #132:

- ADR-004 amended (Kafka durability: RF=3, ISR=2, acks=all)
- ADR-008 amended (outbox max replica lag: 30s, primary-only read)
- Design spec: docs/superpowers/specs/2026-05-09-issue-132-multi-topology-test-harness-design.md
- Cross-ref §11 in docs/design.md naming what single-host CI cannot prove
- test/ scaffold with placeholder compose.yaml + READMEs per topology
- Make targets `topo-up-{single,quorum,full}` (stubs that fail loudly)
- `make lint-test-tags` enforcing the two-axis tag rule
- 8 sub-issues spawned for real implementation work

No test code or compose contents land here — sub-issues do that.

## Test plan

- [x] `make lint-md` passes
- [x] `make lint-test-tags` exits 0 on empty test/scenarios
- [x] `make topo-up-single` exits 1 with the expected pointer message
- [x] Spec markdown renders cleanly on GitHub

Closes #132 (umbrella; sub-issues track remaining implementation).
EOF
)"
```

---

## Self-review

**Spec coverage:** every acceptance criterion in spec §11 maps to a task: ADR-004→Task 1, ADR-008→Task 2, spec file→Task 3, design.md cross-ref→Task 4, topology dirs+placeholder compose→Task 5, Make targets→Task 6, lint-test-tags→Task 6, 8 sub-issues→Task 7.

**Placeholder scan:** no "TBD"/"TODO"/"implement later" steps. Stubs in shipped code are intentional and named (sub-issue 1 owns real compose contents).

**Type consistency:** tag names (`topo_single`, `topo_quorum`, `topo_full`, `perf`, `chaos_link`, `chaos_blast`) used consistently across spec §4, scaffold READMEs (Task 5), and lint script (Task 6).
