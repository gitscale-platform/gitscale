#!/usr/bin/env bash
# PreToolUse(Bash) — fires only on `git commit*`. Validates branch name + cross-plane import boundaries + golangci-lint.
# Hard-blocks on plane boundary violation or lint failure (exit 2).
set -euo pipefail

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // empty')

# Only act on git commit invocations.
case "$cmd" in
  *"git commit"*) ;;
  *) exit 0 ;;
esac

cd "${CLAUDE_PROJECT_DIR:-.}"

block() { printf '[gitscale-hook] BLOCK: %s\n' "$1" >&2; exit 2; }
warn()  { printf '[gitscale-hook] %s\n' "$1"; }

# 1. Branch naming: type/plane-short-description (warning only — main is allowed during scaffold phase).
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
case "$branch" in
  main|master|HEAD) warn "Committing on $branch — branch convention applies to feature branches only." ;;
  */*) ;;
  *) warn "Branch '$branch' does not match 'type/plane-short-description'. See CLAUDE.md branch conventions." ;;
esac

# 2. Cross-plane internal import lint. plane/X may not import plane/Y/internal.
staged_go=$(git diff --cached --name-only --diff-filter=AM -- '*.go' 2>/dev/null || true)
if [[ -n "$staged_go" ]]; then
  violations=""
  while IFS= read -r f; do
    [[ -z "$f" || ! -f "$f" ]] && continue
    src_plane=$(printf '%s' "$f" | sed -nE 's|.*plane/([^/]+)/.*|\1|p')
    [[ -z "$src_plane" ]] && continue
    bad=$(grep -nE '"[^"]*plane/(edge|git|application|workflow|data)/internal' "$f" 2>/dev/null \
          | grep -vE "plane/${src_plane}/internal" || true)
    if [[ -n "$bad" ]]; then
      violations+=$'\n'"$f:"$'\n'"$bad"
    fi
  done <<< "$staged_go"
  if [[ -n "$violations" ]]; then
    printf '[gitscale-hook] Cross-plane internal import detected:%s\n' "$violations" >&2
    block "Plane boundary violation. Move shared code to a public package or rethink the call. Invoke gitscale-plane-boundary."
  fi
fi

# 3. golangci-lint on staged Go files. Soft-skip if tool absent.
if [[ -n "$staged_go" ]] && command -v golangci-lint >/dev/null 2>&1; then
  if ! golangci-lint run --new-from-rev=HEAD~1 ./... 2>&1; then
    block "golangci-lint reported issues on staged Go files. Fix before committing."
  fi
fi

exit 0
