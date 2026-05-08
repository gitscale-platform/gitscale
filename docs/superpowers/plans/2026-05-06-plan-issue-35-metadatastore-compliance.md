# Plan: #35 — MetadataStore compliance suite

**Date:** 2026-05-06
**Issue:** #35
**Spec:** none (issue body + execution-plan §13.1 UUIDv7 assertion)
**Branch:** `feat/data-metadatastore-compliance-suite`
**Pre-merge of:** #14 (`MetadataStore`, `Tx`, stub + postgres impls)
**Blocks:** #15-postgres

## Pre-flight

- Confirm #14 merged on main.
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/data-metadatastore-compliance-suite`
- Confirm `plane/data/compliance/{cachestore,eventqueue,ratelimiter}.go` already exist (template to mirror).

## Step sequence

### Step 1 — Factory type

File: `plane/data/compliance/metadatastore.go`

```go
type MetadataStoreFactory func(t *testing.T) (s store.MetadataStore, cleanup func())

func RunMetadataStoreCompliance(t *testing.T, factory MetadataStoreFactory) {
    t.Run("Transact_commit_on_nil_error", func(t *testing.T) { ... })
    t.Run("Transact_rollback_on_non_nil_error", func(t *testing.T) { ... })
    t.Run("WriteOutbox_transactional_invariant", func(t *testing.T) { ... })
    t.Run("WriteOutbox_domain_allowlist", func(t *testing.T) { ... })
    t.Run("WriteOutbox_table_dispatch", func(t *testing.T) { ... })
    t.Run("Serializable_retry_contract_40001", func(t *testing.T) { ... })
    t.Run("EventID_monotonic_uuidv7", func(t *testing.T) { ... })
    t.Run("Domain_reader_stubs_return_sentinel", func(t *testing.T) { ... })
}
```

### Step 2 — Per-case implementations

For each subtest:

| Case | Asserts |
|---|---|
| Transact basics | commit on nil; rollback on non-nil; no partial writes after rollback |
| Transactional invariant | source row + outbox row commit atomically; rollback removes both (query each table) |
| Domain allowlist | `WriteOutbox(Domain("unknown"), …)` errors before any DB write; all 5 domains succeed |
| Table dispatch | `domain=DomainIdentity` writes to `identity_outbox`; assert via `SELECT FROM <table>` after each |
| 40001 race | two goroutines, both serializable, both `SELECT … FOR UPDATE` on `agent_identities.reputation_score`, both UPDATE; loser gets `40001`; `IsRetryable(err)==true` |
| EventID UUIDv7 | sample N=100 outbox writes; assert each `event_id` is UUIDv7; assert monotonic across single-thread inserts |
| Stub sentinel | calling un-filled reader (e.g. `RepositoryReader.GetBySlug`) returns sentinel error, never panics |

### Step 3 — Postgres wiring

File: `plane/data/store/postgres/compliance_test.go`

```go
func TestPostgresCompliance(t *testing.T) {
    factory := func(t *testing.T) (store.MetadataStore, func()) {
        // testcontainers PG; apply migrations 000-005; return store + cleanup
    }
    compliance.RunMetadataStoreCompliance(t, factory)
}
```

Reuse `plane/data/outbox/integration_test.go::testDB` helper (already does container + migration apply).

### Step 4 — Stub wiring

File: `plane/data/store/stub/compliance_test.go`

```go
func TestStubCompliance(t *testing.T) {
    factory := func(t *testing.T) (store.MetadataStore, func()) {
        return stub.New(), func() {}
    }
    compliance.RunMetadataStoreCompliance(t, factory)
}
```

Stub explicitly skips `Serializable_retry_contract_40001` with `t.Skip("40001 race covered by postgres compliance")`. Document in `plane/data/compliance/metadatastore.go` doc comment.

### Step 5 — Update postgres impl gaps surfaced by compliance

If 40001 case fails (e.g. `Transact` not actually serializable), fix in `plane/data/store/postgres/metadata.go`. Compliance is the gate.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./plane/data/compliance/... && go build ./...` | exit 0 |
| Postgres compliance | `go test -tags integration -race -run TestPostgresCompliance ./plane/data/store/postgres/...` | all 8 subtests pass |
| Stub compliance | `go test -race -run TestStubCompliance ./plane/data/store/stub/...` | 7 pass + 1 skip |
| 40001 case timing | run 5 times | no flakes |

## Acceptance checklist

- [ ] `plane/data/compliance/metadatastore.go` exports `RunMetadataStoreCompliance` + `MetadataStoreFactory`.
- [ ] All 8 cases listed above implemented.
- [ ] Postgres compliance test passes against testcontainers PG.
- [ ] Stub compliance passes; 40001 skip is explicit.
- [ ] Each domain (`identity`, `repositories`, `collaboration`, `ci`, `billing`) is exercised at least once in dispatch test.
- [ ] EventID test asserts UUIDv7 (not just any UUID).
- [ ] PR description cross-links #14 + #35; closes #35.

## Risks

| Risk | Mitigation |
|---|---|
| `parent_user_id` FK in agent_identities forces test users to exist before agents | factory seeds a baseline user during cleanup setup |
| testcontainers slow in CI | reuse the existing helper; cache image |
| 40001 race flake | bound retry attempts; if no `40001` after 5 attempts, fail with explicit message |
| Compliance suite imports postgres impl directly | suite imports only `plane/data/store` interface; postgres binding is in caller's `_test.go` only |

## Rollback

Test-only PR; pure addition. Revert if compliance surfaces a #14 bug — fix it as a separate PR rather than reverting the suite.
