# Spec: #15 Identity domain service

**Date:** 2026-05-06
**Issue:** #15 `[Application] Identity domain service: HumanUser + AgentIdentity CRUD over MetadataStore`
**Branches:** `feat/application-identity-service-stub`, `feat/application-identity-service-postgres`, `feat/application-identity-service-revocation`
**Depends on:** #14 (MetadataStore + Tx interfaces), #35 (compliance suite — gates postgres-arm only)
**Closes:** #5 Phase 1 epic (along with #14, #35)

## 1. Context

`plane/application/` is empty (only `doc.go`). #15 is the first application-plane service. It implements CRUD for `HumanUser` and `AgentIdentity` using the `MetadataStore` interface defined in #14, and is the first consumer of the outbox: state mutations atomically write source row + `identity_outbox` row in the same Tx (ADR-008).

#15 is also the first emitter of identity events — `user.created`, `agent.created`, `agent.reputation_updated`. The identity-cache invalidator (#27) and future metering plane both depend on these events.

ADRs: ADR-006 (PostgreSQL semantics), ADR-008 (outbox invariant), ADR-010 (JWT-SVID identity claims), ADR-017 (swap surface).

Review surfaced multiple decisions that justify a spec rather than executing the original issue body verbatim. This document locks them.

## 2. Decisions

### D1 — Ship #15 in three sequenced PRs

**Decision.**

| PR | Branch | Depends on | Scope |
|---|---|---|---|
| **#15-stub** | `feat/application-identity-service-stub` | #14 only | `Service` interface + `models.go` + in-memory stub impl + service-level tests using stub `MetadataStore` |
| **#15-postgres** | `feat/application-identity-service-postgres` | #14 + #35 | `postgres_service.go` wired against real `MetadataStore` postgres impl + integration tests |
| **#15-revocation** | `feat/application-identity-service-revocation` | #15-postgres | `DisableUser`, `RevokeAgent`, `UpdateAgentPermissions`, org membership writes + emitters; gRPC surface for `appclient.IdentityClient` |

**Rationale.** Original issue body bundled all three. Splitting reviews 1 merge cycle in parallel: #15-stub overlaps #35 (compliance suite). #15-revocation is the unblocker for #27.

### D2 — Service interface, final list

```go
// plane/application/identity/service.go

type Service interface {
    // Reads
    GetUser(ctx context.Context, id uuid.UUID) (*HumanUser, error)
    GetUserByEmail(ctx context.Context, email string) (*HumanUser, error)
    GetAgent(ctx context.Context, id uuid.UUID) (*AgentIdentity, error)
    GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]*AgentIdentity, error)
    LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*cache.IdentityCacheEntry, error)

    // Creates (#15-stub + #15-postgres)
    CreateUser(ctx context.Context, email, plaintextCredential string) (*HumanUser, error)
    CreateAgent(ctx context.Context, parentUserID uuid.UUID, displayName string, scope []string) (*AgentIdentity, error)

    // Reputation (#15-postgres)
    SetAgentReputationScore(ctx context.Context, agentID uuid.UUID, score float64) error

    // Revocation surface (#15-revocation)
    DisableUser(ctx context.Context, id uuid.UUID, reason string) error
    RevokeAgent(ctx context.Context, id uuid.UUID, reason string) error
    UpdateAgentPermissions(ctx context.Context, id uuid.UUID, scope []string) error

    // Org membership (#15-revocation)
    AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string) error
    RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error
}
```

**Method-level rationale.**

- `GetAgentsByParentUser` — uses existing `idx_agent_identities_parent_user_id`; PR engine + edge identity resolution will need it. Cheap to ship now.
- `LookupIdentityForCache` — loader callback for `cache.GetIdentity`. Without it, edge plane reaches past the service into `IdentityReader` (plane-boundary breach).
- `SetAgentReputationScore` — see D4.
- `DisableUser` / `RevokeAgent` / `UpdateAgentPermissions` — emit the events that #27 consumes.
- `AddOrgMember` / `RemoveOrgMember` — emit `org.member_added` / `org.member_removed` (the latter is one of #27's target events).

**Out of scope.** OAuth-app CRUD, `RotateCredential`, transfer of agent ownership, multi-org agent membership.

### D3 — `credential_hash` policy lives in the service

**Decision.** `CreateUser(ctx, email, plaintextCredential string)`. The service hashes via a `CredentialHasher` interface (ADR-017 swap surface):

```go
// plane/application/identity/credential.go
type CredentialHasher interface {
    Hash(plaintext string) (hashed string, err error)
    Verify(plaintext, hashed string) (ok bool, needsRehash bool)
}

// Default impl: argon2id with parameters m=64MB, t=3, p=2.
type Argon2idHasher struct{ /* params */ }
```

**Rationale.** If callers hash, three different hash functions emerge across edge / signup / admin paths within six months. The column constraint is "hash, not plaintext" — a security invariant. Hashing internal preserves that.

**Open follow-up.** Argon2id parameters MAY warrant their own ADR if cross-team review wants them pinned. For #15 purposes, pin in code with constants; revisit if compliance asks.

**Edge plane never sees plaintext** post-TLS-termination at Envoy. Signup endpoint (Phase-2) hands the service plaintext over a process boundary internal to the application plane.

### D4 — Reputation as `Set`, not `delta`

**Decision.** `SetAgentReputationScore(ctx, agentID, score float64)` clamps `score` to `[0.0, 1.0]` before write. Emits `agent.reputation_updated` with payload:

```json
{
  "agent_id": "<uuid>",
  "old_score": 0.8000,
  "new_score": 0.6500,
  "delta": -0.1500,
  "computed_at": "<RFC3339Nano>"
}
```

**Rejected.** `UpdateAgentReputationScore(ctx, agentID, delta float64)` (read-modify-write). Concurrent updates trigger 40001 retry storms; each retry replays the read-modify-write. Reputation scoring runs offline (analytics → compute new score → call service); the caller already knows the target score.

**Implication.** `delta` in the payload is computed by the service from `(new - old)` after read; this is a read step inside the same Tx that performs the write — cheap, no concurrency hazard.

### D5 — Outbox payload is metering-ready from day one

**Decision.** `agent.created` and `user.created` payloads include the metering plane's required fields even though no metering consumer exists yet:

```json
// agent.created
{
  "agent_id": "<uuid>",
  "parent_user_id": "<uuid>",
  "display_name": "code-reviewer",
  "permission_scope": ["repo:read", "issue:write"],
  "rate_bucket": "agent_default",
  "session_quota": 4,
  "tokens_per_week_cap": 100000000,
  "reputation_score": 0.5000,
  "quota_account_id": "<uuid|null>",
  "principal_class": "agent",
  "created_at": "<RFC3339Nano>",
  "_envelope_version": 1
}

// user.created
{
  "user_id": "<uuid>",
  "email": "<email>",
  "rate_bucket": "human_default",
  "quota_account_id": "<uuid|null>",
  "principal_class": "user",
  "created_at": "<RFC3339Nano>",
  "_envelope_version": 1
}
```

**Rationale.** Adding these fields after the fact requires a migration on every consumer (search, webhook, billing, audit) once they exist. Cheap to include now.

**`_envelope_version`** — payload-level version distinct from the envelope-level `EventEnvelope.version`. Allows in-place backwards-compatible evolution of identity payloads (per #12 spec D4) without bumping the topic version.

### D6 — Revocation event payloads include `affected_principal_ids[]`

**Decision.** `org.member_removed` and `principal.permissions_changed` include an explicit array of affected principal IDs, NOT just `aggregate_id`:

```json
// org.member_removed (aggregate_id = org_id)
{
  "org_id": "<uuid>",
  "removed_user_id": "<uuid>",
  "removed_by": "<uuid>",
  "affected_principal_ids": ["<user_uuid>"],
  "reason": "<string>",
  "removed_at": "<RFC3339Nano>",
  "_envelope_version": 1
}

// principal.permissions_changed (aggregate_id = agent_id or user_id)
{
  "principal_id": "<uuid>",
  "principal_class": "agent",
  "old_scope": ["repo:read"],
  "new_scope": ["repo:read", "issue:write"],
  "affected_principal_ids": ["<agent_uuid>"],
  "changed_at": "<RFC3339Nano>",
  "_envelope_version": 1
}
```

**Rationale.** #27 invalidates `IdentityKey<aggregate_id>`. For `org.member_removed`, `aggregate_id` is the org, not the user whose cache entry must die. Without `affected_principal_ids[]`, the invalidator either guesses or scans. Including the list makes #27's logic mechanical.

**Schema implication.** `events/identity/org.member_removed.schema.json` and `events/identity/principal.permissions_changed.schema.json` declare `affected_principal_ids` as required.

### D7 — Postgres impl uses `MetadataStore.Transact` exclusively

**Decision.** Every state-mutating method calls `store.Transact(ctx, func(tx Tx) error)`. Inside the Tx:

1. Validate input.
2. `tx.Identity().Insert<X>` (or update).
3. `tx.WriteOutbox(DomainIdentity, "human_user", userID, "user.created", payload)`.
4. Return nil → commit.

Outside the Tx: nothing observable to the caller.

**40001 handling.** `Transact` returns the original `40001` error. Service-level methods wrap with the `store.IsRetryable(err)` helper from #14 + bounded retry (max 3 attempts, 10–100ms jitter). Retry loop in `plane/application/identity/retry.go`.

**No ad-hoc Tx.** Service code never opens a Tx outside `MetadataStore.Transact`.

### D8 — Stub impl mirrors postgres semantics for testability

**Decision.** `plane/application/identity/stub_service.go` uses a stub `MetadataStore` (same factory as #35 compliance suite). Stub records:

- All inserted `HumanUser` / `AgentIdentity` rows.
- All outbox writes (domain, aggregate_type, aggregate_id, event_type, payload).

Service-level tests assert outbox writes happen in the same Tx as source-row writes (rollback removes both — the stub's `Tx` records writes only on commit).

**Skips.** 40001 race tests are skipped on the stub with explicit `t.Skip("40001 race covered by postgres compliance suite")`.

### D9 — Plane-boundary imports

**Allowed imports for `plane/application/identity/`:**

- `plane/data/store` (interface)
- `plane/data/cache` (interface, for `LookupIdentityForCache` consumers; service itself does not call cache)
- `plane/data/kafka` (envelope + topic constants only — no producer)

**Forbidden imports:**

- `plane/data/outbox` (only the polling consumer publishes)
- `plane/data/store/postgres` (concrete) — the service receives the interface from main
- Any `pgx*` package — the interface hides it
- Any other plane's internal package

### D10 — Wiring + binary

**Decision.** Identity service ships as a library + a thin binary:

```
plane/application/identity/         # library (Service + impl + models)
cmd/identity-service/main.go        # gRPC server (#15-revocation lands the gRPC surface)
```

#15-stub + #15-postgres ship the library only. #15-revocation lands `cmd/identity-service/main.go` along with the gRPC surface that `appclient.IdentityClient` (from #33) consumes.

## 3. Files to add

### #15-stub

```
plane/application/identity/doc.go
plane/application/identity/models.go            # HumanUser, AgentIdentity
plane/application/identity/service.go           # Service interface
plane/application/identity/credential.go        # CredentialHasher + Argon2idHasher
plane/application/identity/retry.go             # bounded serializable retry helper
plane/application/identity/events.go            # event-type constants + payload structs
plane/application/identity/stub_service.go      # uses stub MetadataStore from #14
plane/application/identity/stub_service_test.go
```

### #15-postgres

```
plane/application/identity/postgres_service.go
plane/application/identity/postgres_service_test.go
plane/application/identity/integration_test.go  # testcontainers PG; asserts source+outbox same Tx
plane/data/events/identity/user.created.schema.json
plane/data/events/identity/agent.created.schema.json
plane/data/events/identity/agent.reputation_updated.schema.json
```

### #15-revocation

```
plane/application/identity/revocation.go
plane/application/identity/revocation_test.go
plane/application/identity/integration_revocation_test.go
plane/data/events/identity/user.disabled.schema.json
plane/data/events/identity/agent.revoked.schema.json
plane/data/events/identity/principal.permissions_changed.schema.json
plane/data/events/identity/org.member_added.schema.json
plane/data/events/identity/org.member_removed.schema.json
internal/proto/identity/v1/identity.proto
internal/proto/identity/v1/identity.pb.go      # generated
internal/proto/identity/v1/identity_grpc.pb.go # generated
cmd/identity-service/main.go
plane/workflow/appclient/identity_grpc.go      # gRPC impl of appclient.IdentityClient
```

## 4. Files to modify

| Path | Change |
|---|---|
| `plane/data/store/postgres/identity_reader.go` | bodies filled in #14; #15 uses them |
| `plane/data/store/postgres/identity_writer.go` | bodies filled in #14; #15 uses them |
| `plane/workflow/appclient/identity.go` | gRPC client impl in #15-revocation (interface from #33) |

## 5. Implementation plan

### #15-stub (depends on #14 only)

1. `models.go` — domain types.
2. `events.go` — event type constants + payload structs (D5, D6).
3. `service.go` — `Service` interface (D2).
4. `credential.go` — `CredentialHasher` + `Argon2idHasher`.
5. `retry.go` — `WithSerializableRetry` helper using `store.IsRetryable`.
6. `stub_service.go` — implementation atop stub `MetadataStore`.
7. `stub_service_test.go` — unit tests for every method, asserting outbox writes via stub recorder.
8. PR opens as draft alongside #35.

### #15-postgres (depends on #14 + #35)

9. `postgres_service.go` — impl using real `MetadataStore.Transact`.
10. `postgres_service_test.go` — uses postgres `MetadataStore` against testcontainers.
11. `integration_test.go` — asserts source row + outbox row in same Tx; rollback removes both.
12. JSON schemas for `user.created`, `agent.created`, `agent.reputation_updated`.
13. Run `lint-events` (from #31) — schemas must validate.
14. PR opens after #35 merges.

### #15-revocation (depends on #15-postgres)

15. `revocation.go` — `DisableUser`, `RevokeAgent`, `UpdateAgentPermissions`, `AddOrgMember`, `RemoveOrgMember` impls.
16. JSON schemas for `user.disabled`, `agent.revoked`, `principal.permissions_changed`, `org.member_added`, `org.member_removed`.
17. `internal/proto/identity/v1/identity.proto` + generation.
18. `cmd/identity-service/main.go` — gRPC server.
19. `plane/workflow/appclient/identity_grpc.go` — gRPC client impl of `IdentityClient` interface from #33.
20. Integration test for full revocation flow + cache invalidation observability.

## 6. Acceptance criteria

### #15-stub

- [ ] `Service` interface exports the methods listed in D2.
- [ ] `Argon2idHasher` hashes + verifies; pinned parameters documented in code.
- [ ] Stub impl passes service-level test suite.
- [ ] All outbox writes carry metering-ready payloads (D5).
- [ ] `go test ./plane/application/identity/...` clean (unit only).

### #15-postgres

- [ ] Postgres impl uses `MetadataStore.Transact` exclusively.
- [ ] Integration test: `CreateUser` writes `human_users` row + `identity_outbox` row in same Tx; rollback removes both.
- [ ] Integration test: 40001 retry helper succeeds within 3 attempts under contended writes.
- [ ] All identity event schemas validate against `lint-events`.
- [ ] `go test -tags integration ./plane/application/identity/...` clean.

### #15-revocation

- [ ] All revocation methods emit the documented events with `affected_principal_ids[]`.
- [ ] gRPC service compiles and serves; `appclient.IdentityClient` round-trips successfully.
- [ ] `cmd/identity-service` boots against docker-compose.
- [ ] #27 integration test (under #27's PR) consumes a `user.disabled` event end-to-end.

## 7. Open follow-ups (not in this issue)

- **Argon2id parameter ADR** — file if cross-team review wants them governance-locked.
- **Multi-org agent membership** — out of scope; current schema permits one parent_user only.
- **OAuth-app CRUD** — separate issue post-Phase-1.
- **Identity gRPC surface for edge plane** — `cmd/identity-service` ships in #15-revocation; edge plane integration is Phase-2.
- **Metering plane consumer** — D5 makes the event payloads metering-ready; the consumer itself is Phase-2.

## 8. Risk mitigations

| Risk | Mitigation |
|---|---|
| Service interface grows ad-hoc across 3 PRs | This spec locks the final list; deviation requires a spec amendment |
| 40001 retry helper used inconsistently across methods | All state mutations route through `WithSerializableRetry`; lint check `grep -L "WithSerializableRetry" postgres_service.go` in CI |
| Stub drift from postgres impl | Compliance suite (#35) is shared; service-level tests use stub MetadataStore + assert outbox-recorder shape mirrors postgres dispatch table |
| Argon2id params ship with weak defaults | Pinned at m=64MB, t=3, p=2 (OWASP 2026 baseline); ADR follow-up if reviewer wants stricter |
| `affected_principal_ids[]` missing from old payload schemas in legacy tools | These are new event types; no legacy consumers exist; payload version pinned at 1 |

## 9. Cross-references

- ADR-006 (PostgreSQL), ADR-008 (outbox), ADR-010 (JWT-SVID identity claims), ADR-017 (swap surface).
- ADR-019 (workflow→app-plane boundary, this PR cycle) — `cmd/identity-service` is the gRPC target.
- #14 (MetadataStore + Tx) — direct dependency.
- #35 (compliance suite) — gates #15-postgres.
- #27 (identity cache invalidator) — consumes #15-revocation events.
- #33 (workflow bootstrap) — `appclient.IdentityClient` interface defined there, impl lands here.
- `gitscale-outbox-check` skill — D7 enforces the dual-write avoidance.
- `gitscale-event-schema` skill — D5/D6 schema additions trigger backwards-compat review.
