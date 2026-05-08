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
		Image:        "hashicorp/vault:1.16",
		Cmd:          []string{"server", "-dev", "-dev-root-token-id=root", "-dev-listen-address=0.0.0.0:8200"},
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
	// HMAC-based derivation does not require derived=true; we still set
	// exportable=false to mirror production policy.
	if _, err := client.Logical().WriteWithContext(ctx, "transit/keys/platform-billing-master", map[string]any{
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

	a, err := kp.GetDEK(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := kp.GetDEK(ctx, 2026, 6)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Bytes) == string(b.Bytes) {
		t.Fatalf("expected different DEKs for different months")
	}
}

func TestVaultKeyProvider_KEKHintReflectsRotation(t *testing.T) {
	ctx := context.Background()
	client := bootVault(t)
	kp := billing.NewVaultKeyProvider(client, "", "")

	pre, err := kp.GetDEK(ctx, 2026, 5)
	if err != nil {
		t.Fatal(err)
	}
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
