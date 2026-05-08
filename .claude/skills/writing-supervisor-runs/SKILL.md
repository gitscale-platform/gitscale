---
name: writing-supervisor-runs
description: Use when scaffolding a new ralph-loop supervisor run that drives a batch of GitHub issues through brainstorm → spec → plan → implementation → PR → merge. Triggers on "set up a supervisor", "draft a supervisor prompt", "drive these N issues to merge", "next wave", "ralph-loop my backlog", "scaffold a multi-issue ralph loop", or when authoring a new file under `docs/superpowers/prompts/*-supervisor*.md`. Authors the per-run trio (entry prompt + plan + empty state file) the `supervisor` agent reads, plus the `/ralph-loop` launch line. Does NOT carry the supervisor protocol body — that lives in the `supervisor` agent's system prompt.
---

# Writing Supervisor Runs

## Overview

The `supervisor` agent (at `.claude/agents/supervisor.md`) executes one ralph-loop iteration per invocation: read state, take one iteration's worth of action, write state, exit. Ralph-loop restarts it with fresh context; recovery is automatic because the agent reads its state from disk every time.

This skill produces the **per-run inputs** the agent needs:

1. A small **entry prompt** ralph-loop reads on every iteration. Two-three lines: "Use the `supervisor` agent. Plan: `<path>`. State: `<path>`."
2. A **plan file** carrying the run-specific data: scope, issue list, dependency graph, subagent map, self-review battery, sentinel suffix, project-specific commands.
3. An **empty state file** (`{}`) — the agent's iteration-1 bootstrap initialises it from the plan.

The skill also emits the `/ralph-loop` launch line with `--completion-promise` matching the plan's sentinel.

**Core principle:** Protocol lives in the agent. Run-specific data lives in the plan. Cross-iteration memory lives in the state file. The skill only authors run-specific data — it never re-encodes the protocol.

## When to Use

Trigger when **any** are true:

- User asks to scaffold or launch a supervisor: "set up the supervisor", "draft a supervisor prompt", "drive issues #X..#Y to merge via ralph-loop", "kick off the next wave".
- User is authoring a new `docs/superpowers/prompts/<run-id>-supervisor-*.md` file (or the project's equivalent prompts location).
- An existing supervisor prompt is being refreshed for a new run-id (date roll, scope change, follow-up wave).
- A wave spec exists (e.g. under `docs/superpowers/specs/<run-id>-supervisor-*-design.md`) and needs the matching execution prompt + plan + state.

**Do not trigger** for:

- Single-issue work — there is no wave. Use a normal implementation plan.
- Authoring the *spec* of a wave (use brainstorming + plan-writing skills first).
- Generating a completion report after a wave finishes (separate concern).
- Scheduling unrelated cron-style ralph-loop tasks that aren't multi-issue supervisors.

## Inputs to gather from the user

Most come from the wave spec. Ask only for what's missing.

| Input | Used in plan section | Notes |
|---|---|---|
| `<RUN_ID>` | header, sentinel, log path, prompt filename | `YYYY-MM-DD`. Must be identical in every occurrence. |
| `<REPO_ROOT>` | `repo` block | Absolute path of primary checkout. |
| `<DEFAULT_BRANCH>` | `repo` block | Usually `main`. |
| `<WORKTREES_ROOT>` | `repo` block | Sibling of `<REPO_ROOT>` recommended. |
| Issue list with title + domain + priority | `issues` table | One row per issue. |
| Dependency graph | `waves` block | Wave 0 = no deps; Wave N gates on Wave N-1 issues being MERGED. |
| Out-of-scope guardrails | `scope` block | Areas the supervisor must not touch. |
| Domain-to-subagent mapping | `subagent_map` block | Which subagent owns which domain; mandatory pre-commit skills per domain. |
| Self-review battery | `self_review_battery` list | Review subagents to fan-out before every `gh pr create`. |
| Brainstorming + plan-writing skill names | `interactive_skills` block | Project may use `superpowers:brainstorming` + `superpowers:writing-plans`, or its own. |
| Local pre-push gates | `pre_push_gates` block | Project's build/test/lint commands. |
| CI gates that must be green | `ci_gates` list | Names of required GitHub Actions checks. |
| Spec/plan directory paths | `paths` block | Where per-issue specs and plans land. |
| Concurrency caps | `caps` block | `concurrent_branch_cap` (default 4), `concurrent_subagent_cap` (default 6). |
| Project invariant docs | `invariant_docs` list | Files where merge conflicts are auto-blockers (e.g. architecture decision records). |

## Workflow

1. **Confirm the wave spec exists.** If `docs/superpowers/specs/<RUN_ID>-supervisor-*-design.md` (or equivalent) is missing, stop and route to brainstorming + plan-writing skills first. There is no supervisor without a spec.

2. **Confirm the supervisor agent exists.** Verify `.claude/agents/supervisor.md` is present (frontmatter `name: supervisor`). If missing, stop and report — the agent ships alongside this skill but is not auto-installed.

3. **Lock in `<RUN_ID>`.** It appears in (a) entry prompt filename, (b) plan filename, (c) state filename, (d) iteration log filename, (e) sentinel `SUPERVISOR-RUN-COMPLETE-<RUN_ID>`, (f) `--completion-promise` on the launch line. Drift anywhere breaks termination silently.

4. **Build the issues table.** From `gh issue list --state open --json number,title,labels,state` filtered to the spec's scope. Annotate with domain (free-text — plane, module, layer, package) and priority. Populates the plan's `issues` block.

5. **Build the dependency graph.** Wave 0 = issues with no deps → run in parallel. Wave 1 = issues that gate on Wave-0 issues being MERGED. And so on. Note (a) soft-coordination cases (issue X may ship before issue Y even though both touch the same area), (b) doc-only / amendment-only issues that don't get an implementation branch.

6. **Build the subagent + skill matrix.** Per domain (or per issue when special): name the implementation subagent and the mandatory pre-commit skills. List the self-review battery once for the whole run.

7. **Fill the plan template.** Open [references/plan-template.md](references/plan-template.md) and substitute every `<<placeholder>>`. The plan structure is fixed — do not add new top-level blocks; add data inside existing blocks.

8. **Sentinel sanity-check.** Grep the finished plan for `<RUN_ID>` — every occurrence must match. Grep for `SUPERVISOR-RUN-COMPLETE-` — must appear once, in the `sentinel:` field, exactly as `SUPERVISOR-RUN-COMPLETE-<RUN_ID>`.

9. **Validate plan + state schemas.** Plan front-matter is YAML — see [references/plan-template.md](references/plan-template.md). State is JSON — see [references/state-schema.json](references/state-schema.json). The agent refuses to start if either fails to parse.

10. **Write the entry prompt.** Path: `docs/superpowers/prompts/<RUN_ID>-supervisor-<scope>.md`. Full contents:

    ```markdown
    # Supervisor entry — run <RUN_ID>

    Use the `supervisor` agent for this iteration.

    - Plan: `docs/superpowers/runs/<RUN_ID>-supervisor-plan.md`
    - State: `docs/superpowers/runs/<RUN_ID>-supervisor-state.json`

    Do not alter the protocol; the agent's system prompt is authoritative. If the agent reports a blocker, surface it and stop.
    ```

    Keep it that short. The agent's system prompt is the protocol; the plan is the data; the state is the memory. The entry prompt is just routing.

11. **Write the plan file.** Path: `docs/superpowers/runs/<RUN_ID>-supervisor-plan.md`. Use the template, fill placeholders, validate YAML.

12. **Initialise the state file.** Path: `docs/superpowers/runs/<RUN_ID>-supervisor-state.json`. Write `{}`. The agent's iteration-1 bootstrap will populate it from the plan.

    If the run is **resuming** (state file already exists and is non-empty), do not overwrite — confirm health (`jq -e '.version == 1'`) and stop. The supervisor will resume from the existing state.

13. **Scaffold the iteration log.** Path: `docs/superpowers/runs/<RUN_ID>-supervisor.log`. Header lines naming the prompt, plan, state, spec paths, and start date. Append-only thereafter.

14. **Do not commit run artefacts.** Entry prompt, plan, state, and log are all run-local. Only specs and per-issue plans should land on the default branch. (Verify against project conventions before assuming.)

15. **Emit the launch line:**

    ```
    /ralph-loop "Read docs/superpowers/prompts/<RUN_ID>-supervisor-<scope>.md and execute it." --max-iterations <N> --completion-promise "SUPERVISOR-RUN-COMPLETE-<RUN_ID>"
    ```

    `<N>` heuristic for first-time waves: `(issue_count × 3) + 10`. With one-action-per-iteration, iteration count rises (one per merge, one per dispatch, one per brainstorm) — budget more generously than a monolithic-prompt run.

## Output format

Four on-disk artefacts (un-committed) plus one launch line in chat:

```
docs/superpowers/prompts/<RUN_ID>-supervisor-<scope>.md     # entry prompt
docs/superpowers/runs/<RUN_ID>-supervisor-plan.md            # plan
docs/superpowers/runs/<RUN_ID>-supervisor-state.json         # state (initial: {})
docs/superpowers/runs/<RUN_ID>-supervisor.log                # iteration log header
```

Followed by the `/ralph-loop` launch line.

## Optional: Stop hook for state-write enforcement

The agent owns state writes. As a safety net, project hooks can validate `state.json` was written and is parseable before allowing a session to end. Recommended snippet for `.claude/settings.json` or `.claude/settings.local.json`:

```json
{
  "hooks": {
    "Stop": [
      {
        "command": "scripts/validate-supervisor-state.sh",
        "matchers": []
      }
    ]
  }
}
```

The script (~10 lines) finds the most recent `*-supervisor-state.json` modified in the last 5 minutes, runs `jq -e '.version == 1 and (.issues | length) > 0'` against it, exits non-zero if validation fails (which prompts the user before session-end). Optional — the agent's closing checklist already enforces this in well-behaved iterations.

## Spot-checks before launch

- The `supervisor` agent file exists and parses (frontmatter intact).
- Plan parses as the template's YAML front-matter schema.
- State file parses as JSON.
- Sentinel in the plan matches the launch line `--completion-promise` character-for-character.
- Subagent map covers every issue in the issues table (no orphans).
- Dependency graph terminates (every Wave N+1 issue's deps are in Wave ≤ N).
- Scope guardrails name every domain the issue table does not touch.
- Pre-push gates and CI gates are runnable in this project.
- `concurrent_branch_cap` is realistic for the team's review bandwidth.
- Issue count in the plan's `termination` block matches the issues table count.

## Common mistakes

| Mistake | Fix |
|---|---|
| Putting protocol logic in the entry prompt or plan | Protocol lives in the agent. Plan carries data. Entry prompt routes. If you find yourself writing "if state X then do Y" in the plan, that belongs in the agent. |
| Sentinel date drift | Grep the plan for `<RUN_ID>` before launching; every match must be identical. |
| Committing run artefacts to the default branch | Only specs and per-issue plans should land. Prompt + plan + state + log are run-local. |
| Pre-populating state with detailed cached state instead of `{}` | Adds a second bootstrap path. Empty `{}` keeps iteration-1 init the only path. |
| Adding new top-level blocks to the plan template "for this run" | Resist. Variable data fits in existing blocks. New blocks mean the agent ignores them. |
| Hard-coding subagent names that aren't installed | Verify each subagent in `subagent_map` is invokable before launch. The agent fails mid-iteration if a subagent is missing. |
| Setting `concurrent_branch_cap` too high | Reviewer bandwidth is the bottleneck. 4 is a sane default; 2-3 is safer for fragile review queues. |
| Writing the entry prompt longer than ~10 lines | If the entry prompt is doing real work, the protocol is leaking out of the agent. Move logic back. |
| Running this skill before the wave spec exists | Brainstorm first. The skill needs the spec to derive the issues table, dep graph, scope, subagent map. |

## Why this matters

Three load-bearing properties this architecture preserves:

1. **One iteration = one invocation = one action.** Voluntary exit at the end of every iteration means each ralph-loop spawn starts with a clean context window. No iteration ever exhausts itself by chaining too many actions.
2. **Recovery is automatic.** State on disk + atomic write means a fresh agent reads the state file and resumes — there is no separate "restart" mode. Crash, kill, ralph-loop budget exhaustion: all converge on the same recovery path.
3. **Protocol is stable across runs.** The agent definition is the single source of truth for the state machine, merge rules, worktree rules, self-review battery, and termination predicate. Per-run plans cannot drift because they don't carry protocol — only data.

The skill exists to make authoring per-run inputs trivial so the human's energy goes into the parts that genuinely vary: the issue list, the dependency graph, and the subagent map.
