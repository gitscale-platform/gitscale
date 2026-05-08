# Spec — Issue #63 docker-compose Temporal dev-server entry + .env.example

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/63
Plane: meta
Priority: p2 (Wave 0)
ADR-impact: none (local-dev convenience)

## Problem

`cmd/workflow-worker` requires a Temporal frontend reachable on
`localhost:7233`. There is no compose service that boots one — every
developer has to bring their own Temporal install. Integration tests in
`plane/workflow/...` fall back to in-process testsuite mode but the worker
binary itself can't be exercised end-to-end on a laptop.

Soft-coordination: issue #75's VaultKeyProvider integration tests boot
Vault via testcontainers, but local-dev runs of `cmd/workflow-worker`
benefit from a long-lived Vault dev container alongside the Temporal one.
Adding both in the same PR is correct: they share the same workflow-of-day
(`make workflow-up`).

## Goals

1. Add `temporal` and `vault` services to `docker-compose.yml`.
2. Update `.env.example` with the new env vars.
3. Add `make` convenience targets: `temporal-up`, `temporal-down`,
   `vault-up`, `vault-down`, `workflow-stack-up` (composite of all three +
   postgres).
4. Verify by running `cmd/workflow-worker` against the stack with default
   env values.

## Non-goals

- Production Temporal cluster topology — the dev compose is single-node
  `temporalio/auto-setup`; staging/prod uses a managed cluster (separate
  effort).
- TLS for the dev Temporal frontend.
- Configuring Vault transit keys in `entrypoint`. The seeding of
  `transit/keys/platform-billing-master` belongs to the worker's bootstrap
  path or a dedicated bootstrap script — not the compose entry.
- Adding observability for the dev Temporal (no OTel collector).

## Architecture

### `docker-compose.yml`

Append two services after the existing `kafka` block:

```yaml
  temporal:
    image: temporalio/auto-setup:1.24
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      DB: postgres12
      DB_PORT: 5432
      POSTGRES_USER: gitscale
      POSTGRES_PWD: gitscale
      POSTGRES_SEEDS: postgres
      DYNAMIC_CONFIG_FILE_PATH: /etc/temporal/config/dynamicconfig/development.yaml
    ports:
      - "7233:7233"
    healthcheck:
      test: ["CMD", "tctl", "--address", "127.0.0.1:7233", "cluster", "health"]
      interval: 10s
      timeout: 5s
      retries: 10

  temporal-ui:
    image: temporalio/ui:2.27.0
    depends_on:
      temporal:
        condition: service_healthy
    environment:
      TEMPORAL_ADDRESS: temporal:7233
      TEMPORAL_CORS_ORIGINS: http://localhost:8080
    ports:
      - "8080:8080"

  vault:
    image: hashicorp/vault:1.16
    cap_add:
      - IPC_LOCK
    environment:
      VAULT_DEV_ROOT_TOKEN_ID: root
      VAULT_DEV_LISTEN_ADDRESS: 0.0.0.0:8200
    ports:
      - "8200:8200"
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://127.0.0.1:8200/v1/sys/health"]
      interval: 5s
      timeout: 5s
      retries: 10
```

Notes:
- `temporalio/auto-setup` runs the schema bootstrap against the existing
  `postgres` service. It picks the user via env vars; the dev `postgres`
  service uses `gitscale/gitscale` (matches existing service env).
- The history/matching ports (7234-7239) are *not* exposed; the worker only
  needs the frontend (7233). Exposing them clutters the host port map.
- Temporal UI lives on `localhost:8080`. If port `8080` clashes with the
  worker's existing healthz binding, use `8233` (the Temporal default).
  Verify against `cmd/workflow-worker` listeners before committing.
- `vault dev` mode mounts `secret/` and `cubbyhole/` automatically. The
  `transit` mount is enabled at first call by either the worker or a dev-
  bootstrap helper — out of scope for this PR.

### `.env.example`

Append:

```
# Temporal
TEMPORAL_HOST=localhost:7233
TEMPORAL_NAMESPACE=gitscale-dev

# Vault
VAULT_ADDR=http://localhost:8200
VAULT_TOKEN=root
```

(`VAULT_TOKEN=root` matches the dev-mode token defined in compose. **Never**
copy that line into a non-dev env file — `.env.example` is a starter
template; the production deploy uses Vault Agent / AppRole.)

### `Makefile`

Append targets after the existing `lint-md` block:

```makefile
.PHONY: temporal-up temporal-down vault-up vault-down workflow-stack-up workflow-stack-down

temporal-up:
	docker compose up -d temporal temporal-ui

temporal-down:
	docker compose stop temporal temporal-ui

vault-up:
	docker compose up -d vault

vault-down:
	docker compose stop vault

workflow-stack-up:
	docker compose up -d postgres redis temporal vault

workflow-stack-down:
	docker compose stop temporal temporal-ui vault
```

The composite `workflow-stack-up` only stops Temporal + Vault on its
matching down; postgres + redis stay running because other services use
them. Developers running just the workflow worker need both running, which
this target covers.

### Smoke verification

Manual (recorded in PR body):

```bash
docker compose up -d postgres temporal temporal-ui vault
sleep 5  # let healthchecks pass
TEMPORAL_NAMESPACE=gitscale-dev TEMPORAL_HOST=localhost:7233 \
    REDIS_ADDR=localhost:6379 \
    go run ./cmd/workflow-worker
```

Worker logs `worker started` within 5s. `curl localhost:8080` returns the
Temporal UI HTML. `vault status -address=http://localhost:8200` reports
`Initialized: true, Sealed: false`.

## Test plan

- No new automated tests (compose changes are inherently integration-only;
  testcontainers cover the actual code paths).
- `docker compose config` lint: `docker compose -f docker-compose.yml config -q`
  must succeed (validates YAML + interpolation).
- Spot-check that `make workflow-stack-up` brings the stack up healthy on
  the maintainer's laptop; capture `docker compose ps` output in PR body.

## Acceptance checklist

- [ ] `temporal`, `temporal-ui`, `vault` services in `docker-compose.yml`
- [ ] Healthchecks defined for each
- [ ] `.env.example` updated with new env vars and a "(dev only)" comment
      next to `VAULT_TOKEN`
- [ ] `Makefile` has the five new targets
- [ ] `docker compose config -q` passes
- [ ] Manual smoke verification logged in PR description

## Open questions

None.

## References

- Existing `docker-compose.yml`
- `cmd/workflow-worker/main.go` env var contract
- `temporalio/auto-setup` docs: https://github.com/temporalio/docker-compose
- HashiCorp Vault dev mode: https://developer.hashicorp.com/vault/docs/concepts/dev-server
