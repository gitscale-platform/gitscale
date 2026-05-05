#!/usr/bin/env bash
# lint-events.sh — validates the Kafka topic definitions and event envelope schema.
#
# Called by `make lint-events`. Required tools: yq, ajv (ajv-cli).
# Install: npm install -g ajv-cli ajv-formats && brew install yq  (or apt-get install yq)
#
# Exit codes:
#   0  all checks pass
#   1  one or more checks failed (messages written to stderr)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
KAFKA_DIR="${REPO_ROOT}/plane/data/kafka"
EVENTS_DIR="${REPO_ROOT}/plane/data/events"

FAIL=0

# ── helpers ───────────────────────────────────────────────────────────────────

log_ok()   { echo "  [ok]  $*"; }
log_fail() { echo "  [FAIL] $*" >&2; FAIL=1; }

require_tool() {
  if ! command -v "$1" &>/dev/null; then
    echo "ERROR: required tool '$1' not found. Install: $2" >&2
    exit 1
  fi
}

# ── prerequisite tools ────────────────────────────────────────────────────────

require_tool yq   "brew install yq  OR  apt-get install yq"
require_tool ajv  "npm install -g ajv-cli ajv-formats"

# ── check 1: topics.yaml is valid YAML and contains required top-level keys ──

echo "==> Check 1: topics.yaml structure"

TOPICS_YAML="${KAFKA_DIR}/topics.yaml"

if ! yq eval '.' "${TOPICS_YAML}" >/dev/null 2>&1; then
  log_fail "topics.yaml is not valid YAML"
else
  log_ok "topics.yaml parses as valid YAML"
fi

# Every topic must declare key_field: aggregate_id (ADR-004)
TOPICS_WITHOUT_KEY=$(yq eval '.topics[] | select(.key_field != "aggregate_id") | .name' "${TOPICS_YAML}" 2>/dev/null || true)
if [[ -n "${TOPICS_WITHOUT_KEY}" ]]; then
  log_fail "topics missing key_field: aggregate_id (ADR-004): ${TOPICS_WITHOUT_KEY}"
else
  log_ok "all topics declare key_field: aggregate_id"
fi

# Every main topic (not DLQ) must have an acls: block with producer_spiffe_id
TOPICS_WITHOUT_ACLS=$(yq eval '.topics[] | select(.name | test("^(?!.*\\.dlq$)")) | select((.acls | has("producer_spiffe_id")) | not) | .name' "${TOPICS_YAML}" 2>/dev/null || true)
if [[ -n "${TOPICS_WITHOUT_ACLS}" ]]; then
  log_fail "main topics missing acls.producer_spiffe_id (ADR-010): ${TOPICS_WITHOUT_ACLS}"
else
  log_ok "all main topics declare acls.producer_spiffe_id"
fi

# No billing topic should use infinite retention (retention_ms must be finite / > 0)
BILLING_INFINITE=$(yq eval '.topics[] | select(.name | test("billing")) | select(.retention_ms == null or .retention_ms == 0) | .name' "${TOPICS_YAML}" 2>/dev/null || true)
if [[ -n "${BILLING_INFINITE}" ]]; then
  log_fail "billing topics with missing/zero retention_ms (Kafka is operational-only, ADR-008): ${BILLING_INFINITE}"
else
  log_ok "billing topics have finite retention_ms"
fi

# ── check 2: envelope.schema.json is valid JSON ───────────────────────────────

echo "==> Check 2: envelope.schema.json is valid JSON"

ENVELOPE_SCHEMA="${KAFKA_DIR}/envelope.schema.json"

if ! python3 -c "import json,sys; json.load(open('${ENVELOPE_SCHEMA}'))" 2>/dev/null; then
  log_fail "envelope.schema.json is not valid JSON"
else
  log_ok "envelope.schema.json parses as valid JSON"
fi

# ── check 3: validate event schemas in plane/data/events/ ────────────────────

echo "==> Check 3: event schemas (plane/data/events/)"

if [[ -d "${EVENTS_DIR}" ]]; then
  SCHEMA_COUNT=0
  FIXTURE_COUNT=0
  FIXTURE_FAIL=0

  while IFS= read -r -d '' schema_file; do
    SCHEMA_COUNT=$((SCHEMA_COUNT + 1))
    domain_event="$(basename "${schema_file}" .schema.json)"
    testdata_dir="$(dirname "${schema_file}")/${domain_event}.testdata"

    # validate the schema is valid JSON
    if ! python3 -c "import json,sys; json.load(open('${schema_file}'))" 2>/dev/null; then
      log_fail "invalid JSON: ${schema_file}"
      continue
    fi

    # every schema must have at least one fixture
    if [[ ! -d "${testdata_dir}" ]]; then
      log_fail "missing testdata dir for schema: ${schema_file} (expected: ${testdata_dir}/)"
      FIXTURE_FAIL=1
      continue
    fi

    fixture_files=("${testdata_dir}"/*.json)
    if [[ ${#fixture_files[@]} -eq 0 ]] || [[ ! -f "${fixture_files[0]}" ]]; then
      log_fail "no fixture files in ${testdata_dir}/ — at least one required"
      FIXTURE_FAIL=1
      continue
    fi

    # validate each fixture against its schema
    for fixture in "${testdata_dir}"/*.json; do
      FIXTURE_COUNT=$((FIXTURE_COUNT + 1))
      if ! ajv validate --spec=draft2020 --strict=false -s "${schema_file}" -d "${fixture}" 2>/dev/null; then
        log_fail "fixture validation failed: ${fixture} against ${schema_file}"
        FIXTURE_FAIL=1
      fi
    done
  done < <(find "${EVENTS_DIR}" -name '*.schema.json' -print0 2>/dev/null)

  if [[ ${SCHEMA_COUNT} -eq 0 ]]; then
    log_ok "no event schemas yet in ${EVENTS_DIR}/ (domain schemas filed under per-domain issues)"
  else
    if [[ ${FIXTURE_FAIL} -eq 0 ]]; then
      log_ok "${SCHEMA_COUNT} schemas, ${FIXTURE_COUNT} fixtures — all valid"
    fi
  fi
else
  log_ok "plane/data/events/ not yet created — skipping event schema checks"
fi

# ── check 4: every event_type string literal in Go has a schema file ─────────

echo "==> Check 4: event_type literals in Go code have corresponding schema files"

if [[ -d "${EVENTS_DIR}" ]]; then
  # Extract quoted strings assigned to event_type or EventType fields in Go files.
  # Pattern matches: event_type: "foo.bar"  or  EventType: "foo.bar"
  while IFS= read -r event_type; do
    # derive domain from the first segment before the dot
    domain="${event_type%%.*}"
    schema_file="${EVENTS_DIR}/${domain}/${event_type}.schema.json"
    if [[ ! -f "${schema_file}" ]]; then
      log_fail "event_type '${event_type}' used in Go code but no schema at ${schema_file}"
    fi
  done < <(grep -rh --include='*.go' --exclude='*_test.go' -oP '(?:event_type|EventType):\s*"([a-z_]+\.[a-z_]+)"' "${REPO_ROOT}/plane" 2>/dev/null \
    | grep -oP '"[a-z_]+\.[a-z_]+"' | tr -d '"' | sort -u)

  log_ok "event_type literal check complete"
else
  log_ok "plane/data/events/ not yet created — skipping event_type literal check"
fi

# ── result ────────────────────────────────────────────────────────────────────

echo ""
if [[ ${FAIL} -ne 0 ]]; then
  echo "FAIL: one or more lint-events checks failed (see above)" >&2
  exit 1
fi
echo "OK: all lint-events checks passed"
