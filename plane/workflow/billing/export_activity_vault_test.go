//go:build integration

package billing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	vault "github.com/hashicorp/vault/api"
)

// bootVaultInternal is a copy of bootVault from vault_keyprovider_test.go but
// callable from the internal-package test file. The duplication is preferred
// over exporting a test helper from the external test package.
func bootVaultInternal(t *testing.T) *vault.Client {
	t.Helper()
	return bootVaultForExport(t)
}

func TestExportActivity_VaultManifestRecordsKeyVersion(t *testing.T) {
	ctx := context.Background()
	client := bootVaultInternal(t)

	archiver := billingstore.NewStubArchiver()
	store := newStubObjectStore("test-bucket")
	keys := NewVaultKeyProvider(client, "", "")

	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	archiver.SetRows(2026, 5, []billingstore.UsageEventRow{
		billingstore.SeedUsageEventRow("id-1", "acc-1", ts),
	})

	act, err := NewExportActivity(archiver, store, keys, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := act.Execute(ctx, ExportInput{Year: 2026, Month: 5}); err != nil {
		t.Fatalf("Execute pre-rotation: %v", err)
	}

	manifestKey := "billing/usage_events/year=2026/month=05/usage_events_2026_05.manifest.json"
	preBytes := store.Get(manifestKey)
	if preBytes == nil {
		t.Fatal("manifest not uploaded pre-rotation")
	}
	var pre archiveManifest
	if err := json.Unmarshal(preBytes, &pre); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if pre.KEKHint != "platform-billing-v1" {
		t.Fatalf("pre-rotation KEKHint=%q want platform-billing-v1", pre.KEKHint)
	}

	// Rotate the transit key; a fresh export must record v2.
	if _, err := client.Logical().WriteWithContext(ctx, "transit/keys/platform-billing-master/rotate", nil); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Use a different month to dodge stub-archiver state and force a new export.
	archiver.SetRows(2026, 6, []billingstore.UsageEventRow{
		billingstore.SeedUsageEventRow("id-2", "acc-1", ts),
	})
	if _, err := act.Execute(ctx, ExportInput{Year: 2026, Month: 6}); err != nil {
		t.Fatalf("Execute post-rotation: %v", err)
	}

	postKey := "billing/usage_events/year=2026/month=06/usage_events_2026_06.manifest.json"
	postBytes := store.Get(postKey)
	if postBytes == nil {
		t.Fatal("manifest not uploaded post-rotation")
	}
	var post archiveManifest
	if err := json.Unmarshal(postBytes, &post); err != nil {
		t.Fatalf("post manifest JSON: %v", err)
	}
	if post.KEKHint != "platform-billing-v2" {
		t.Fatalf("post-rotation KEKHint=%q want platform-billing-v2", post.KEKHint)
	}
	if !strings.HasPrefix(post.SourcePartition, "billing.usage_events_2026_06") {
		t.Errorf("SourcePartition=%s", post.SourcePartition)
	}
}
