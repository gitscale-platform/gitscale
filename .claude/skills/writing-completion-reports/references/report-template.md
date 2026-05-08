# Completion report template

The completion report is a single markdown file. Section count and order are fixed — do not add or drop sections. Substitute every `<<placeholder>>`. Empty sections render as a single sentence stating that the section is intentionally empty (`No plan deltas — per-issue plans shipped as written.`); never delete the heading.

---

```markdown
# Supervisor run completion — <<RUN_ID>>

**Run prompt:** `docs/superpowers/prompts/<<RUN_ID>>-supervisor-<<scope>>.md`
**Run plan:** `docs/superpowers/runs/<<RUN_ID>>-supervisor-plan.md`
**Spec:** `docs/superpowers/specs/<<RUN_ID>>-supervisor-<<scope>>-design.md`
**Iteration log:** `docs/superpowers/runs/<<RUN_ID>>-supervisor.log`
**Started:** <<ISO date of iteration 1>>
**Terminated:** <<ISO date of last iteration>>
**Termination mode:** <<clean | partial — N of M issues merged | aborted at iteration X>>

## 1. Outcome

<<one-paragraph statement of what was attempted and what shipped>>

| Issue | Title | PR | Domain | Priority | State |
|---|---|---|---|---|---|
| #<<NN>> | <<title>> | #<<PR>> | <<domain>> | <<p1|p2|p3>> | <<MERGED|BLOCKED|...>> |
| #<<NN>> | <<title>> | #<<PR>> | <<domain>> | <<p1|p2|p3>> | <<MERGED|...>> |
<!-- one row per issue in plan.issues + any follow-ups registered mid-run -->

## 2. Wave traversal

- **Wave 0** (no dependencies — parallelisable): <<#NN, #NN, #NN>>. <<all merged | N of M merged>>.
- **Wave 1** (gates on Wave 0): <<#NN>>. <<merged | not reached>>.
- **Wave 2** (gates on Wave 1): <<#NN, #NN>>. <<status>>.

<<optional: per-wave notes — soft-coordination cases, special handling, re-dispatched issues>>

## 3. Notable plan deltas across the run

| Issue | Deviation | Reason |
|---|---|---|
| #<<NN>> | <<one-line description: file moved, helper renamed, version pinned, schema added, integration switched>> | <<one-line cause: e.g. "plan helper does not exist; used existing setupPostgres(t)">> |
| #<<NN>> | <<...>> | <<...>> |

<!--
Every delta preserves the plan's intent, not its literal text — that is the
contract documented in the supervisor protocol. If a delta does NOT preserve
intent, that is a bug to file as a follow-up issue, not a delta to log here.

If no deltas: replace the table with the single sentence:
"No plan deltas — per-issue plans shipped as written."
-->

## 4. Supervisor protocol observations

<<two or three short paragraphs distilling what worked, what didn't, and what affected throughput. Examples from past waves:

- The brainstorm-then-implement pacing held design queue tractable.
- Subagent dispatch cap (concurrent_branch_cap = N) kept review pressure manageable.
- Spec+plan files committed locally before each impl branch forked, then dropped automatically by `git pull --rebase` once the subagent's branch was squash-merged.
- A single bottleneck issue dominated the early-iter latency until it cleared.
- A subagent kill mid-iter was a non-fatal anomaly; re-dispatch with explicit "finish push + PR" instructions completed in one pass.

This section primes future-wave planners — keep it specific to *this* run and *this* protocol cycle. Generic advice belongs in the supervisor agent's documentation, not here.>>

## 5. Worktree state at termination

```
<<paste verbatim output of `git worktree list`>>
```

<<one-sentence audit: "No feat/chore/docs branches outstanding. No open PRs authored by the supervisor account. All listed issues closed." OR for partial runs: "N branches still open: ...; M PRs still open: ...">>

## 6. Open architecture questions (untouched, per scope)

These remain open and unaffected by this run:

- <<question 1, with target-resolution date if known>>
- <<question 2>>
- <<question 3>>

<!--
Source: plan.scope.open_arch_questions, copied verbatim. The point is to make
explicit what this run *deliberately did not address*. If this run resolved
one of these, move it to a separate "Resolved this run" subsection above.
-->

## 7. Notes for the next wave

<<bullets the next wave's planner needs to know — usually a small handful>>

- <<carry-over: e.g. "ADR-019's static-check requirement was not landed in this wave; follow-up issue #N tracks it">>
- <<extraction opportunity: e.g. "cmd/<service>/main.go now hosts substantial boot-time wiring; consider extracting into a wiring/ package — cosmetic only">>
- <<known caveat: e.g. "the docker-compose <X> healthcheck used <tool> which is unhappy in some local Docker DNS configurations">>
- <<discovered constraint: e.g. "Vault transit /trim requires both min_decryption_version AND min_encryption_version; fake-Vault test suites must enforce">>

---

Run completes per spec §Termination criteria: <<all N issues MERGED, no
open supervisor PRs, only the primary worktree remains, this completion
report committed | OR for partial: "Run aborted at iteration N — see
iteration log for the blocker; N of M issues merged">>.
```

---

## Conventions

- **`<<placeholder>>`** is a literal marker. Grep the finished report for `<<` before commit — every occurrence must be gone.
- **§Outcome table** must include every issue from `plan.issues` plus any registered mid-run follow-ups. Don't filter for MERGED only.
- **§Wave traversal** lists every issue exactly once across all waves (no duplicates).
- **§Plan deltas** is fully optional in content but mandatory in structure — keep the heading even if the table is empty.
- **§Worktree state** is verbatim `git worktree list`. No reformatting, no editorialising.
- **§Open architecture questions** copies `plan.scope.open_arch_questions` verbatim — do not paraphrase.
- **§Notes for next wave** is human-authored. Bullets only; no essay-length narrative.
- **Trailer line** explicitly distinguishes clean vs partial termination. Silent partial reports are a known failure mode.

## Prose vs auto-fill split

Mechanically derivable (skill auto-fills):
- Frontmatter
- §1 outcome paragraph + table
- §2 wave traversal bullets (without notes)
- §5 worktree state code block
- §6 open arch questions list

Heuristic with human confirmation:
- §3 plan deltas (rows surfaced from diff; reason confirmed by human)
- §4 protocol observations bullets (extracted from log; prose written by human)
- §7 next-wave notes bullets (extracted from log + self-review findings; prose written by human)

Always human:
- The §1 leading paragraph (run framing)
- §2 per-wave notes
- §4 protocol observations narrative
- §7 next-wave notes narrative
- Trailer wording for partial-run case
