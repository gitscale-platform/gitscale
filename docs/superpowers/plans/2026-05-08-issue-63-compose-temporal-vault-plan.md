# Issue #63 docker-compose Temporal+Vault — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add `temporal`, `temporal-ui`, `vault` services to `docker-compose.yml`; update `.env.example`; add Makefile targets.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-63-compose-temporal-vault-design.md`

**Branch:** `chore/meta-compose-temporal-vault`

---

## File map

### Modify
- `docker-compose.yml`
- `.env.example`
- `Makefile`

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b chore/meta-compose-temporal-vault \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/chore-meta-compose-temporal-vault \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/chore-meta-compose-temporal-vault
git status --porcelain
```

- [ ] **Step P.2: Verify Docker reachable**

```bash
docker info >/dev/null
docker compose version
```

Expected: both succeed.

---

## Task 1: Append services to `docker-compose.yml`

**File:** `docker-compose.yml`

- [ ] **Step 1.1: Append the three services**

Add after the `kafka:` block (before `volumes:`):

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

- [ ] **Step 1.2: Validate YAML**

```bash
docker compose -f docker-compose.yml config -q
```

Expected: zero output (success).

---

## Task 2: Update `.env.example`

**File:** `.env.example`

- [ ] **Step 2.1: Append the new env vars**

Append at the bottom of the file:

```
# Temporal
TEMPORAL_HOST=localhost:7233
TEMPORAL_NAMESPACE=gitscale-dev

# Vault (dev only — production uses Vault Agent / AppRole, never a static root token)
VAULT_ADDR=http://localhost:8200
VAULT_TOKEN=root
```

---

## Task 3: Makefile targets

**File:** `Makefile`

- [ ] **Step 3.1: Append targets**

Inspect existing `.PHONY` declarations to follow the convention (one target list per `.PHONY`).

Append:

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

- [ ] **Step 3.2: Sanity-check**

```bash
make -n temporal-up
make -n workflow-stack-up
```

Expected: prints the docker compose commands without running them.

---

## Task 4: Smoke verification (capture for PR body)

- [ ] **Step 4.1: Boot the stack**

```bash
docker compose up -d postgres redis temporal temporal-ui vault
docker compose ps
```

Wait until `healthy` for postgres + temporal + vault.

- [ ] **Step 4.2: Probe**

```bash
curl -fs http://localhost:8080 | head -1   # Temporal UI HTML
curl -fs http://localhost:8200/v1/sys/health | head -1   # Vault sealed=false
```

- [ ] **Step 4.3: Boot worker**

```bash
TEMPORAL_NAMESPACE=gitscale-dev TEMPORAL_HOST=localhost:7233 \
    REDIS_ADDR=localhost:6379 POSTGRES_DSN="postgres://gitscale:gitscale@localhost:5432/gitscale?sslmode=disable" \
    go run ./cmd/workflow-worker &
sleep 5 && kill %1 || true
```

Capture the worker startup logs for the PR body — the line confirming
`worker started` is the success criterion.

- [ ] **Step 4.4: Tear down**

```bash
docker compose stop temporal temporal-ui vault
```

---

## Task 5: Final gates + open PR

- [ ] **Step 5.1: Lint + checks**

```bash
docker compose -f docker-compose.yml config -q
make lint-md
```

- [ ] **Step 5.2: Self-review battery**

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter` (e.g. healthcheck commands must
  fail loudly when the service is broken)
- (No type-design-analyzer / no Go types here)
- `pr-review-toolkit:pr-test-analyzer` — confirm there are no untested code paths added

- [ ] **Step 5.3: Commit**

```bash
git add docker-compose.yml .env.example Makefile
git commit -m "$(cat <<'EOF'
chore(meta): docker-compose Temporal + Vault dev-server entries (#63)

Adds temporal (auto-setup 1.24), temporal-ui (2.27.0), and vault (dev mode)
services to the local stack. New Makefile targets bring them up/down. New
.env.example entries make the connection details discoverable.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 5.4: Push + open PR**

```bash
git push -u origin chore/meta-compose-temporal-vault
gh pr create --title "[Meta] docker-compose Temporal dev-server entry + .env.example" --body "$(cat <<'EOF'
## Summary

- Adds `temporal` (`temporalio/auto-setup:1.24`), `temporal-ui`, and `vault`
  (dev mode) services to `docker-compose.yml`.
- `.env.example` gains `TEMPORAL_HOST`, `TEMPORAL_NAMESPACE`, `VAULT_ADDR`,
  `VAULT_TOKEN` (dev only).
- New Makefile targets: `temporal-up`/`-down`, `vault-up`/`-down`,
  `workflow-stack-up`/`-down`.

## ADR-impact

none. Local-dev convenience.

## Test plan

- [x] `docker compose -f docker-compose.yml config -q` passes
- [x] `docker compose up -d postgres temporal temporal-ui vault` reaches healthy
- [x] `cmd/workflow-worker` starts against the stack (logs captured)
- [x] `curl http://localhost:8080` returns Temporal UI

Spec: docs/superpowers/specs/2026-05-08-issue-63-compose-temporal-vault-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-63-compose-temporal-vault-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- pr-test-analyzer: <result>

</details>

Closes #63.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review (plan author)

**Spec coverage:** all four spec acceptance items map to Tasks 1, 2, 3, 4.

**Placeholder scan:** none.

**Type consistency:** N/A (no Go types).
