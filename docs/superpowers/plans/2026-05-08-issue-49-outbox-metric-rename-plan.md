# Issue #49 outbox metric rename — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Rename `outbox_oldest_unprocessed_seconds` → `outbox_consumer_high_water_lag_seconds` to match ADR-008.

**Architecture:** Pure code rename in `plane/data/outbox/metrics.go` and any test that asserts the metric by name. No alias; no production scrape exists yet.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-49-outbox-metric-rename-design.md`

**Branch:** `chore/data-outbox-metric-rename` (worktree: `../gitscale.worktrees/chore-data-outbox-metric-rename`)

---

## File map

### Modify
- `plane/data/outbox/metrics.go`
- Any `plane/data/outbox/*_test.go` referencing the metric by name

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b chore/data-outbox-metric-rename \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/chore-data-outbox-metric-rename \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/chore-data-outbox-metric-rename
git status --porcelain
```

Expected: clean.

- [ ] **Step P.2: Locate every reference**

```bash
grep -rn "outbox_oldest_unprocessed\|oldestUnprocessed" plane/ docs/ deploy/ Makefile 2>/dev/null
```

Capture the list. The expected set at spec-write time is:

- `plane/data/outbox/metrics.go` (4 lines: comment, field, NewGaugeVec, .Set)
- Possibly a test file in `plane/data/outbox/`

---

## Task 1: Rename in `metrics.go`

**File:** `plane/data/outbox/metrics.go`

- [ ] **Step 1.1: Edit the four sites**

```bash
# field declaration + comment
sed -i 's/oldestUnprocessed is the age in seconds of the oldest unprocessed outbox/highWaterLag is the age in seconds of the oldest unprocessed outbox/' plane/data/outbox/metrics.go
sed -i 's/oldestUnprocessed \*prometheus.GaugeVec/highWaterLag *prometheus.GaugeVec/' plane/data/outbox/metrics.go
# NewGaugeVec assignment
sed -i 's/m.oldestUnprocessed = factory.NewGaugeVec/m.highWaterLag = factory.NewGaugeVec/' plane/data/outbox/metrics.go
sed -i 's/"outbox_oldest_unprocessed_seconds"/"outbox_consumer_high_water_lag_seconds"/' plane/data/outbox/metrics.go
# .Set
sed -i 's/m.oldestUnprocessed.WithLabelValues/m.highWaterLag.WithLabelValues/' plane/data/outbox/metrics.go
```

(Or do the equivalent edits via the Edit tool. Avoid `replace_all` on
non-unique strings; the four sites are unique substrings as written.)

- [ ] **Step 1.2: Build**

```bash
go build ./plane/data/outbox/...
```

Expected: success. If it fails, the field is read elsewhere — grep for
`oldestUnprocessed` and rename those too.

---

## Task 2: Update tests

**Files:** any test under `plane/data/outbox/` that references the old name.

- [ ] **Step 2.1: Locate**

```bash
grep -rn "outbox_oldest_unprocessed\|oldestUnprocessed" plane/data/outbox/
```

- [ ] **Step 2.2: Rename matches to the new name**

For literal string match in tests: `outbox_oldest_unprocessed_seconds` →
`outbox_consumer_high_water_lag_seconds`. For field references: `oldestUnprocessed` → `highWaterLag`.

- [ ] **Step 2.3: Run all outbox tests**

```bash
go test -race ./plane/data/outbox/... -count=1
go test -tags integration -race ./plane/data/outbox/... -count=1
```

Expected: PASS.

- [ ] **Step 2.4: Run lint-events**

```bash
make lint-events
```

Expected: pass (no event schema references this metric).

- [ ] **Step 2.5: Commit**

```bash
git add plane/data/outbox/
git commit -m "$(cat <<'EOF'
chore(data): rename outbox lag metric to ADR-008 name (#49)

outbox_oldest_unprocessed_seconds → outbox_consumer_high_water_lag_seconds.
Field oldestUnprocessed → highWaterLag for clarity at the source.

No deprecation alias — the metric has no production scrape target yet.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Final gates + open PR

- [ ] **Step 3.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
make lint-events
```

- [ ] **Step 3.2: Skills (data plane)**

- `gitscale-go-conventions`
- `gitscale-adr-guard` (rename brings code in line with ADR-008; should report `gap-fills` not `contradicts`)

- [ ] **Step 3.3: Self-review (parallel)**

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter`
- `pr-review-toolkit:type-design-analyzer` (none expected — pure rename)
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` — confirm ADR-008 conformance

- [ ] **Step 3.4: Push + open PR**

```bash
git push -u origin chore/data-outbox-metric-rename
gh pr create --title "[Observability] Rename outbox consumer metric to outbox_consumer_high_water_lag_seconds" --body "$(cat <<'EOF'
## Summary

- Renames `outbox_oldest_unprocessed_seconds` → `outbox_consumer_high_water_lag_seconds`
  to match ADR-008 (`docs/architecture.md` line 435) and the SLO entry in
  `docs/design.md` line 483.
- Renames Go field `oldestUnprocessed` → `highWaterLag` for clarity.
- No deprecation alias: outbox consumer has no production scrape target yet.

## ADR-impact

conforming. Brings the metric name in line with ADR-008's prescribed name.

## Test plan

- [x] `go test -race ./plane/data/outbox/...`
- [x] `make lint-events`
- [x] grep confirms zero residual references to the old name

Spec: docs/superpowers/specs/2026-05-08-issue-49-outbox-metric-rename-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-49-outbox-metric-rename-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #49.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3.5: Watch CI**

```bash
gh pr checks <number> --watch
```

---

## Self-review (plan author)

**Spec coverage:** all four spec acceptance items map to Tasks 1, 2, and 3.

**Placeholder scan:** none.

**Type consistency:** Field rename `oldestUnprocessed → highWaterLag` and metric name string applied consistently.
