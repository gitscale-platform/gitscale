#!/usr/bin/env bash
# Reject test files that declare a kind-axis tag (perf, chaos_link, chaos_blast)
# without also declaring a topology-axis tag (topo_single, topo_quorum, topo_full).
# Issue #132 §4.3.

set -euo pipefail

KIND_TAGS='perf|chaos_link|chaos_blast'
TOPO_TAGS='topo_single|topo_quorum|topo_full'

violations=0
while IFS= read -r -d '' f; do
  head -n 5 "$f" | grep -qE "^//go:build .*($KIND_TAGS)" || continue
  if ! head -n 5 "$f" | grep -qE "^//go:build .*($TOPO_TAGS)"; then
    echo "lint-test-tags: $f declares a kind tag without a topology tag"
    violations=$((violations+1))
  fi
done < <(find test/scenarios -name '*_test.go' -print0 2>/dev/null)

if [ "$violations" -gt 0 ]; then
  echo "lint-test-tags: $violations violation(s). See test/scenarios/README.md."
  exit 1
fi
echo "lint-test-tags: ok"
