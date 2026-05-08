package billing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	vault "github.com/hashicorp/vault/api"
)

// ActivityNameDestroyDEK is the registered name for the
// DestroyDEKActivity.Execute method.
const ActivityNameDestroyDEK = "billing.DestroyDEK"

// DestroyDEKInput identifies the per-month DEK to destroy. KEKHint is the
// "platform-billing-v<N>" string captured at archive time. Year/Month are
// passed for log correlation and idempotency-key derivation.
type DestroyDEKInput struct {
	Year    int
	Month   int
	KEKHint string
}

// DestroyDEKResult records the destroyed Vault transit key version. The
// workflow propagates this into the EmitDEKDestroyed activity input so the
// outbox payload pins the destroyed version.
type DestroyDEKResult struct {
	VaultKeyVersion int
}

// vaultLogicalClient is the subset of vault.Client used by DestroyDEKActivity.
// Defined as an interface so unit tests can substitute a fake without
// booting a Vault testcontainer.
type vaultLogicalClient interface {
	ReadWithContext(ctx context.Context, path string) (*vault.Secret, error)
	WriteWithContext(ctx context.Context, path string, data map[string]any) (*vault.Secret, error)
}

// DestroyDEKActivity destroys a specific historical version of a Vault
// transit key by raising the key's min_decryption_version above the target
// version, then trimming. Vault transit does not expose a per-version
// destroy endpoint (only per-key DELETE and bulk trim); this activity uses
// the documented trim path to achieve the per-version crypto-shred ADR-018
// §Encryption requires.
//
// Idempotency: the activity tolerates pre-trimmed versions. Reading the key
// metadata first; if the target version is already absent (because
// min_available_version > target), Execute returns nil. This makes Temporal
// activity retry safe even after an irreversible side effect succeeded.
//
// Pre-conditions on the Vault key (configured at provisioning time, not
// here):
//   - deletion_allowed=true (allow trim)
//   - min_decryption_version is monotonically increased per-month destroy,
//     which aligns with the workflow's deterministic ascending iteration.
//
// Per-month DEKs are destroyed in ascending (year, month) order, which
// keeps the trim semantics simple: the workflow trims to N+1 only after
// all versions ≤N have already been logically retired by previous runs.
type DestroyDEKActivity struct {
	logical vaultLogicalClient
	mount   string
	keyName string
}

// NewDestroyDEKActivity returns a DestroyDEKActivity backed by client.
// Empty mount or keyName default to the same constants used by the export
// path (DefaultVaultTransitMount, DefaultVaultBillingKeyName).
func NewDestroyDEKActivity(client *vault.Client, mount, keyName string) (*DestroyDEKActivity, error) {
	if client == nil {
		return nil, errors.New("billing.NewDestroyDEKActivity: client is nil")
	}
	if mount == "" {
		mount = DefaultVaultTransitMount
	}
	if keyName == "" {
		keyName = DefaultVaultBillingKeyName
	}
	return &DestroyDEKActivity{logical: client.Logical(), mount: mount, keyName: keyName}, nil
}

// newDestroyDEKActivityWithLogical is a test seam — production callers use
// NewDestroyDEKActivity. The vaultLogicalClient interface keeps unit tests
// off the Vault testcontainer.
func newDestroyDEKActivityWithLogical(logical vaultLogicalClient, mount, keyName string) *DestroyDEKActivity {
	if mount == "" {
		mount = DefaultVaultTransitMount
	}
	if keyName == "" {
		keyName = DefaultVaultBillingKeyName
	}
	return &DestroyDEKActivity{logical: logical, mount: mount, keyName: keyName}
}

// Execute crypto-shreds the Vault transit key version embedded in
// in.KEKHint. Idempotent: if the target version is already trimmed (Vault
// reports min_available_version > target or the key 404s on read), Execute
// returns the parsed version with nil error so the workflow can proceed to
// emit the audit event and not block on Temporal's retry loop.
func (a *DestroyDEKActivity) Execute(ctx context.Context, in DestroyDEKInput) (DestroyDEKResult, error) {
	version, err := parseKEKHintVersion(in.KEKHint)
	if err != nil {
		return DestroyDEKResult{}, err
	}

	keyPath := fmt.Sprintf("%s/keys/%s", a.mount, a.keyName)
	configPath := fmt.Sprintf("%s/config", keyPath)
	trimPath := fmt.Sprintf("%s/trim", keyPath)

	// Idempotency probe: read the key metadata. A 404 (nil secret) means the
	// key itself has been deleted, treated as already-destroyed.
	secret, err := a.logical.ReadWithContext(ctx, keyPath)
	if err != nil {
		return DestroyDEKResult{}, fmt.Errorf("destroy dek: read %s: %w", keyPath, err)
	}
	if secret == nil || secret.Data == nil {
		// 404 → already shredded. Idempotent success.
		return DestroyDEKResult{VaultKeyVersion: version}, nil
	}
	minAvail := readIntFromSecret(secret.Data, "min_available_version")
	if minAvail > version {
		// Already trimmed past the target version.
		return DestroyDEKResult{VaultKeyVersion: version}, nil
	}

	// Step 1: raise min_decryption_version AND min_encryption_version above
	// the target. Vault enforces min_available_version <= min(
	// min_encryption_version, min_decryption_version); trim returns 400
	// "minimum available version cannot be set when minimum encryption
	// version is not set" if min_encryption_version is left at zero. Bumping
	// both to version+1 is safe: the workflow iterates ascending (year,
	// month) so older DEK versions are always destroyed before newer ones,
	// and live encryption operations transparently use the latest key
	// version regardless.
	if _, err := a.logical.WriteWithContext(ctx, configPath, map[string]any{
		"min_decryption_version": version + 1,
		"min_encryption_version": version + 1,
	}); err != nil {
		return DestroyDEKResult{}, fmt.Errorf("destroy dek: config %s: %w", configPath, err)
	}

	// Step 2: trim. min_available_version=version+1 permanently deletes
	// every version <= version. IRREVERSIBLE.
	if _, err := a.logical.WriteWithContext(ctx, trimPath, map[string]any{
		"min_available_version": version + 1,
	}); err != nil {
		return DestroyDEKResult{}, fmt.Errorf("destroy dek: trim %s: %w", trimPath, err)
	}

	return DestroyDEKResult{VaultKeyVersion: version}, nil
}

// parseKEKHintVersion extracts N from "platform-billing-v<N>" hints.
// Mirrors VaultKeyProvider's emit format (vault_keyprovider.go).
func parseKEKHintVersion(hint string) (int, error) {
	if hint == "" {
		return 0, errors.New("destroy dek: empty kek_hint")
	}
	idx := strings.LastIndex(hint, "-v")
	if idx < 0 {
		return 0, fmt.Errorf("destroy dek: malformed kek_hint %q", hint)
	}
	tail := hint[idx+2:]
	v, err := strconv.Atoi(tail)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("destroy dek: bad version in kek_hint %q: %w", hint, err)
	}
	return v, nil
}

// readIntFromSecret coerces a Vault response field to an int. Vault SDK
// returns numeric fields as json.Number or float64 depending on transport;
// both are handled here.
func readIntFromSecret(data map[string]any, key string) int {
	v, ok := data[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		// json.Number Stringer fallback
		if s, ok := v.(fmt.Stringer); ok {
			n, _ := strconv.Atoi(s.String())
			return n
		}
	}
	return 0
}
