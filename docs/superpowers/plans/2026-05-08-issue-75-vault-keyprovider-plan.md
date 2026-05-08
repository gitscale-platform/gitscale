# Issue #75 VaultKeyProvider — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `stubKeyProvider`'s SHA-256-of-(year,month) DEK derivation with a `VaultKeyProvider` that uses Vault transit `datakey/plaintext` with `derived=true`, and capture the actual Vault key version in the manifest's `kek_hint` field instead of the hard-coded `"platform-billing-v1"`.

**Architecture:** `KeyProvider.GetDEK` returns a `DEK{Bytes []byte; KEKHint string}` struct. `stubKeyProvider` returns `KEKHint="stub-v0"`. `VaultKeyProvider` calls Vault transit `datakey/plaintext/<keyName>` with `context=base64("YYYY-MM")` and produces `KEKHint="platform-billing-v<N>"` where N is the Vault key version. `export_activity.go` reads `dek.KEKHint` for the manifest. Worker wiring is deferred to #76.

**Tech Stack:** Go 1.22, `github.com/hashicorp/vault/api`, testcontainers-go.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-75-vault-keyprovider-design.md`

**Branch:** `feat/workflow-vault-keyprovider` (worktree: `../gitscale.worktrees/feat-workflow-vault-keyprovider`)

---

## File map

### Create
- `plane/workflow/billing/vault_keyprovider.go` — `VaultKeyProvider` + `LoadVaultClientFromEnv`
- `plane/workflow/billing/vault_keyprovider_test.go` — integration-tagged Vault tests
- `plane/workflow/billing/keyprovider_test.go` — unit test for stub (if not already covered) and DEK struct

### Modify
- `plane/workflow/billing/keyprovider.go` — `KeyProvider.GetDEK` returns `DEK`; `stubKeyProvider` updated
- `plane/workflow/billing/export_activity.go` — use `dek.KEKHint`; thread `DEK` instead of `[]byte`
- `plane/workflow/billing/export_activity_test.go` — adapt to new return type
- `go.mod` / `go.sum` — `github.com/hashicorp/vault/api`

### Untouched
- `cmd/workflow-worker/main.go` — wired in #76
- Docker compose — additive Vault entry is #63

---

## Pre-flight

- [ ] **Step P.1: Create worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/workflow-vault-keyprovider \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-vault-keyprovider \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-vault-keyprovider
git status --porcelain
```

Expected: clean.

- [ ] **Step P.2: Verify baseline**

```bash
go build ./...
go vet ./...
go test -race ./plane/workflow/billing/... -count=1
```

Expected: all green.

---

## Task 1: Evolve `KeyProvider` interface to return `DEK`

**Files:**
- Modify: `plane/workflow/billing/keyprovider.go`
- Modify: `plane/workflow/billing/export_activity.go`
- Modify: `plane/workflow/billing/export_activity_test.go`

- [ ] **Step 1.1: Update `keyprovider.go`**

Replace the file with:

```go
package billing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

// DEK is a 32-byte AES-256 data encryption key together with the manifest
// hint required to reconstruct it later. Bytes is sensitive and must not be
// logged. KEKHint is opaque to the caller; restore-side code parses it to
// pick the right derivation path (e.g. Vault key version).
type DEK struct {
	Bytes   []byte
	KEKHint string
}

// KeyProvider derives DEKs for archive files.
// Production wires HashiCorp Vault transit (see VaultKeyProvider, ADR-018);
// the stub uses a deterministic derivation safe for tests and local dev.
type KeyProvider interface {
	GetDEK(ctx context.Context, year, month int) (DEK, error)
}

// stubKeyProvider derives keys deterministically from (year, month).
// Never use in production — keys are predictable.
type stubKeyProvider struct{}

// NewStubKeyProvider returns a deterministic KeyProvider for tests.
func NewStubKeyProvider() KeyProvider { return stubKeyProvider{} }

// stubKEKHint is the constant manifest hint emitted by stubKeyProvider.
const stubKEKHint = "stub-v0"

func (stubKeyProvider) GetDEK(_ context.Context, year, month int) (DEK, error) {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(year))
	binary.BigEndian.PutUint32(buf[4:], uint32(month))
	h.Write(buf[:])
	return DEK{Bytes: h.Sum(nil), KEKHint: stubKEKHint}, nil
}
```

- [ ] **Step 1.2: Compile to find every caller**

```bash
go build ./plane/workflow/billing/...
```

Expected: errors at every call site of the old `GetDEK([]byte, error)` signature.

- [ ] **Step 1.3: Update `export_activity.go`**

Find every `dek, err := a.keys.GetDEK(...)` (or equivalent). The variable
type changes from `[]byte` to `DEK`. Internal use:

- `crypto.NewCipher(dek)` → `crypto.NewCipher(dek.Bytes)`
- Replace the literal `KEKHint: "platform-billing-v1",` in the `archiveManifest` literal (around line 233) with `KEKHint: dek.KEKHint,`.

Also: ensure `dek.Bytes` is overwritten with zeros after use:

```go
defer func() {
    for i := range dek.Bytes {
        dek.Bytes[i] = 0
    }
}()
```

(Place the defer immediately after the `GetDEK` call returns successfully.)

- [ ] **Step 1.4: Update `export_activity_test.go`**

Anywhere the test inspects the manifest, change the expected `KEKHint` from
`"platform-billing-v1"` to `"stub-v0"` (since the tests use
`NewStubKeyProvider()`).

- [ ] **Step 1.5: Run unit tests**

```bash
go test -race ./plane/workflow/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 1.6: Commit**

```bash
git add plane/workflow/billing/keyprovider.go \
        plane/workflow/billing/export_activity.go \
        plane/workflow/billing/export_activity_test.go
git commit -m "$(cat <<'EOF'
refactor(workflow): KeyProvider returns DEK with KEKHint (#75)

Lifts the manifest hint out of a hard-coded constant in export_activity
into the KeyProvider return value, so VaultKeyProvider can record the
active Vault transit key version.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `vault/api` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 2.1: Add the import via a compile-driving stub**

Create `plane/workflow/billing/vault_keyprovider.go` with:

```go
package billing

import (
	vault "github.com/hashicorp/vault/api"
)

var _ = (*vault.Client)(nil)
```

- [ ] **Step 2.2: Resolve the dep**

```bash
go mod tidy
go build ./plane/workflow/billing/...
```

Expected: `go.mod` adds `github.com/hashicorp/vault/api`. Build succeeds.

- [ ] **Step 2.3: Commit the dep**

```bash
git add go.mod go.sum plane/workflow/billing/vault_keyprovider.go
git commit -m "$(cat <<'EOF'
chore(deps): add hashicorp/vault/api for #75 VaultKeyProvider

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Implement `VaultKeyProvider`

**Files:**
- Modify: `plane/workflow/billing/vault_keyprovider.go`

- [ ] **Step 3.1: Replace stub file with full impl**

```go
package billing

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	vault "github.com/hashicorp/vault/api"
)

// VaultKeyProvider derives per-month DEKs by calling Vault transit
// `datakey/plaintext/<keyName>` with `derived=true` and a year-month
// context. The transit key must have been created with `derived=true` and
// `exportable=false`. KEKHint records the active key version so post-rotation
// restore (issue #79) can request the correct version explicitly.
type VaultKeyProvider struct {
	client    *vault.Client
	mountPath string
	keyName   string
}

// Default mount + key for the platform billing archive transit key (ADR-018).
const (
	DefaultVaultTransitMount   = "transit"
	DefaultVaultBillingKeyName = "platform-billing-master"
)

// NewVaultKeyProvider returns a VaultKeyProvider against the given Vault
// client, transit mount, and key name. Empty mountPath defaults to "transit";
// empty keyName defaults to "platform-billing-master".
func NewVaultKeyProvider(client *vault.Client, mountPath, keyName string) *VaultKeyProvider {
	if mountPath == "" {
		mountPath = DefaultVaultTransitMount
	}
	if keyName == "" {
		keyName = DefaultVaultBillingKeyName
	}
	return &VaultKeyProvider{client: client, mountPath: mountPath, keyName: keyName}
}

// GetDEK derives a 32-byte AES-256 DEK for (year, month). The Vault transit
// derivation context is the ASCII string "YYYY-MM" (base64-encoded for
// transport per Vault's API).
func (v *VaultKeyProvider) GetDEK(ctx context.Context, year, month int) (DEK, error) {
	if v.client == nil {
		return DEK{}, errors.New("vault keyprovider: nil client")
	}
	if year < 2026 || year > 2100 {
		return DEK{}, fmt.Errorf("vault keyprovider: year %d out of range", year)
	}
	if month < 1 || month > 12 {
		return DEK{}, fmt.Errorf("vault keyprovider: month %d out of range", month)
	}

	contextStr := fmt.Sprintf("%04d-%02d", year, month)
	contextB64 := base64.StdEncoding.EncodeToString([]byte(contextStr))
	path := fmt.Sprintf("%s/datakey/plaintext/%s", v.mountPath, v.keyName)

	secret, err := v.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"context": contextB64,
		"bits":    256,
	})
	if err != nil {
		return DEK{}, fmt.Errorf("vault keyprovider: write %s: %w", path, err)
	}
	if secret == nil || secret.Data == nil {
		return DEK{}, errors.New("vault keyprovider: empty Vault response")
	}

	plaintextB64, _ := secret.Data["plaintext"].(string)
	if plaintextB64 == "" {
		return DEK{}, errors.New("vault keyprovider: missing plaintext in response")
	}
	plaintext, err := base64.StdEncoding.DecodeString(plaintextB64)
	if err != nil {
		return DEK{}, fmt.Errorf("vault keyprovider: decode plaintext: %w", err)
	}
	if len(plaintext) != 32 {
		return DEK{}, fmt.Errorf("vault keyprovider: expected 32-byte DEK, got %d", len(plaintext))
	}

	keyVersion, err := readIntFromVault(secret.Data, "key_version")
	if err != nil {
		return DEK{}, err
	}

	return DEK{
		Bytes:   plaintext,
		KEKHint: fmt.Sprintf("platform-billing-v%d", keyVersion),
	}, nil
}

// LoadVaultClientFromEnv builds a *vault.Client from VAULT_ADDR + VAULT_TOKEN.
// Production wiring (Vault Agent sidecar, AppRole, etc.) is the caller's
// concern; this helper covers dev + testcontainer environments.
func LoadVaultClientFromEnv() (*vault.Client, error) {
	cfg := vault.DefaultConfig()
	if err := cfg.ReadEnvironment(); err != nil {
		return nil, fmt.Errorf("vault config: %w", err)
	}
	c, err := vault.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	return c, nil
}

func readIntFromVault(m map[string]any, key string) (int, error) {
	raw, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("vault keyprovider: missing %q in response", key)
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case interface{ Int64() (int64, error) }:
		// json.Number
		i, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("vault keyprovider: %q not int: %w", key, err)
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("vault keyprovider: %q has unexpected type %T", key, raw)
	}
}
```

- [ ] **Step 3.2: Build**

```bash
go build ./plane/workflow/billing/...
```

Expected: success.

- [ ] **Step 3.3: Commit (no tests yet — Task 4 adds them)**

```bash
git add plane/workflow/billing/vault_keyprovider.go
git commit -m "$(cat <<'EOF'
feat(workflow): VaultKeyProvider implementing KeyProvider via transit datakey (#75)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Integration test against testcontainers Vault

**Files:**
- Create: `plane/workflow/billing/vault_keyprovider_test.go`

- [ ] **Step 4.1: Write the test**

```go
//go:build integration

package billing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/workflow/billing"
	vault "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func bootVault(t *testing.T) *vault.Client {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image: "hashicorp/vault:1.16",
		Cmd:   []string{"server", "-dev", "-dev-root-token-id=root", "-dev-listen-address=0.0.0.0:8200"},
		ExposedPorts: []string{"8200/tcp"},
		WaitingFor:   wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp").WithStartupTimeout(30 * time.Second),
		Env: map[string]string{
			"VAULT_DEV_ROOT_TOKEN_ID": "root",
		},
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Fatalf("vault container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "8200/tcp")
	addr := fmt.Sprintf("http://%s:%s", host, port.Port())

	cfg := vault.DefaultConfig()
	cfg.Address = addr
	client, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("vault client: %v", err)
	}
	client.SetToken("root")

	if _, err := client.Logical().WriteWithContext(ctx, "sys/mounts/transit", map[string]any{"type": "transit"}); err != nil {
		t.Fatalf("enable transit: %v", err)
	}
	if _, err := client.Logical().WriteWithContext(ctx, "transit/keys/platform-billing-master", map[string]any{
		"derived":    true,
		"exportable": false,
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	return client
}

func TestVaultKeyProvider_DeterministicPerMonth(t *testing.T) {
	ctx := context.Background()
	client := bootVault(t)
	kp := billing.NewVaultKeyProvider(client, "", "")

	a, err := kp.GetDEK(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := kp.GetDEK(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Bytes) != string(b.Bytes) {
		t.Fatalf("expected deterministic DEK for same (year,month)")
	}
	if a.KEKHint != "platform-billing-v1" {
		t.Fatalf("expected KEKHint=platform-billing-v1, got %q", a.KEKHint)
	}
}

func TestVaultKeyProvider_DifferentMonthsDiffer(t *testing.T) {
	ctx := context.Background()
	client := bootVault(t)
	kp := billing.NewVaultKeyProvider(client, "", "")

	a, _ := kp.GetDEK(ctx, 2026, 5)
	b, _ := kp.GetDEK(ctx, 2026, 6)
	if string(a.Bytes) == string(b.Bytes) {
		t.Fatalf("expected different DEKs for different months")
	}
}

func TestVaultKeyProvider_KEKHintReflectsRotation(t *testing.T) {
	ctx := context.Background()
	client := bootVault(t)
	kp := billing.NewVaultKeyProvider(client, "", "")

	pre, _ := kp.GetDEK(ctx, 2026, 5)
	if pre.KEKHint != "platform-billing-v1" {
		t.Fatalf("pre-rotation hint=%q", pre.KEKHint)
	}

	if _, err := client.Logical().WriteWithContext(ctx, "transit/keys/platform-billing-master/rotate", nil); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	post, err := kp.GetDEK(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
	if post.KEKHint != "platform-billing-v2" {
		t.Fatalf("post-rotation hint=%q", post.KEKHint)
	}
}

func TestVaultKeyProvider_NilClient(t *testing.T) {
	kp := billing.NewVaultKeyProvider(nil, "", "")
	if _, err := kp.GetDEK(context.Background(), 2026, 5); err == nil {
		t.Fatal("expected error on nil client")
	}
}
```

- [ ] **Step 4.2: Run integration tests**

```bash
go test -tags integration -race -run TestVaultKeyProvider ./plane/workflow/billing/... -count=1
```

Expected: PASS. (Requires Docker daemon reachable for testcontainers.)

- [ ] **Step 4.3: Commit**

```bash
git add plane/workflow/billing/vault_keyprovider_test.go
git commit -m "$(cat <<'EOF'
test(workflow): integration tests for VaultKeyProvider (#75)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: End-to-end test through `export_activity` with Vault

**Files:**
- Create: `plane/workflow/billing/export_activity_vault_test.go`

- [ ] **Step 5.1: Write the test (build tag `integration`)**

The test boots Vault + an in-memory `ObjectStore` (or testcontainer minio if
already used by sibling tests — pick whichever sibling tests use), substitutes
`VaultKeyProvider`, runs `ExportActivity` against a small synthetic partition,
and asserts:

- The manifest's `kek_hint` matches Vault's reported `platform-billing-v1`.
- After rotation, a fresh export produces `kek_hint=platform-billing-v2`.

(Inspect `plane/workflow/billing/export_activity_test.go` for the existing
test harness — a sibling `setupExportActivity(t)` helper is likely; reuse.)

- [ ] **Step 5.2: Run**

```bash
go test -tags integration -race -run TestExportActivity_Vault ./plane/workflow/billing/... -count=1
```

Expected: PASS.

- [ ] **Step 5.3: Commit**

```bash
git add plane/workflow/billing/export_activity_vault_test.go
git commit -m "$(cat <<'EOF'
test(workflow): export_activity Vault end-to-end (#75)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Final gates + open PR

- [ ] **Step 6.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
go test -tags integration -race ./plane/workflow/billing/... -count=1
```

Expected: all green.

- [ ] **Step 6.2: Mandatory skills (per supervisor §6 for workflow plane)**

Invoke (Skill tool, exact names) and resolve every finding:

- `gitscale-temporal-determinism`
- `gitscale-go-conventions`
- `gitscale-plane-boundary`

Note: `VaultKeyProvider.GetDEK` is called from inside an Activity, not a
Workflow function — so `time`, `randomness`, network are all permitted. The
determinism check still applies to anything in `plane/workflow/billing/` that
the workflow imports; verify the call site is activity-only.

- [ ] **Step 6.3: Self-review battery (parallel)**

Dispatch in one message:

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter` (Vault errors must propagate, not swallow)
- `pr-review-toolkit:type-design-analyzer` (new public types: `DEK`, `VaultKeyProvider`)
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` — confirm ADR-018 conformance

- [ ] **Step 6.4: Push + open PR**

```bash
git push -u origin feat/workflow-vault-keyprovider
gh pr create --title "[Workflow] KeyProvider Vault HKDF wiring for billing archive DEK derivation" --body "$(cat <<'EOF'
## Summary

- `KeyProvider.GetDEK` returns `DEK{Bytes, KEKHint}`; manifest reads the
  hint from the provider instead of a hard-coded constant.
- New `VaultKeyProvider` calls Vault transit `datakey/plaintext/<key>` with
  `derived=true` and a `YYYY-MM` context; `KEKHint=platform-billing-v<N>`
  records the active key version for post-rotation restore.
- `stubKeyProvider` keeps backing unit tests with `KEKHint=stub-v0`.
- Worker wiring lives in #76; this PR ships the type, helpers, and
  testcontainers Vault integration tests.

## ADR-impact

conforming. Implements ADR-018 §Encryption "HKDF(platform_billing_master,
'year-month')" via Vault's transit derived-datakey primitive.

## Test plan

- [x] `go test -race ./plane/workflow/billing/...`
- [x] `go test -tags integration -race ./plane/workflow/billing/...` (testcontainers Vault)
- [x] Determinism: same (year,month) → same DEK
- [x] Rotation: post-rotate `kek_hint` becomes v2
- [x] Negative: nil client returns error

Spec: docs/superpowers/specs/2026-05-08-issue-75-vault-keyprovider-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-75-vault-keyprovider-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #75.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 6.5: Watch CI**

```bash
gh pr checks <number> --watch
```

---

## Self-review (plan author)

**Spec coverage:**
- DEK struct + interface change — Task 1.
- Vault dependency — Task 2.
- VaultKeyProvider impl — Task 3.
- Testcontainers Vault tests (determinism, rotation, error path) — Task 4.
- End-to-end via export_activity — Task 5.
- ADR-018 reference in PR body — Task 6.

**Placeholder scan:** Step 5.1 directs the implementer to inspect a sibling
test for the harness rather than spelling it out. Acceptable: the harness is
local context the implementer has in their worktree, and reproducing it
verbatim risks drift if it has changed since this plan was written.

**Type consistency:**
- `DEK` defined in Task 1, consumed in Tasks 3, 4, 5.
- `VaultKeyProvider` constructor signature stable across plan.
- `KEKHint` literal `"platform-billing-v<N>"` used consistently.
