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
//
// VaultKeyProvider.GetDEK is invoked from inside a Temporal Activity
// (ExportActivity.Execute), never from a workflow function — network calls
// and non-determinism are permitted in that scope.
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
