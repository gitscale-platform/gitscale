---
name: gitscale-issue-pr-link
description: Use when creating a branch, opening a pull request, drafting a PR title or description, or pushing to a remote that may auto-open a PR. Triggers on git checkout -b, gh pr create, opening or editing a PR title, opening or editing a PR body, or when the user asks "what should I name this branch?", "what's the PR title?", "does this PR close the issue?". Catches the merge-time invariants — every merged PR closes ≥1 issue, branch matches type/plane-short-description, PR title mirrors the issue title — before they fail late in review.
---

# GitScale Issue ↔ PR Link

## Overview

GitScale's contribution model has three hard rules:

1. **Branch naming:** `type/plane-short-description` (e.g., `spike/data-postgres-partition-strategy`, `feat/edge-token-meter-wasm-filter`).
2. **PR title:** mirrors the issue title (e.g., `[Git] Design hot-tier replication quorum protocol`).
3. **Every merged PR closes at least one issue.** PR body must contain `Closes #N` for some open issue.

These rules exist so that the project's history is navigable from issues, the plane affinity of every change is visible at a glance, and no merged PR is orphaned from its motivating discussion.

**Core principle:** the branch, the PR, and the issue are three views of the same change. Keep them aligned at creation time, not retroactively.

## When to Use

Trigger on **any** of:

- `git checkout -b <name>` or `git switch -c <name>` is run
- `gh pr create` is run, or a PR is being drafted in any way
- The user asks "what should I name this branch?", "what's the PR title?", "does this PR need an issue?", "what type is this?"
- A push to a remote on a branch that doesn't have an associated PR yet (auto-PR-creation flows)
- A PR body is being written that doesn't include `Closes #` or `Fixes #`

**Don't trigger** for: branches under explicit special prefixes the project uses for non-PR workflows (e.g., `release/...`, `dependabot/...`), or for direct main branch commits (which the project disallows anyway).

## The naming grammar

```
<type>/<plane>-<short-description>
```

**Types** (drives the work category, mirrors issue templates):

| Type | When |
|---|---|
| `feat` | New feature |
| `fix` | Bug fix |
| `spike` | Time-boxed investigation (resolves an open architecture question) |
| `adr` | New ADR or amendment to an existing ADR |
| `refactor` | Code reshape, no behavior change |
| `chore` | Tooling, dependencies, CI |
| `docs` | Documentation-only change |

**Planes:**

| Plane | Code |
|---|---|
| Edge | `edge` |
| Git | `git` |
| Application | `application` (or `app`) |
| Workflow | `workflow` |
| Data | `data` |
| Cross-plane / repo-wide | `core` |

**Short description:** kebab-case, ≤ 6 words, focused on the *what*, not the *how*.

Examples:
- `spike/data-postgres-partition-strategy` ✅
- `feat/edge-token-meter-wasm-filter` ✅
- `adr/git-storage-tiering-decision` ✅
- `fix/workflow-temporal-deterministic-clock` ✅
- `wip/stuff` ❌ (no type, no plane, no description)
- `feature/add-thing` ❌ (`feature` not `feat`, plane missing)

## The PR title rule

The PR title **mirrors the issue title**. This means:

- Same wording
- Same plane prefix in `[Brackets]` if the issue uses one (which is the convention)
- Don't paraphrase — copy

If the PR addresses multiple issues with different titles, pick the primary issue's title and link the others in the body.

## The closure rule

PR body must contain at least one of:

- `Closes #N`
- `Fixes #N`
- `Resolves #N`

…where `#N` is an open issue in this repo. The keyword matters — GitHub auto-closes only on those exact verbs.

If the change is truly issue-less (rare, real cases: typo in a doc, urgent CI fix), open an issue first. The cost of one issue is one minute; the value is durable history.

## Workflow

1. **For a branch name** — verify type ∈ allowed set, plane ∈ allowed set, description is kebab-case ≤6 words. If not, propose a fix.
2. **For a PR title** — read the linked issue's title (`gh issue view <N>`). PR title should match. If a `Closes #N` is missing, ask the user for the issue number; if there genuinely isn't one, propose opening an issue.
3. **For a PR body** — verify a `Closes #N` (or equivalent) is present and the referenced issue is open. Verify the PR template's checklist is filled.
4. **Output a verdict.**

## Output Format

```
issue-pr-link: <ok | violations>
Branch: <name> — <ok | needs rename to: <suggestion>>
PR title: <title> — <ok | mirrors issue #N | needs change to: <suggestion>>
PR body: <closes-found | missing-closes | wrong-keyword>
Issue refs: <#N (open) | #N (closed) | none>
Fix: <one bullet per violation>
```

## Example

**Input:**

- Branch: `add-rate-limiter`
- PR title: "added rate limiter"
- PR body: "This adds a rate limiter to the API."

**Verdict:**

```
issue-pr-link: violations
Branch: add-rate-limiter — needs rename to: feat/edge-rate-limiter-token-bucket
PR title: added rate limiter — needs change to mirror issue title
PR body: missing-closes
Issue refs: none
Fix:
  - Open or identify the issue this PR addresses; let's say #42
    "[Edge] Add per-agent token-bucket rate limiter".
  - Rename branch:    git branch -m feat/edge-rate-limiter-token-bucket
  - Set PR title:     [Edge] Add per-agent token-bucket rate limiter
  - Add to PR body:   Closes #42
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| Closing the issue manually after merge instead of letting GitHub auto-close | Use `Closes #N`. Manual close drifts the audit trail and breaks the PR↔issue invariant. |
| Plane = `frontend` / `backend` / `infra` | Plane is one of edge/git/application/workflow/data/core. There is no frontend plane. |
| Multi-issue PRs with conflicting titles | Pick a primary, link the rest. If the changes really are unrelated, split the PR. |
| `Closes` keyword followed by an issue from a different repo | Only same-repo issues auto-close on merge. Use the full URL and `Tracks` keyword for cross-repo references. |
| Branch name with spaces, underscores, or no plane | Always `<type>/<plane>-<kebab-description>` — separator is hyphen. |
| Reusing the same branch for multiple PRs | One PR per branch. Open a new branch for the next change. |

## Why This Matters

Three small disciplines, one large payoff: a year from now, a new contributor can navigate from any merged commit back to the issue that motivated it, see the discussion, and understand the plane it touched — without spelunking. The alternative is a history of "added rate limiter" commits with no context, where a quarter of the changes are quietly orphaned from any issue and no one remembers why they shipped.

The rules also serve mechanical needs: branch grep filters by plane (e.g., reviewers for `data` changes), PR-issue links drive release notes, the auto-close keyword removes manual triage. A small upfront discipline removes a recurring tax forever.
