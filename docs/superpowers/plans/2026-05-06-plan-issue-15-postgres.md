# Plan: #15-postgres — Identity service postgres impl

**Date:** 2026-05-06
**Issue:** #15 (postgres PR — second of three)
**Spec:** `2026-05-06-issue-15-identity-service-design.md`
**Branch:** `feat/application-identity-service-postgres`
**Pre-merge of:** #14, #35, #15-stub
**Blocks:** #15-revocation; closes #5 Phase 1 epic together with #14, #35, #15-stub

## Pre-flight

- Confirm #14, #35, #15-stub all merged on main.
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/application-identity-service-postgres`
- Verify postgres `MetadataStore` impl from #14 passes #35's compliance suite.

## Step sequence

### Step 1 — Postgres service

File: `plane/application/identity/postgres_service.go`

`postgresService` wraps a `store.MetadataStore`. Each method:

```go
func (s *postgresService) CreateUser(ctx context.Context, email, plaintextCred string) (*HumanUser, error) {
    hash, err := s.hasher.Hash(plaintextCred)
    if err != nil { return nil, err }

    user := &HumanUser{
        ID:             uuid.Must(uuid.NewV7()),
        Email:          normalizeEmail(email),
        CredentialHash: hash,
        RateBucket:     "human_default",
        CreatedAt:      time.Now().UTC(),
    }

    err = WithSerializableRetry(ctx, func() error {
        return s.store.Transact(ctx, func(tx store.Tx) error {
            if err := tx.Identity().InsertHumanUser(ctx, user); err != nil {
                return err
            }
            return tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", user.ID, EventUserCreated, userCreatedPayload(user))
        })
    })
    if err != nil { return nil, err }
    return user, nil
}
```

Same pattern for `CreateAgent` (writes `agent_identities` + outbox). `SetAgentReputationScore` reads-then-writes inside the same Tx (single round trip).

### Step 2 — JSON schemas

Files:

- `plane/data/events/identity/user.created.schema.json`
- `plane/data/events/identity/agent.created.schema.json`
- `plane/data/events/identity/agent.reputation_updated.schema.json`

Each declares all fields from spec D5; `additionalProperties: false`; `_envelope_version: const 1`.

### Step 3 — `lint-events` validation

Run `make lint-events`; resolve any failures (key field, partition key, schema completeness).

### Step 4 — Postgres-specific service tests

File: `plane/application/identity/postgres_service_test.go`

Same suite as `stub_service_test.go` but configured with postgres factory. Reuse `compliance.MetadataStoreFactory` from #35 to spin testcontainers + apply migrations. Tag with `//go:build integration`.

### Step 5 — Integration test

File: `plane/application/identity/integration_test.go`

```go
//go:build integration

func TestCreateUser_atomicity(t *testing.T) {
    pgStore, cleanup := newPostgresStore(t)
    defer cleanup()
    s := newPostgresService(pgStore, defaultHasher())

    user, err := s.CreateUser(ctx, "alice@example.com", "S3cret!1234")
    require.NoError(t, err)

    // Source row exists
    var got HumanUser
    require.NoError(t, queryUserByID(pgStore.Pool(), user.ID, &got))
    require.Equal(t, user.Email, got.Email)

    // Outbox row exists with same UUIDv7-ish event_id and matching payload
    var outboxCount int
    require.NoError(t, pgStore.Pool().QueryRow(ctx,
        "SELECT count(*) FROM identity.identity_outbox WHERE aggregate_id=$1 AND event_type=$2",
        user.ID, EventUserCreated,
    ).Scan(&outboxCount))
    require.Equal(t, 1, outboxCount)
}

func TestCreateUser_rollback_removes_both(t *testing.T) { ... }
func TestSetAgentReputationScore_under_contention_retries(t *testing.T) { ... }
```

### Step 6 — Wiring example

File: `plane/application/identity/example_test.go`

Documentation-grade example showing how a future `cmd/identity-service` will wire the postgres service together. No binary in this PR.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Unit | `go test -race ./plane/application/identity/...` | pass |
| Integration | `go test -tags integration -race ./plane/application/identity/...` | pass |
| Event lint | `make lint-events` | pass |
| Markdown lint | `make lint-md` | pass |
| Coverage | review | postgres_service.go ≥ 75% |

## Acceptance checklist

- [ ] `postgresService` implements all non-revocation `Service` methods.
- [ ] Each create method writes source row + outbox row in same `Transact`.
- [ ] Integration test proves atomicity (rollback removes both).
- [ ] Integration test proves 40001 retry success within 3 attempts under simulated contention.
- [ ] All three event schemas validate against `lint-events`.
- [ ] No stub references in postgres_service.go.
- [ ] Closes #5 (in PR description, alongside #14, #35, #15-stub references).

## Risks

| Risk | Mitigation |
|---|---|
| Email normalization changes break existing rows in tests | normalize on input only; existing rows unchanged |
| 40001 retry mishandled when outbox payload changes mid-retry | retry boundary is per-Tx; payload is recomputed on each attempt — already correct |
| `lint-events` schema drift | schemas land in same PR with payload structs; CI gate catches drift |
| Migration overlay (PG-only partitioning from #21) interacts oddly with serializable | partitions are hash-by-`repo_id`; identity tables are not partitioned; no interaction |
| Race in `SetAgentReputationScore` reads stale row under serializable | retry helper handles `40001`; if non-retryable, propagates |

## Rollback

Revert undoes service code + schemas; nothing else depends on them yet (#15-revocation hasn't shipped). One-PR revert is safe.
