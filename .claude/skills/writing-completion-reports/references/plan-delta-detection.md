# Plan-delta detection heuristics

A plan delta is a divergence between what a per-issue plan said would ship and what actually shipped. Deltas are valuable to record because they encode learning — typically a constraint discovered during implementation that the plan author couldn't have known. Two waves of completion reports captured 5–9 deltas each; every one was a unit of accumulated knowledge for the next wave.

This document catalogues heuristics for detecting delta candidates from observable artefacts. **Every candidate needs human confirmation** before landing in the report — false positives (test-file naming differences, formatting-only changes) create noise that erases the signal in real deltas.

## Inputs available per merged issue

For each merged issue `<n>` with state `MERGED` and `pr <p>`:

```bash
# Per-issue plan as it was committed at impl time
PLAN=$(cat <state.issues.<n>.plan>)

# What actually shipped
SHA=$(gh pr view <p> --json mergeCommit -q '.mergeCommit.oid')
SHIPPED_FILES=$(git show --name-only --format= "$SHA")
SHIPPED_DIFF=$(git show "$SHA")
COMMIT_BODY=$(git show -s --format=%B "$SHA")
```

Three signals to inspect: the plan text, the shipped diff, the squashed commit body.

## Heuristic catalogue

### 1. File path moved or renamed

**Detect:** Plan names a file path (typically in a `Files:` block at the top of a task or step). Shipped diff modifies a different path covering the same logical component.

**Search:** for each `Create:|Edit:|Files:` line in the plan, extract the path. Check whether the path appears in `SHIPPED_FILES`. If not, look for a near-match (same basename, different directory) in shipped files.

**Confirmation prompt:** "Plan named `<plan-path>`; shipped at `<shipped-path>`. Was this a deliberate move?"

**Examples (from past waves):**
- Migration `006_billing_partition_archives.sql` → `007_billing_partition_archives.sql` (006 was already taken on main).
- `plane/data/store/billing/billing.go` → `plane/data/store/postgres/billing.go` (target dir was already occupied).

### 2. Helper or function renamed

**Detect:** Plan references a specific symbol name (e.g. `postgrestest.NewPool`); shipped diff uses a different symbol that achieves the same purpose.

**Search:** extract `<package>.<symbol>` patterns from the plan. Grep shipped diff for the pattern. If absent, surface as a candidate.

**Confirmation prompt:** "Plan referenced `<plan-symbol>`; shipped diff uses `<shipped-symbol>` (or no equivalent). Was the helper renamed, missing, or replaced?"

**Examples:**
- `postgrestest.NewPool` (planned) vs `setupPostgres(t)` (shipped) — plan helper did not exist; existing test helper was reused.

### 3. Dependency version pinned, bumped, or replaced

**Detect:** `go.mod`, `package.json`, `Cargo.toml`, etc. modified in shipped diff. Plan did not anticipate a version change.

**Search:** diff `go.mod` (or equivalent) before and after. Surface every added/changed line. Check whether the plan mentioned the dependency.

**Confirmation prompt:** "Shipped diff changes `<dep>` from `<old>` to `<new>` (or pins it). Plan did not specify. What forced the version?"

**Examples:**
- `go.temporal.io/sdk/contrib/opentelemetry@v0.6.0` pinned (instead of latest) because v0.7.0 transitively required missing API versions.
- Manual go.mod / go.sum conflict resolution after a sibling PR landed — `go mod tidy` produced a fix-up commit.

### 4. Schema, fixture, or convention file added (not in plan)

**Detect:** Files matching `*.schema.json`, `*.proto`, `fixtures/**`, `migrations/**` appear in shipped diff but are not named in the plan.

**Search:** grep shipped files for these patterns; cross-reference plan text.

**Confirmation prompt:** "Shipped diff adds `<file>` which the plan does not name. Was this required by a repo convention (e.g. lint-events) or a downstream consumer?"

**Examples:**
- `plane/data/events/billing/partition_archived.schema.json` + fixture added because `make lint-events` requires per-event schema.

### 5. Integration approach changed mid-implementation

**Detect:** Plan describes an integration mechanism (e.g. "use Vault `transit/datakey/plaintext`"). Shipped diff uses a different mechanism for the same role.

**Search:** the squashed commit body almost always documents this kind of change — grep for "switched", "instead of", "rather than", "couldn't use", "had to". Cross-reference with the plan text to identify what was originally intended.

**Confirmation prompt:** "Commit body says `<excerpt>`. Plan said `<plan excerpt>`. Was the mechanism changed because plan-version was unfit, unavailable, or wrong?"

**Examples:**
- Plan: `transit/datakey/plaintext`. Shipped: `transit/hmac/<key>/sha2-256` — datakey is non-deterministic per Vault docs; HMAC is a true PRF, satisfying the determinism constraint.
- Plan assumed single SDK type for two registration sites. Shipped split into two distinct types (`TemporalInterceptor` and `TemporalWorkerInterceptor`) because the SDK requires separate slice types.

### 6. Constraint discovered during integration

**Detect:** Tests added or modified in shipped diff that enforce a constraint not present in the plan. Often the squashed commit body says "fake-X missed the constraint" or "real-X enforces" or "added test for".

**Search:** scan shipped test files for new asserts. Cross-reference with commit body for the discovery narrative.

**Confirmation prompt:** "Tests added enforce `<constraint>`. Plan did not anticipate. Was this discovered during real-system integration?"

**Examples:**
- Vault `transit /trim` requires both `min_decryption_version` AND `min_encryption_version` to be set; fake-Vault unit suite missed this constraint and integration test caught it.

### 7. Scope expansion within intent

**Detect:** Shipped diff touches files the plan did not enumerate, but the change is consistent with the plan's intent.

**Search:** files in `SHIPPED_FILES` not mentioned in the plan's task blocks. Filter out trivial cases (formatting changes, generated code) by hand.

**Confirmation prompt:** "Shipped diff touches `<file>` not named in the plan. Was this in-intent expansion or scope creep?"

**Discrimination rule:** If the change is mechanically required by the plan's stated goal (e.g. updating a test fixture to match a new field), it is in-intent — log it as a delta only if the magnitude is non-trivial. If the change is a side-quest (refactor, cleanup, unrelated fix), it is scope creep — surface as a candidate but consider whether it should have been a separate PR.

## What is NOT a delta worth logging

- **Test file naming differences** — the plan doesn't always specify exact test file paths; matching content matters more than matching path.
- **Formatting changes** — `gofmt`, `prettier`, `black` reflows are not deltas.
- **Comment text changes** — only material if the comment described a contract that changed.
- **Generated code changes** — `protoc` output, `mockgen` output, etc. are derivative, not authored.
- **Trivial conflict resolution** — `go mod tidy` after a rebase, import order changes, etc.

If a candidate falls into these categories, drop it before showing the user.

## Output format for the report

For each confirmed delta, render one row of the §Plan deltas table:

```markdown
| #<<NN>> | <<one-line description>> | <<one-line cause>> |
```

Keep both the description and the cause to one line each. The completion report is a record, not an essay — the *reasoning* lives in the squashed commit body, which the report links to via PR number. The delta row is the index into that record.

## Confirmation flow

When the skill is invoked by the supervisor agent (autonomous), it surfaces candidate deltas to the user via a single consolidated prompt before writing the report. When invoked by a human directly, the human walks the candidate list and confirms each. Either way: every row in the final §Plan deltas table is human-confirmed.

If a candidate is flagged but the human cannot recall the reason, mark it `Reason: <<unrecorded — see commit body>>` and link the PR. Future readers can reconstruct from the squashed commit. Better than dropping the row.

## Completeness

This catalogue is non-exhaustive. New delta categories emerge with each wave. When a new category appears (and it is genuinely new — not just a sub-case of the seven above), add it here as part of the next completion-report-writing session. The catalogue is a living artefact; resist the urge to leave it static.
