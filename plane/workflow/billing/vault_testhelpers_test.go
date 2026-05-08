//go:build integration

package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// bootVaultForExport boots a Vault dev container and configures the transit
// engine + platform-billing-master key. Shared by integration tests that live
// in the internal `billing` package (e.g. export_activity_vault_test.go).
// The external-package equivalent (bootVault in vault_keyprovider_test.go) is
// duplicated rather than exported to keep the test surface minimal.
func bootVaultForExport(t *testing.T) *vault.Client {
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
	if _, err := client.Logical().WriteWithContext(ctx, "transit/keys/platform-billing-master", map[string]any{
		"exportable": false,
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	return client
}
