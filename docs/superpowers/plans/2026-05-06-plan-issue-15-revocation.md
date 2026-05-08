# Plan: #15-revocation — Identity revocation methods + emitters + gRPC service

**Date:** 2026-05-06
**Issue:** #15 (revocation PR — third of three) + new "identity revocation methods" issue (file before this PR opens)
**Spec:** `2026-05-06-issue-15-identity-service-design.md` (D6, D2 revocation set)
**Branch:** `feat/application-identity-service-revocation`
**Pre-merge of:** #15-postgres, #33 (for `appclient.IdentityClient` interface)
**Blocks:** #27 (cache invalidator)

## Pre-flight

- Confirm #15-postgres + #33 merged on main.
- File the new "identity revocation methods" issue first if not yet filed (gates this PR per execution-plan §13.6).
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/application-identity-service-revocation`
- Verify `plane/workflow/appclient/identity.go` exports the `IdentityClient` interface.

## Step sequence

### Step 1 — Revocation method impls

File: `plane/application/identity/revocation.go`

Methods (all serializable Tx, all emit outbox):

```go
DisableUser(ctx, id, reason) error
RevokeAgent(ctx, id, reason) error
UpdateAgentPermissions(ctx, id, scope) error
AddOrgMember(ctx, orgID, userID, role) error
RemoveOrgMember(ctx, orgID, userID) error
```

Each:

1. Validates input.
2. `WithSerializableRetry` wraps `store.Transact`.
3. Inside Tx: read current state (for `affected_principal_ids[]` and old/new diff payload).
4. Update via writer.
5. Emit outbox row with payload per spec D6.

`RemoveOrgMember` payload includes the removed user UUID in `affected_principal_ids`. `UpdateAgentPermissions` payload includes the agent UUID in `affected_principal_ids`.

### Step 2 — `IdentityWriter` body fills

File: `plane/data/store/postgres/identity_writer.go`

Replace `errNotImplemented` stubs (left from #14) with real impls for: `DisableUser`, `RevokeAgent`, `UpdateAgentPermissions`, `AddOrgMember`, `RemoveOrgMember`.

`DisableUser` sets a `disabled_at` column — schema currently has none. **Schema gap to resolve before this PR**: file a sub-issue to add `disabled_at TIMESTAMPTZ` to `human_users` and `revoked_at TIMESTAMPTZ` + `revoke_reason TEXT` to `agent_identities`. Migration `006_identity_disabled_columns.sql` lands in this PR with the schema additions.

### Step 3 — Migration

File: `plane/data/migrations/006_identity_disabled_columns.sql`

```sql
ALTER TABLE identity.human_users
    ADD COLUMN disabled_at TIMESTAMPTZ,
    ADD COLUMN disable_reason TEXT;

ALTER TABLE identity.agent_identities
    ADD COLUMN revoked_at TIMESTAMPTZ,
    ADD COLUMN revoke_reason TEXT;

CREATE INDEX idx_human_users_disabled_at ON identity.human_users(disabled_at) WHERE disabled_at IS NOT NULL;
CREATE INDEX idx_agent_identities_revoked_at ON identity.agent_identities(revoked_at) WHERE revoked_at IS NOT NULL;

-- updated_at bump trigger
CREATE TRIGGER trg_human_users_updated_at BEFORE UPDATE ON identity.human_users
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

CREATE TRIGGER trg_agent_identities_updated_at BEFORE UPDATE ON identity.agent_identities
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();
```

(Trigger function added if not yet present from the schema-gap follow-up issue.)

### Step 4 — Event schemas

Files in `plane/data/events/identity/`:

- `user.disabled.schema.json`
- `agent.revoked.schema.json`
- `principal.permissions_changed.schema.json`
- `org.member_added.schema.json`
- `org.member_removed.schema.json`

All include `affected_principal_ids: array of uuid`. Required fields per spec D6.

Run `make lint-events`.

### Step 5 — gRPC proto

File: `internal/proto/identity/v1/identity.proto`

Service definition with all `Service` methods exposed as RPCs. Generate Go code:

```bash
buf generate # or protoc, depending on project tooling
```

Output: `internal/proto/identity/v1/identity.pb.go` + `identity_grpc.pb.go`.

### Step 6 — gRPC server

File: `cmd/identity-service/main.go`

```go
func main() {
    cfg := loadConfig()
    pool := openPostgres(cfg.PostgresURL)
    pgStore := postgres.NewStore(pool)
    hasher := identity.NewArgon2idHasher()
    svc := identity.NewPostgresService(pgStore, hasher)

    grpcSrv := grpc.NewServer(/* mTLS via SPIFFE ADR-010 */)
    identityv1.RegisterIdentityServiceServer(grpcSrv, &grpcAdapter{svc: svc})

    // signal handling, listen, serve.
}
```

`grpcAdapter` translates between proto types and `Service` types.

### Step 7 — `appclient.IdentityClient` impl

File: `plane/workflow/appclient/identity_grpc.go`

Implements the interface from #33 by wrapping a generated gRPC client. Workflow activities receive this concrete via DI.

### Step 8 — Integration tests

File: `plane/application/identity/integration_revocation_test.go`

Each method: assert source-row mutation + outbox row in same Tx; assert payload shape matches schema.

File: `cmd/identity-service/integration_test.go`

Boots the gRPC server against testcontainers PG; uses `appclient.NewIdentityGRPC(addr)` to round-trip `DisableUser` end-to-end.

### Step 9 — README

File: `plane/application/identity/README.md`

Documents:
- Service shape.
- ADR-019 routing rule (workflows call this service via gRPC).
- Argon2id parameters.
- Revocation event semantics + `affected_principal_ids[]` contract.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Migrate | apply 006 against testcontainers PG | clean |
| Unit | `go test -race ./plane/application/identity/...` | pass |
| Integration | `go test -tags integration ./plane/application/identity/... ./cmd/identity-service/...` | pass |
| Event lint | `make lint-events` | pass |
| Proto | `buf lint && buf breaking --against '.git#branch=main'` | pass |
| `appclient.IdentityClient` | unit test in `plane/workflow/appclient/...` against in-memory gRPC server | pass |

## Acceptance checklist

- [ ] All revocation methods implemented and emit outbox events with correct payloads.
- [ ] Migration 006 applies cleanly; `disabled_at` / `revoked_at` columns in place.
- [ ] `updated_at` triggers in place on `human_users` + `agent_identities`.
- [ ] All 5 revocation event schemas validate against `lint-events`.
- [ ] gRPC service compiles; mTLS configured via SPIFFE per ADR-010.
- [ ] `cmd/identity-service` boots against docker-compose.
- [ ] `appclient.IdentityClient` gRPC impl round-trips end-to-end.
- [ ] PR description references ADR-019 + #15 spec + new revocation issue.
- [ ] #27 unblocked.

## Risks

| Risk | Mitigation |
|---|---|
| Schema migration 006 conflicts with parallel work | rebase before merge; CI runs migrations on fresh DB |
| `affected_principal_ids[]` payload missing for org events | schema enforces required; integration test asserts content |
| gRPC mTLS not yet wired (SPIRE deployment) | start with insecure local credentials; gate prod deploy on SPIRE rollout — flagged in PR |
| `revoke_reason` text grows unbounded | column is TEXT but limit caller via gRPC field validation (max 1024 chars) |
| Trigger conflict with future `auto-update` libraries | trigger is plain SQL `BEFORE UPDATE`; standard pattern |
| `appclient.IdentityClient` interface from #33 changed | ADR-019 fixes the contract; `IdentityClient` is interface, not concrete — drift caught at compile |

## Rollback

Migration 006 is additive only (new columns + triggers + indexes); rollback via `006_down.sql`. gRPC service can be turned off via deployment flag without breaking #15-postgres.
