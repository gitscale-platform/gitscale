#!/usr/bin/env bash
# PostToolUse(Edit|Write|MultiEdit) — flag Go files that open a DB transaction without referencing the outbox table.
# Soft hook: warns, never blocks. ADR-010 requires same-txn source-row + outbox-row pairing.
set -euo pipefail

input=$(cat)
file_path=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')
[[ -z "$file_path" ]] && exit 0
[[ "$file_path" != *.go ]] && exit 0

case "$file_path" in
  */plane/application/*|*/plane/data/*) ;;
  *) exit 0 ;;
esac

[[ ! -f "$file_path" ]] && exit 0

opens_tx=0
mentions_outbox=0
grep -qE '\.BeginTx\(|\.Begin\(|pgx\.BeginFunc|conn\.Begin' "$file_path" && opens_tx=1
grep -qiE 'outbox|event_id' "$file_path" && mentions_outbox=1

if [[ $opens_tx -eq 1 && $mentions_outbox -eq 0 ]]; then
  printf '[gitscale-hook] %s opens a DB transaction but does not reference outbox / event_id.\n' "$file_path"
  printf '[gitscale-hook]   ADR-010: state mutations must write source row + outbox row in the same txn.\n'
  printf '[gitscale-hook]   If this is a read-only txn, ignore. Otherwise invoke gitscale-outbox-check.\n'
fi

exit 0
