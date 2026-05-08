# Plan: #14 — MetadataStore + Tx interfaces

**Date:** 2026-05-06
**Issue:** #14
**Spec:** none (issue body + execution-plan §13.1 amendments)
**Branch:** `feat/data-store-interfaces`
**Closes:** none directly; unblocks #35 + #15-stub
**Pre-merge of:** none required (compiles standalone; integration test against existing `identity_outbox` from #19)

## Pre-flight

- `git fetch && git checkout main && git pull`
- `git checkout -b feat/data-store-interfaces`
- Confirm `plane/data/migrations/001_identity.sql` exists and `identity_outbox` is part of it.
- Confirm `plane/data/cache/` package shape — mirror it for layout.

## Step sequence

### Step 1 — Domain enum + topic helper

File: `plane/data/store/domain.go`

```go
package store

type Domain string

const (
    DomainIdentity      Domain = "identity"
    DomainRepositories  Domain = "repositories"
    DomainCollaboration Domain = "collaboration"
    DomainCI            Domain = "ci"
    DomainBilling       Domain = "billing"
)

func (d Domain) Valid() bool { /* allowlist check */ }
func (d Domain) OutboxTable() string // returns "<domain>_outbox"
```

Verify: `go vet ./plane/data/store/...` clean.

### Step 2 — Interfaces

File: `plane/data/store/metadata.go`

- `MetadataStore` interface (`Transact`, `Identity()`, `Repositories()`).
- `Tx` interface (`Identity()`, `Repositories()`, `WriteOutbox(ctx, Domain, aggregateType string, aggregateID uuid.UUID, eventType string, payload any) error`).
- `IdentityReader` / `IdentityWriter` / `RepositoryReader` / `RepositoryWriter` interfaces with method bodies for currently-merged schema (per execution-plan §13.1: fill in #14, do NOT punt to #15).

Methods to fill on `IdentityReader`: `GetUserByID`, `GetUserByEmail`, `GetAgentByID`, `GetAgentsByParentUser`, `LookupIdentityForCache`.

Methods to fill on `IdentityWriter`: `InsertHumanUser`, `InsertAgentIdentity`, `SetAgentReputationScore`, `DisableUser`, `RevokeAgent`, `UpdateAgentPermissions`, `AddOrgMember`, `RemoveOrgMember`. (Bodies stay `errNotImplemented` for the revocation set since #15-revocation owns wiring; signatures land here so #15-stub can compile against the interface.)

Methods on `RepositoryReader` / `RepositoryWriter`: `GetByID`, `GetBySlug`, `Insert`, `UpdatePermissions` — bodies `errNotImplemented` until repository service work begins.

### Step 3 — Retryable error helper

File: `plane/data/store/retryable.go`

```go
func IsRetryable(err error) bool // checks pgconn SQLState == "40001"
```

Tested with both pgx-wrapped and bare errors.

### Step 4 — Postgres impl

File: `plane/data/store/postgres/metadata.go`

- `Store` struct over `*pgxpool.Pool`.
- `Transact(ctx, fn)` — opens `BEGIN ISOLATION LEVEL SERIALIZABLE`; calls fn(tx); commits on nil error; rolls back on non-nil; surfaces `40001` to caller.
- `WriteOutbox` — validates `domain.Valid()`, dispatches to `<domain>_outbox` via prepared INSERT.
- Reader/writer impls that have schema (per Step 2) execute real SQL; revocation + repository writers return sentinel.

File: `plane/data/store/postgres/identity_reader.go`, `identity_writer.go` — split for review hygiene.

### Step 5 — Stub impl

File: `plane/data/store/stub/metadata.go`

- In-memory `Store` recording all inserts + outbox writes.
- `Transact` simulates serializable: commit applies recorded ops; rollback discards.
- Records exposed via `Recorded()` for test assertions.

### Step 6 — Compliance hooks

File: `plane/data/store/postgres/compliance_test.go` — empty placeholder; #35 fills.
File: `plane/data/store/stub/compliance_test.go` — empty placeholder; #35 fills.

### Step 7 — UUIDv7 generator

File: `plane/data/store/eventid.go`

```go
func NewEventID() uuid.UUID // monotonic UUIDv7
```

Used by `WriteOutbox` for outbox row's `event_id` if caller doesn't supply one. Avoids collision risk under concurrent inserts (architect review).

### Step 8 — Refactor `wiring.DomainConfig`

File: `plane/data/outbox/wiring/wiring.go`

- Replace string-typed `Domain` field with `store.Domain`.
- `AllDomains` becomes `[]store.Domain` populated from store constants.

Verify: `go build ./plane/data/outbox/...` clean (no caller drift).

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./plane/data/store/... && go build ./...` | exit 0 |
| Vet | `go vet ./plane/data/store/...` | exit 0 |
| Unit | `go test -race ./plane/data/store/...` | pass |
| Integration | `go test -tags integration -race ./plane/data/store/postgres/...` | pass — testcontainers PG, exercises `WriteOutbox` against real `identity_outbox` |
| 40001 race | manual + integration test in `postgres_test.go` | second tx gets `40001` |
| Lint | `golangci-lint run ./plane/data/store/...` | clean |

## Acceptance checklist

- [ ] `plane/data/store/{domain.go, metadata.go, retryable.go, eventid.go}` compile.
- [ ] `plane/data/store/postgres/` implements `MetadataStore` against `*pgxpool.Pool`.
- [ ] `plane/data/store/stub/` provides in-memory test double with `Recorded()` accessor.
- [ ] `WriteOutbox(DomainIdentity, …)` writes into `identity_outbox`; rollback removes it.
- [ ] `IsRetryable(err)` returns true for `40001` and false for everything else.
- [ ] `wiring.DomainConfig` refactored to use `store.Domain`.
- [ ] No `EventQueue` interface introduced.
- [ ] Branch closes #14 in PR description; spec/execution-plan cross-links present.

## Rollback

Pure additions in a new package + a single typed-refactor in `wiring.DomainConfig`. If post-merge regression in outbox consumer, revert PR — `wiring.DomainConfig` field rename is the only call-site touched.

## Risks

| Risk | Mitigation |
|---|---|
| `pgxpool.Pool` import in `plane/data/store/postgres/` accidentally re-exported | only the postgres subpackage imports pgx; package boundary enforced by `gitscale-plane-boundary` skill |
| Stub diverges from postgres on rollback semantics | both run the #35 compliance suite |
| Domain enum refactor leaks pgx | `store.Domain` is a plain string-typed alias; no pgx dep |
| 40001 race test flaky in CI | use `sync.WaitGroup` + chan to interleave; document expected outcome |
