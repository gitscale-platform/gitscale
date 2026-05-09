#!/usr/bin/env bash
# lint-firecracker.sh — enforces ADR-002 Firecracker-only isolation in the CI
# runner packages (plane/workflow/runner/ and plane/workflow/ci/). Reads regex
# rules from plane/workflow/lint/firecracker-rules.txt and greps every .go
# file in those directories. *_test.go files are NOT exempt because a forbidden
# import in a test still pulls the package into the build graph.
#
# Exit codes:
#   0  no violations
#   1  one or more violations (messages on stderr)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
LINT_DIR="${REPO_ROOT}/plane/workflow/lint"
RULES_FILE="${LINT_DIR}/firecracker-rules.txt"

# Default scan roots: the two CI runner packages. Override via the env var
# below to point at testdata for the lint's own self-test.
DEFAULT_SCAN_ROOTS=(
  "${REPO_ROOT}/plane/workflow/runner"
  "${REPO_ROOT}/plane/workflow/ci"
)

if [[ -n "${FIRECRACKER_LINT_SCAN_ROOTS:-}" ]]; then
  # shellcheck disable=SC2206
  SCAN_ROOTS=( ${FIRECRACKER_LINT_SCAN_ROOTS} )
else
  SCAN_ROOTS=( "${DEFAULT_SCAN_ROOTS[@]}" )
fi

if [[ ! -f "${RULES_FILE}" ]]; then
  echo "ERROR: rules file not found: ${RULES_FILE}" >&2
  exit 2
fi

mapfile -t RULES < <(grep -Ev '^\s*(#|$)' "${RULES_FILE}")
if [[ ${#RULES[@]} -eq 0 ]]; then
  echo "ERROR: no rules loaded from ${RULES_FILE}" >&2
  exit 2
fi

# Collect target files: every .go file under each scan root, including tests.
TARGETS=()
for root in "${SCAN_ROOTS[@]}"; do
  if [[ ! -d "${root}" ]]; then
    continue
  fi
  while IFS= read -r f; do
    TARGETS+=( "${f}" )
  done < <(find "${root}" -type f -name '*.go' -not -path '*/testdata/*' 2>/dev/null | sort)
done

if [[ ${#TARGETS[@]} -eq 0 ]]; then
  echo "[ok] no runner/ci files to scan"
  exit 0
fi

FAIL=0
for target in "${TARGETS[@]}"; do
  for rule in "${RULES[@]}"; do
    if matches=$(grep -EnH "${rule}" "${target}" 2>/dev/null); then
      while IFS= read -r line; do
        echo "[FAIL] ADR-002 (Firecracker-only) violation: ${line}" >&2
        echo "       rule: ${rule}" >&2
        FAIL=1
      done <<< "${matches}"
    fi
  done
done

if [[ ${FAIL} -ne 0 ]]; then
  echo "" >&2
  echo "FAIL: CI runner code imports a forbidden container runtime." >&2
  echo "      ADR-002 mandates Firecracker microVMs as the only sandbox." >&2
  echo "      Remove the import; if a new sandbox is genuinely required," >&2
  echo "      open an ADR amendment first." >&2
  exit 1
fi

echo "[ok] Firecracker-only lint clean (${#TARGETS[@]} files, ${#RULES[@]} rules)"
exit 0
