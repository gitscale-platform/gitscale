#!/usr/bin/env bash
# PreToolUse(Edit|Write|MultiEdit) — remind plane-specific invariants when editing plane/** code.
# Soft hook: prints reminders to context, never blocks.
set -euo pipefail

input=$(cat)
file_path=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')
[[ -z "$file_path" ]] && exit 0

remind() { printf '[gitscale-hook] %s\n' "$1"; }

case "$file_path" in
  */plane/edge/*)
    remind "Edge plane edit detected: $file_path"
    remind "  ADR-010 SPIRE/SPIFFE: re-verify SVID, never trust upstream-stamped headers."
    remind "  Metering at edge, not deeper. Redis (ADR-009) for rate limits."
    remind "  Recommended skills: gitscale-adr-guard, gitscale-agent-quota-check."
    ;;
  */plane/git/*)
    remind "Git plane edit detected: $file_path"
    remind "  Storage tiering: hot = 3x sync replication, cold = (10,4) RS. No mixing."
    remind "  All Git ops via Gitaly RPC. Never os/exec the git binary."
    remind "  Recommended skills: gitscale-storage-tier-lint, gitscale-adr-guard."
    ;;
  */plane/application/*)
    remind "Application plane edit detected: $file_path"
    remind "  ADR-008: state mutations must write source row + outbox row in same txn."
    remind "  No goroutines for async work — use Temporal (workflow plane)."
    remind "  Recommended skills: gitscale-outbox-check, gitscale-plane-boundary, gitscale-go-conventions."
    ;;
  */plane/workflow/*)
    remind "Workflow plane edit detected: $file_path"
    remind "  Workflow funcs deterministic: no time.Now / rand / os / network — activities only."
    remind "  CI runner: Firecracker microVMs only. No Docker, no gVisor."
    remind "  Recommended skills: gitscale-temporal-determinism, gitscale-firecracker-isolation."
    ;;
  */plane/data/*)
    remind "Data plane edit detected: $file_path"
    remind "  Schema: forward-only online migrations. Outbox row shape per ADR-008."
    remind "  Vespa = primary search. Qdrant = PR dedup only (cosine >= 0.92, ADR-016)."
    remind "  Recommended skills: gitscale-event-schema, gitscale-adr-guard."
    ;;
  *docs/architecture.md|*docs/architecture/*)
    remind "ADR doc edit detected. Confirm change is an ADR amendment, not a code-driven retrofit."
    remind "  Pair with code change in same PR. Update .claude/skills/gitscale-adr-guard/references/adr-map.md if surfaces moved."
    ;;
esac

exit 0
