#!/usr/bin/env bash
# lint-determinism.sh — enforces ADR-003 determinism constraints on workflow
# code. Reads regex rules from plane/workflow/lint/determinism-rules.txt and
# greps every workflow*.go / workflows/*.go file under plane/workflow/.
# activity*.go and *_test.go files are exempt — activities are the I/O
# boundary per ADR-003.
#
# Exit codes:
#   0  no violations
#   1  one or more violations (messages on stderr)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
LINT_DIR="${REPO_ROOT}/plane/workflow/lint"
RULES_FILE="${LINT_DIR}/determinism-rules.txt"
SCAN_ROOT="${REPO_ROOT}/plane/workflow"

# Optional override: WORKFLOW_LINT_SCAN_ROOT lets the integration test point
# the scanner at testdata/bad/ or testdata/good/ without touching real code.
if [[ -n "${WORKFLOW_LINT_SCAN_ROOT:-}" ]]; then
  SCAN_ROOT="${WORKFLOW_LINT_SCAN_ROOT}"
fi

if [[ ! -f "${RULES_FILE}" ]]; then
  echo "ERROR: rules file not found: ${RULES_FILE}" >&2
  exit 2
fi

# Read non-blank, non-comment lines from rules file into an array.
mapfile -t RULES < <(grep -Ev '^\s*(#|$)' "${RULES_FILE}")

if [[ ${#RULES[@]} -eq 0 ]]; then
  echo "ERROR: no rules loaded from ${RULES_FILE}" >&2
  exit 2
fi

# Collect the set of workflow Go files. Exclude *_test.go and activity files.
# testdata/ is excluded by default (Go toolchain convention); the integration
# test that exercises the lint sets WORKFLOW_LINT_SCAN_ROOT to point directly
# at testdata/bad or testdata/good — in that override mode the testdata
# exclusion is skipped because the caller is intentionally targeting it.
EXCLUDE_TESTDATA="-not -path */testdata/*"
if [[ -n "${WORKFLOW_LINT_SCAN_ROOT:-}" ]]; then
  EXCLUDE_TESTDATA=""
fi

# shellcheck disable=SC2086
mapfile -t TARGETS < <(
  find "${SCAN_ROOT}" -type f \
    \( -name 'workflow*.go' -o -path '*/workflows/*.go' \) \
    -not -name 'activity*.go' \
    -not -name '*_test.go' \
    ${EXCLUDE_TESTDATA} \
    2>/dev/null | sort
)

if [[ ${#TARGETS[@]} -eq 0 ]]; then
  echo "[ok] no workflow files to scan under ${SCAN_ROOT}"
  exit 0
fi

FAIL=0
for target in "${TARGETS[@]}"; do
  for rule in "${RULES[@]}"; do
    # -E extended regex; -n line numbers; -H filename always.
    if matches=$(grep -EnH "${rule}" "${target}" 2>/dev/null); then
      while IFS= read -r line; do
        echo "[FAIL] determinism violation: ${line}" >&2
        echo "       rule: ${rule}" >&2
        FAIL=1
      done <<< "${matches}"
    fi
  done
done

if [[ ${FAIL} -ne 0 ]]; then
  echo "" >&2
  echo "FAIL: workflow files contain determinism violations." >&2
  echo "      Fix: use workflow.Now(ctx), workflow.NewTimer(ctx, …)," >&2
  echo "           or wrap nondeterministic reads in workflow.SideEffect." >&2
  exit 1
fi

echo "[ok] determinism lint clean (${#TARGETS[@]} files, ${#RULES[@]} rules)"
exit 0
