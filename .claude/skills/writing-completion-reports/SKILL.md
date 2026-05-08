---
name: writing-completion-reports
description: Use when a ralph-loop supervisor run reaches termination and the completion report needs to be authored. Triggers when (a) the `supervisor` agent's termination step runs (every issue MERGED, no open PRs, primary worktree only), (b) a human asks to "write the run summary", "draft the completion report", "wrap up the wave", or (c) an authoring session is creating `docs/superpowers/runs/<run-id>-supervisor.completion.md`. Also useful post-mortem on a stuck or aborted run to capture what landed before yielding. Reads the run's state file, iteration log, per-issue plans, and merged-PR list; emits the standard 7-section completion-report markdown with auto-detected plan deltas surfaced for human confirmation.
---

# Writing Completion Reports

## Overview

The completion report is the run's permanent record. It documents (a) what merged in dependency order, (b) where shipped code diverged from per-issue plans and why, (c) what the supervisor protocol got right or wrong, (d) what the next wave should know. Two completed waves' reports are the proof-of-concept (~110 lines each, dense — every section earns its place).

This skill produces that report from observable artefacts: the state file, iteration log, per-issue plans, and `gh pr list --state merged`. The agent invokes it as the last step before emitting the termination sentinel. Humans invoke it post-hoc to wrap up a stuck run, summarise mid-wave progress, or audit a finished wave.

**Core principle:** The skeleton (issue table, wave traversal, worktree state) is mechanically derivable. Plan deltas and protocol observations are heuristic-surfaced, then human-confirmed. Notes for the next wave are entirely human-authored.

## When to Use

Trigger when **any** are true:

- The `supervisor` agent reaches §Termination — every issue MERGED, no open supervisor PRs, primary-only worktree state, sentinel about to be emitted.
- User asks: "write the completion report", "draft the run summary", "close out this wave".
- An authoring session is editing `docs/superpowers/runs/<run-id>-supervisor.completion.md` (or the project's equivalent path).
- A run is being aborted before termination — capture what landed and what blocked.

**Do not trigger** for:

- Mid-run progress notes — those go in the iteration log, not the completion report.
- Per-issue PR descriptions — the PR body is its own artefact.
- A run that has not yet had any issues merge — there is nothing to report.

## Inputs

The skill reads:

| Input | Where it lives | Used in section |
|---|---|---|
| Run plan | `<plan_path>` from the entry prompt | §Outcome (planned issues), §Open architecture questions (untouched scope) |
| State file | `<state_path>` from the entry prompt | §Outcome (final states), §Worktree state |
| Iteration log | path from `plan.log_path` | §Wave traversal, §Protocol observations |
| Merged PRs | `gh pr list --state merged --author @me --json number,title,headRefName,mergedAt,mergeCommit,body` | §Outcome (PR table), §Wave traversal |
| Per-issue plans | `state.issues.<n>.plan` paths | §Plan deltas (compare planned vs shipped) |
| Squashed commit messages | `git show <merge_sha>` for each merged PR | §Plan deltas (rationale captured in commit body) |
| Run-config plan's `scope.open_arch_questions` | run plan | §Open architecture questions untouched |

If invoked **before termination** (post-mortem on a stuck run), the skill still works but the §Outcome table will show non-MERGED entries and §Worktree state will reflect leftover branches; the prose body explicitly names the run as incomplete.

## Workflow

1. **Verify run identity.** Read the run plan and state file. Confirm `state.run_id == plan.run_id`. Refuse to proceed on mismatch — that's a wiring error, not something this skill should paper over.

2. **Determine completion mode.** If every issue is `MERGED` and `gh pr list --state open --author @me` is empty, this is a *clean termination* report. Otherwise, this is a *partial / aborted* report — the prose explicitly says so.

3. **Build the §Outcome table.** Walk `state.issues` in priority then issue-number order. For each, look up the merged PR via `gh pr view <pr> --json number,title,mergedAt,mergeCommit`. Cells: issue number, title (from plan), PR number, domain (from plan), priority (from plan), state (MERGED / non-MERGED). One row per issue, including any `follow_up: true` issues filed mid-run.

4. **Build the §Wave traversal block.** From `plan.waves[]`, list each wave with the issues that landed in it. Add the soft-coordination note from `plan.waves[].soft_coordination` if present. Note any issue that needed re-dispatch or rebase (read the iteration log for `Subagents dispatched:` and `New branches:` lines).

5. **Detect plan deltas.** This is the hardest section — see [references/plan-delta-detection.md](references/plan-delta-detection.md) for the heuristic catalogue. For each merged issue:
   - Read the per-issue plan file at `state.issues.<n>.plan`.
   - Diff plan-named file paths vs actual files in the squashed commit.
   - Look for: file moves/renames, version pins, helpers renamed, schemas added, integration approach changes (often called out in the squashed commit body).
   - Surface candidate deltas to the user with the *why* extracted from the squashed commit body where possible.
   - Confirm each candidate with the user before committing it to the report — false positives are worse than missed deltas.

6. **Draft §Supervisor protocol observations.** This is human-authored prose, but the skill primes it from the iteration log:
   - Count of brainstorms (one per `Designed + planned` log entry).
   - Count of subagent re-dispatches (look for "re-dispatching" or repeated `Subagents dispatched:` for the same issue).
   - Notable bottlenecks (longest gap between dispatch and PR open).
   - Anomalies recorded across iterations (`Anomalies:` lines).
   - Surface these as bullets the human extends or rewrites.

7. **Build §Worktree state at termination.** Read `git worktree list`. Render verbatim in a fenced code block. On clean termination, this should be the primary worktree only — confirm before declaring success.

8. **Build §Open architecture questions untouched.** From `plan.scope.open_arch_questions`, list each verbatim with its target-resolution date if known. The point is to make explicit what this run *deliberately did not address*.

9. **Draft §Notes for the next wave.** Human-authored prose. The skill primes this from:
   - Any `notes` fields on `plan.issues[]` flagged as carry-over.
   - Static-check / lint follow-ups suggested by the run (read iteration log).
   - Re-architecture or extraction opportunities mentioned in self-review subagent findings (extract from PR self-review blocks).
   Surface as bullets the human edits.

10. **Validate the report against the template.** All 7 sections present (§Outcome, §Wave traversal, §Plan deltas, §Protocol observations, §Worktree state, §Open arch questions, §Next-wave notes). Frontmatter naming the run-id, prompt path, spec path, log path. The trailer line: `Run completes per spec §Termination criteria` (clean termination only) or `Run aborted at iteration N — see iteration log` (partial).

11. **Write the report.** Path: `<plan.completion_report_path>`, typically `docs/superpowers/runs/<run-id>-supervisor.completion.md`.

12. **Commit the report.** Unlike the entry prompt and state file, the completion report **is** committed to the default branch. It's the run's permanent record. Use a `docs(meta):` (or project-equivalent) commit subject.

## Output format

A markdown file at `<plan.completion_report_path>` matching [references/report-template.md](references/report-template.md). Frontmatter, then 7 sections in order, then the trailer.

The supervisor agent commits this file via:

```bash
git add <plan.completion_report_path>
git commit -m "docs(meta): supervisor wave <run-id> completion report"
```

…and only then emits the sentinel.

## Quick reference — section sources

| Section | Mechanically derivable | Human-authored | Source |
|---|---|---|---|
| Outcome | yes | no | state + `gh pr view` |
| Wave traversal | yes | optional notes | plan.waves + iteration log |
| Plan deltas | candidates only | confirmation + reason | per-issue plans + git show + commit body |
| Protocol observations | bullets only | full prose | iteration log |
| Worktree state | yes | no | `git worktree list` |
| Open arch questions untouched | yes | no | plan.scope.open_arch_questions |
| Notes for next wave | bullets only | full prose | log + plan notes + self-review findings |

## Spot-checks before commit

- §Outcome table row count == `len(state.issues)` (plus any registered follow-ups).
- §Wave traversal lists every issue exactly once (no duplicates, no orphans).
- §Plan deltas section present even if empty — empty case says "no notable deltas; per-issue plans shipped as written".
- §Worktree state code block is verbatim `git worktree list` output, no editorialising.
- §Open arch questions section names every entry from `plan.scope.open_arch_questions` — none silently dropped.
- Frontmatter run-id matches `state.run_id` matches `plan.run_id`.
- Trailer accurately reflects clean vs partial termination.

## Common mistakes

| Mistake | Fix |
|---|---|
| Auto-committing detected plan deltas without human confirmation | False positives create "deltas" that aren't real (e.g. test-file naming differs from plan but content matches). Always confirm each candidate. |
| Treating §Plan deltas as optional | Empty is fine; missing is not. Future readers compare plan vs shipped code via this section. |
| Editorialising in §Worktree state | Render `git worktree list` verbatim. The point is auditability. |
| Listing only merged issues in §Outcome on a partial run | Include unmerged issues too with their final state. The report's job is to document the wave's actual outcome, not pretend everything succeeded. |
| Inflating §Protocol observations into a postmortem essay | Two-three short paragraphs is right. The iteration log is the long-form record; the report distills only what affects the *next* wave's planning. |
| Dropping §Open arch questions because "nothing in this wave touched them" | That's the point — the section makes the *deliberate non-coverage* explicit. Future humans reading this in 6 months won't remember what was deferred. |
| Committing the report before the agent exits | The agent is responsible for the commit (it owns the working tree). The skill writes the file; the agent commits. |
| Writing the report on a partial run without saying so | Add the explicit "Run aborted at iteration N" trailer. Silent partial reports mislead readers into believing the wave succeeded. |
| Using the report to justify what shipped | The report is descriptive, not justificatory. If a delta needs justifying, fix the delta or amend the plan in a follow-up; don't argue in the report. |

## Why this matters

Two waves of completion reports are the only durable record of what those runs actually shipped vs. what they planned to ship. Every plan delta captured (`#74 migration renumbered`, `#75 transit primitive switch`, `#62 SDK version pin`, `#80 Vault decryption-version constraint`) is a unit of accumulated knowledge — the next wave's planner reads them and avoids re-treading the same surface. Skipping the report or letting it drift toward a marketing summary erases that knowledge.

Mechanising the derivable sections (outcome, wave traversal, worktree state, untouched arch questions) means the human's energy goes into the parts that genuinely require judgment: confirming plan deltas, distilling protocol observations, and naming what the next wave should know.
