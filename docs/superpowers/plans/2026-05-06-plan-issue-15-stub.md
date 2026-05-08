# Plan: #15-stub — Identity service interface + stub impl

**Date:** 2026-05-06
**Issue:** #15 (stub PR — first of three)
**Spec:** `2026-05-06-issue-15-identity-service-design.md`
**Branch:** `feat/application-identity-service-stub`
**Pre-merge of:** #14
**Blocks:** #15-postgres
**Parallel-safe with:** #35

## Pre-flight

- Confirm #14 merged.
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/application-identity-service-stub`
- Verify `plane/data/store.MetadataStore` + `plane/data/store/stub` exist.

## Step sequence

### Step 1 — Models

File: `plane/application/identity/models.go`

`HumanUser`, `AgentIdentity` structs per spec D2. Field set matches `001_identity.sql` columns.

### Step 2 — Event constants + payloads

File: `plane/application/identity/events.go`

Event-type constants: `EventUserCreated`, `EventAgentCreated`, `EventAgentReputationUpdated`, plus revocation set (constants only; no emitters yet).

Payload structs per spec D5 + D6 with `EnvelopeVersion = 1`.

### Step 3 — `CredentialHasher` interface + Argon2id default

File: `plane/application/identity/credential.go`

```go
type CredentialHasher interface {
    Hash(plaintext string) (string, error)
    Verify(plaintext, hashed string) (ok, needsRehash bool)
}

type Argon2idHasher struct {
    memoryKB    uint32 // default 64MB → 65536
    iterations  uint32 // default 3
    parallelism uint8  // default 2
    saltLen     uint32 // 16
    keyLen      uint32 // 32
}
```

Constants pinned in code with comment citing OWASP 2026 baseline. Test vectors (round-trip hash + verify) in `credential_test.go`.

### Step 4 — Service interface

File: `plane/application/identity/service.go`

Full interface per spec D2. All methods declared; revocation set marked `// implemented in #15-revocation` (returns sentinel in stub for now).

### Step 5 — Retry helper

File: `plane/application/identity/retry.go`

```go
func WithSerializableRetry(ctx context.Context, fn func() error) error {
    const maxAttempts = 3
    delay := 10 * time.Millisecond
    for attempt := 0; attempt < maxAttempts; attempt++ {
        if err := fn(); err == nil {
            return nil
        } else if !store.IsRetryable(err) {
            return err
        }
        // jittered sleep
    }
    return ErrRetryExhausted
}
```

### Step 6 — Stub service impl

File: `plane/application/identity/stub_service.go`

`stubService` wraps a `*stub.Store` from #14. All read methods consult the in-memory map. `CreateUser` / `CreateAgent` call `store.Transact` + `tx.WriteOutbox`. `SetAgentReputationScore` clamps + reads-current + writes-new + outbox in one Tx. Revocation methods return `ErrNotImplemented`.

### Step 7 — Service-level tests

File: `plane/application/identity/stub_service_test.go`

Subtests per service method (excluding revocation):

- `TestCreateUser_emitsUserCreated_inSameTx`
- `TestCreateUser_rollbackOnInvalidEmail_removesOutbox`
- `TestCreateUser_payloadCarriesMeteringFields`
- `TestCreateAgent_emitsAgentCreated_withMeteringFields`
- `TestCreateAgent_clampsInitialReputation`
- `TestSetAgentReputationScore_clampsToZeroOne`
- `TestSetAgentReputationScore_emitsDeltaPayload`
- `TestGetUserByEmail_caseInsensitive`
- `TestGetAgentsByParentUser_returnsAll`
- `TestLookupIdentityForCache_returnsCacheEntry`
- `TestCredentialHasher_roundTrip`
- `TestWithSerializableRetry_succeedsWithin3`
- `TestWithSerializableRetry_failsAfter3`

Use `stub.Store.Recorded()` to assert outbox writes.

### Step 8 — PR description

Cross-link spec + execution plan + ADR-019 (governing). Note: `cmd/identity-service` ships in #15-revocation; this PR is library-only.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./plane/application/identity/... && go build ./...` | exit 0 |
| Vet | `go vet ./plane/application/identity/...` | exit 0 |
| Unit | `go test -race ./plane/application/identity/...` | all pass |
| Coverage | manual review | ≥ 80% line coverage on stub_service.go |
| Lint | `golangci-lint run ./plane/application/identity/...` | clean |

## Acceptance checklist

- [ ] All non-revocation Service methods implemented in stub.
- [ ] All test cases listed in Step 7 green.
- [ ] `Argon2idHasher` round-trip works; parameters pinned.
- [ ] `WithSerializableRetry` correctly classifies retryable errors.
- [ ] Outbox payloads carry metering fields per spec D5.
- [ ] No imports outside the allowlist (spec D9).
- [ ] `cmd/identity-service` NOT added (deferred to #15-revocation).

## Risks

| Risk | Mitigation |
|---|---|
| Stub semantics drift from postgres (revealed in #15-postgres) | both run service-level tests; #35 compliance covers `MetadataStore` invariants |
| Argon2id parameters too aggressive for CI test runtime | `_test.go` uses lower params via internal constructor; production constants only via the default constructor |
| Revocation method placeholders confuse callers | sentinel error message includes "available in #15-revocation" |

## Rollback

Pure addition under new package; no caller exists yet. Revert is one PR drop.
