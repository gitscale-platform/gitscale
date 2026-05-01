#!/usr/bin/env bash
# Stop hook — when Claude finishes a turn on a feature branch with commits ahead of main, remind the user about PR-issue linkage.
# Soft hook: never blocks, just reminds.
set -euo pipefail

cd "${CLAUDE_PROJECT_DIR:-.}"

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
[[ -z "$branch" || "$branch" == "main" || "$branch" == "master" ]] && exit 0

ahead=$(git rev-list --count main..HEAD 2>/dev/null || echo 0)
[[ "$ahead" == "0" ]] && exit 0

# If gh available, check PR for issue link.
if command -v gh >/dev/null 2>&1; then
  pr_json=$(gh pr view "$branch" --json url,body 2>/dev/null || true)
  if [[ -n "$pr_json" ]]; then
    body=$(printf '%s' "$pr_json" | jq -r '.body // empty')
    url=$(printf '%s' "$pr_json" | jq -r '.url // empty')
    if ! printf '%s' "$body" | grep -qiE 'closes? #[0-9]+|fixes? #[0-9]+|resolves? #[0-9]+'; then
      printf '[gitscale-hook] PR %s lacks Closes/Fixes/Resolves #N. Every merged PR must close >=1 issue (CLAUDE.md).\n' "$url"
    fi
  fi
fi

exit 0
