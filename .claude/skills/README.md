# GitScale custom skills

Project-scoped skills enforcing GitScale's architectural principles, ADRs, and conventions. Skills live here (versioned with the code) so they evolve alongside the system they govern.

## Index

| Skill | Triggers on | Enforces |
|---|---|---|
| [`gitscale-adr-guard`](gitscale-adr-guard/SKILL.md) | Edits to code/schema/infra/design docs | ADR conformance vs. `docs/architecture.md §8` |
| [`gitscale-plane-boundary`](gitscale-plane-boundary/SKILL.md) | Cross-plane imports, shared in-process state | Loose coupling at plane seams (core principle 2) |
| [`gitscale-outbox-check`](gitscale-outbox-check/SKILL.md) | SQL writes, Kafka producers, new consumers | ADR-008 outbox + idempotent consumers |
| [`gitscale-storage-tier-lint`](gitscale-storage-tier-lint/SKILL.md) | Storage code, replication / encoding config | Hot 3× sync replication, cold (10,4) RS |
| [`gitscale-agent-quota-check`](gitscale-agent-quota-check/SKILL.md) | New endpoints, identity / metering / billing paths | SPIFFE + quota + billing chain at every entry (ADR-010, core principle 3) |
| [`gitscale-event-schema`](gitscale-event-schema/SKILL.md) | Kafka topic / payload / consumer changes | Forward-compatible schema evolution + `event_id` idempotency |
| [`gitscale-issue-pr-link`](gitscale-issue-pr-link/SKILL.md) | Branch creation, PR draft / open | `type/plane-desc` branch, mirrored title, `Closes #N` body |
| [`gitscale-go-conventions`](gitscale-go-conventions/SKILL.md) | Any `.go` edit | Project Go style beyond `gofmt` / `golangci-lint` |
| [`gitscale-temporal-determinism`](gitscale-temporal-determinism/SKILL.md) | Workflow code under `plane/workflow` | Deterministic workflows; non-determinism only in activities |
| [`gitscale-firecracker-isolation`](gitscale-firecracker-isolation/SKILL.md) | CI runner / sandbox code | Untrusted code in Firecracker microVMs only (no Docker, gVisor, runc) |

## Cohesion model

Three layers of enforcement, each with its own latency and authority:

1. **Skills (this directory)** — invoked by Claude (or a sub-agent) at edit / draft time. Advisory; produces a verdict and a fix proposal.
2. **Hooks (`.claude/settings.json`)** — auto-run by the Claude Code harness on `PreToolUse` / `PostToolUse` / `PreCommit` events. Block tool execution on hard violations.
3. **CI checks (`.github/workflows/`)** — last line of defense at PR time. Block merge.

A skill's job is to catch the issue before the hook fires; a hook's job is to catch the issue before CI fires; CI's job is to catch what slipped past both. Each layer should be progressively cheaper to fix at, and progressively more expensive if violated.

## How Claude picks a skill

Each `SKILL.md` frontmatter `description` is the trigger contract. When a user prompt or an in-flight diff matches the description's "Use when…" symptoms, Claude reads the SKILL.md body. Heavy reference material (e.g., `gitscale-adr-guard/references/adr-map.md`) is loaded only if the skill's body references it.

Skills are **independent** — never call one skill from inside another. If two skills both apply to a diff, Claude runs both and reconciles findings.

## Adding a new skill

1. Create `.claude/skills/gitscale-<name>/SKILL.md` with frontmatter (`name`, `description`).
2. Description starts with `Use when…` — triggers and symptoms only, never a workflow summary (workflow goes in the body, not the description).
3. Body sections: Overview → When to Use (incl. when NOT) → Rules → Workflow → Output Format → Example → Common Mistakes → Why This Matters.
4. Heavy references go in `references/` siblings; reusable scripts go in `scripts/` siblings.
5. Add a row to the index table above.
6. Run the skill against three realistic test prompts before merging — see `claude-plugins-official/skill-creator` for the full eval workflow.

## Why skills, not just docs

Skills are auto-discovered by Claude when their description matches the work in front of it. A document in `docs/` does not auto-trigger; a skill does. Putting these governance rules into skills means an agent making changes is informed by them in-flight, not retroactively at code review.
