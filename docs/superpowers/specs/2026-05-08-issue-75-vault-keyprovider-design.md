# Spec — Issue #75 VaultKeyProvider for billing archive DEK derivation

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/75
Plane: workflow
Priority: p1 (Wave 0; soft-coordinated with #63 docker-compose Vault entry)
ADR-impact: conforming (ADR-018 §Encryption)

## Problem

`plane/workflow/billing/keyprovider.go::stubKeyProvider` derives the per-month
DEK as `SHA-256(year || month)` — public, predictable, and unfit for any real
data. ADR-018 specifies HashiCorp Vault transit HKDF with the platform billing
master KEK; the manifest's `kek_hint` (currently the hard-coded
`"platform-billing-v1"`) must record the Vault transit key version actually
used so a future `RestorePartition` workflow can decrypt post-rotation.

## Goals

1. `VaultKeyProvider` that derives DEKs from a Vault transit key, with the
   key version captured for the manifest.
2. Manifests record the actual key version used (not a constant).
3. Local-dev Vault stays opt-in: a testcontainers Vault devmode entry is
   sufficient for #75; the docker-compose Vault service is the property of #63
   and is additive.
4. Existing `stubKeyProvider` continues to back unit tests; the `KeyProvider`
   interface evolves only as far as needed to surface the version.

## Non-goals

- Wiring the real `VaultKeyProvider` into `cmd/workflow-worker` — that is #76
  (Wave 1). #75 ships the type, its tests, and ergonomic constructors; #76
  picks them up.
- Per-month DEK destruction / crypto-shred (issue #80, Wave 2).
- RestorePartition (issue #79, Wave 2).
- docker-compose Vault entry (issue #63 — independent Wave 0 item).
- Any change to the AES-256-GCM frame format or manifest schema beyond making
  `kek_hint` dynamic.

## Architecture

### Interface change (minimal)

`KeyProvider.GetDEK` returns a richer value so the activity can read the key
version without a second call:

```go
// DEK is the per-month data encryption key plus the metadata needed to
// reconstruct it later. Bytes is 32 bytes (AES-256). KEKHint is the value to
// embed in the archive manifest; for VaultKeyProvider it has the form
// "platform-billing-v<N>" where N is the Vault transit key version active at
// derivation time.
type DEK struct {
    Bytes   []byte
    KEKHint string
}

type KeyProvider interface {
    GetDEK(ctx context.Context, year, month int) (DEK, error)
}
```

`stubKeyProvider` returns `DEK{Bytes: sha256(year||month), KEKHint: "stub-v0"}`
so existing tests keep working with a one-line change.

`export_activity.go` uses `dek.KEKHint` instead of the hard-coded
`"platform-billing-v1"`.

### VaultKeyProvider

```go
type VaultKeyProvider struct {
    client    *vault.Client
    mountPath string // default "transit"
    keyName   string // default "platform-billing-master"
}

func NewVaultKeyProvider(client *vault.Client, mountPath, keyName string) *VaultKeyProvider
```

`GetDEK(ctx, year, month)`:

1. `context := fmt.Sprintf("%04d-%02d", year, month)` — ASCII, base64-encoded
   per Vault transit derive-key requirements.
2. Call `transit/datakey/plaintext/<keyName>` with
   `{"context": base64(context), "bits": 256}`. Vault returns `plaintext`
   (base64 32-byte DEK) and `key_version` (int).
3. Return `DEK{Bytes: rawPlaintext, KEKHint: fmt.Sprintf("platform-billing-v%d", keyVersion)}`.

Why `datakey/plaintext` over Vault's HKDF endpoint:

- `datakey` with `derived=true` is exactly Vault's HKDF-on-transit primitive
  for "give me a deterministic-per-context DEK".
- `plaintext` variant returns the DEK to the caller (we encrypt the file
  client-side, then can discard the DEK in memory).
- ADR-018 §Encryption prescribes "HKDF(platform_billing_master,
  'year-month')"; Vault's `derived=true datakey` is the implementation of
  that primitive.

Vault transit key must be created with `derived=true` and
`exportable=false`. See `Local dev` below.

### `kek_hint` semantics

| Value form | When |
|---|---|
| `stub-v0` | StubKeyProvider (tests, dev) |
| `platform-billing-v<N>` | VaultKeyProvider; N = Vault key version at encryption time |

Restore (issue #79) parses the suffix: `stub-v0` → stub provider; `v<N>` →
Vault, request `key_version=N` from `transit/datakey/plaintext/...` (rewrap path).

### Constructor wiring

`NewVaultKeyProvider` takes the client; the caller owns Vault auth (auth
methods, token renewal). The constructor does NOT do auth. This matches the
project's pattern of receiving infrastructure handles, not building them.

A small package-level helper `LoadVaultClientFromEnv() (*vault.Client, error)`
parses `VAULT_ADDR` + `VAULT_TOKEN` (dev) or `VAULT_AGENT_ADDR` (prod) and
returns a configured `*vault.Client`. Used by `cmd/workflow-worker` in #76.

### Vault dependency

Add `github.com/hashicorp/vault/api` (single import). No agent sidecar at
this revision — the worker holds a long-lived `*vault.Client` with token
renewal handled by the SDK's `LifetimeWatcher`. Vault Agent sidecar is a
deployment concern we will revisit; not in #75 scope.

### Local dev (testcontainers)

Tests boot Vault in dev mode via testcontainers-go:

```
testcontainers.GenericContainer{
  Image: "hashicorp/vault:1.16",
  Cmd:   []string{"server", "-dev", "-dev-root-token-id=root"},
  Env:   {VAULT_DEV_LISTEN_ADDRESS: "0.0.0.0:8200"},
  Wait:  HTTP /v1/sys/health,
}
```

Test setup:

```
vault secrets enable transit
vault write -f transit/keys/platform-billing-master derived=true exportable=false
```

Done as Vault API calls, not shell — the test owns the container.

## Test plan

| Layer | Test |
|---|---|
| Unit | `stubKeyProvider` returns DEK with `KEKHint="stub-v0"` |
| Integration (testcontainers Vault) | Same `(year,month)` produces same `Bytes` across calls (HKDF determinism); different `(year,month)` produces different `Bytes` |
| Integration (rotation) | Issue `vault write -f transit/keys/platform-billing-master/rotate`; new call returns DEK with `KEKHint="platform-billing-v2"` |
| Integration with export_activity | Boot Vault, set `VaultKeyProvider`, run export; manifest's `kek_hint` reflects active version |
| Negative | Vault unreachable → error wraps `*api.ResponseError` with status code; activity caller surfaces retryable failure |

Integration tests gated by `//go:build integration` to keep `go test ./...`
fast.

## Acceptance checklist

- [ ] `KeyProvider.GetDEK` returns `DEK` struct with `Bytes` + `KEKHint`
- [ ] `VaultKeyProvider` implemented and unit-tested
- [ ] `export_activity.go` uses `dek.KEKHint` (no hard-coded `"platform-billing-v1"`)
- [ ] Integration test against testcontainers Vault asserts post-rotation
      `kek_hint` reflects new version
- [ ] PR description references ADR-018
- [ ] Worker wiring in `cmd/workflow-worker` deferred to #76

## Open questions

None — resolved by design defaults; the spec records them explicitly.

## References

- ADR-018 §Encryption — `docs/architecture.md` line ~663
- Spike: `docs/superpowers/specs/2026-05-06-issue-34-billing-archival-tier-spike.md` §Q7
- Existing stub: `plane/workflow/billing/keyprovider.go`
- Existing manifest hard-coded value: `plane/workflow/billing/export_activity.go:233`
- Vault transit datakey: https://developer.hashicorp.com/vault/api-docs/secret/transit#generate-data-key
