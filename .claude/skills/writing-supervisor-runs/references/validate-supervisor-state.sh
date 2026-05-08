#!/usr/bin/env bash
# Stop-hook validator for supervisor runs.
#
# Wire into .claude/settings.json:
#   { "hooks": { "Stop": [{ "command": "scripts/validate-supervisor-state.sh", "matchers": [] }] } }
#
# Finds the most recent *-supervisor-state.json modified in the last 5 minutes,
# validates it against the schema. Exits non-zero (blocking session-end) if:
#   - file is missing
#   - file is not valid JSON
#   - file does not satisfy {.version == 1, .issues is non-empty}
#
# This is a safety net. The agent's closing checklist already enforces the same
# invariants on every well-behaved iteration; the hook catches the cases where
# the agent crashed before its closing checklist ran.

set -euo pipefail

RUNS_DIR="${SUPERVISOR_RUNS_DIR:-docs/superpowers/runs}"

# Find the most recent state file modified in the last 5 minutes.
STATE_FILE="$(find "$RUNS_DIR" -maxdepth 1 -name '*-supervisor-state.json' -mmin -5 2>/dev/null \
              | xargs --no-run-if-empty ls -t \
              | head -n1 || true)"

# No recent state file → no supervisor run active in this session. Allow stop.
if [[ -z "$STATE_FILE" ]]; then
  exit 0
fi

# File must be valid JSON.
if ! jq empty "$STATE_FILE" >/dev/null 2>&1; then
  echo "supervisor state file is not valid JSON: $STATE_FILE" >&2
  exit 1
fi

# Schema invariants: version == 1 and issues map is non-empty.
if ! jq -e '.version == 1 and (.issues // {} | length) > 0' "$STATE_FILE" >/dev/null; then
  echo "supervisor state file failed schema check (.version == 1, .issues non-empty): $STATE_FILE" >&2
  exit 1
fi

exit 0
