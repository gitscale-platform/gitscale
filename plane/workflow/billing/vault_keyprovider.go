package billing

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	vault "github.com/hashicorp/vault/api"
)

// VaultKeyProvider derives per-month DEKs from a Vault transit key by
// computing HMAC-SHA256(transit_key, "YYYY-MM"). The HMAC is deterministic for
// a given (key version, input) pair, which is exactly the HKDF-style
// determinism ADR-018 §Encryption prescribes ("HKDF(platform_billing_master,
// 'year-month')"). Vault returns the HMAC prefixed with a version tag of the
// form "vault:v<N>:<base64>"; we record N in KEKHint so post-rotation restore
// (issue #79) can pin the same key version.
//
// Rationale for HMAC (vs transit/datakey/plaintext): datakey returns a fresh
// random key on every call, defeating the deterministic per-month derivation
// that ADR-018 requires. transit/hmac is Vault's exposed PRF over the
// transit key — same key, same input, same output, with key_version embedded
// in the response prefix.
//
// VaultKeyProvider.GetDEK is invoked from inside a Temporal Activity
// (ExportActivity.Execute), never from a workflow function — network calls
// and non-determinism in the transport layer are permitted in that scope.
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

// GetDEK derives a 32-byte AES-256 DEK for (year, month) deterministically by
// asking Vault to HMAC-SHA256 the ASCII string "YYYY-MM" with the transit key.
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

	input := fmt.Sprintf("%04d-%02d", year, month)
	inputB64 := base64.StdEncoding.EncodeToString([]byte(input))
	path := fmt.Sprintf("%s/hmac/%s/sha2-256", v.mountPath, v.keyName)

	secret, err := v.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"input": inputB64,
	})
	if err != nil {
		return DEK{}, fmt.Errorf("vault keyprovider: write %s: %w", path, err)
	}
	if secret == nil || secret.Data == nil {
		return DEK{}, errors.New("vault keyprovider: empty Vault response")
	}

	hmacStr, _ := secret.Data["hmac"].(string)
	if hmacStr == "" {
		return DEK{}, errors.New("vault keyprovider: missing hmac in response")
	}

	keyVersion, dekBytes, err := parseVaultHMAC(hmacStr)
	if err != nil {
		return DEK{}, err
	}
	if len(dekBytes) != 32 {
		return DEK{}, fmt.Errorf("vault keyprovider: expected 32-byte HMAC-SHA256, got %d", len(dekBytes))
	}

	return DEK{
		Bytes:   dekBytes,
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

// parseVaultHMAC parses Vault's transit HMAC response of the form
// "vault:v<N>:<base64>" into (keyVersion, rawDigest).
func parseVaultHMAC(s string) (int, []byte, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		return 0, nil, fmt.Errorf("vault keyprovider: malformed hmac %q", s)
	}
	v, err := strconv.Atoi(strings.TrimPrefix(parts[1], "v"))
	if err != nil {
		return 0, nil, fmt.Errorf("vault keyprovider: bad key version in %q: %w", s, err)
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return 0, nil, fmt.Errorf("vault keyprovider: bad base64 in hmac %q: %w", s, err)
	}
	return v, raw, nil
}
