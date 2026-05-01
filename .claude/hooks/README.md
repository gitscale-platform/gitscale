# GitScale Claude Code hooks

Hooks run automatically on tool events. They enforce the GitScale architectural invariants — ADRs, plane boundaries, outbox pattern, branch conventions — *before* a violation lands.

## What's wired

| Event | Matcher | Script | Behaviour |
|---|---|---|---|
| `PreToolUse` | `Edit\|Write\|MultiEdit` | `check-plane-edit.sh` | Soft. Prints plane-specific reminders + recommended skill names when editing `plane/**` |
| `PreToolUse` | `Bash` | `check-git-commit.sh` | Hard for `git commit*` only. Validates branch name (warn), blocks cross-plane internal imports, blocks golangci-lint failures. Silent for other Bash commands |
| `PostToolUse` | `Edit\|Write\|MultiEdit` | `check-outbox-pair.sh` | Soft. Warns when a Go file in `plane/application/**` or `plane/data/**` opens a DB transaction without referencing the outbox table (ADR-008) |
| `Stop` | — | `check-stop.sh` | Soft. When ending a turn on a feature branch ahead of `main` with an open PR, warns if the PR body lacks `Closes/Fixes/Resolves #N` |

Soft = exit 0, output goes to context as a hint. Hard = exit 2, blocks the tool call.

## How they cohere with skills + agents

Hooks are **automatic guardrails**. Skills are **deliberate workflows**. Agents are **specialists**.

- A `PreToolUse` reminder on `plane/edge/**` names `gitscale-adr-guard` and `gitscale-agent-quota-check` — Claude can then invoke those skills explicitly.
- The plane-edit reminder also names the relevant ADRs so the dispatched plane agent (`edge-plane`, `git-plane`, etc.) starts with the right context.
- The git-commit hook is the last line of defence. The plane-boundary skill catches violations during edit; the hook catches them at commit if the skill was skipped.

## Disabling temporarily

Comment out the relevant block in `.claude/settings.json` under `hooks`. Or set `CLAUDE_PROJECT_DIR` to a path where the script doesn't exist (advanced; usually unneeded).

## Adding a new hook

1. Drop the script in `.claude/hooks/`. Make it executable. Read `stdin` as JSON, write reminders to `stdout`, exit 2 only for hard blocks with a clear `[gitscale-hook] BLOCK: reason` line on `stderr`.
2. Register the script under the right event in `settings.json`.
3. Smoke-test with the patterns in `check-plane-edit.sh` (echo a JSON payload, pipe to the script, inspect output).

## Hook script conventions

- All output prefixed `[gitscale-hook]` so it's distinguishable in the transcript.
- Soft hooks must always exit 0 even when warning — never let a soft check accidentally block.
- Hard hooks must echo `BLOCK:` on `stderr` and exit 2.
- Use `${CLAUDE_PROJECT_DIR}` not relative paths.
- `cd` into `${CLAUDE_PROJECT_DIR}` before running git commands.
- Soft-skip when an external tool (gh, golangci-lint) is missing — never block on tool absence.
