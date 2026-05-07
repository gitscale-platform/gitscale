package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

func TestExportActivity_uploadsParquetAndWritesSidecarFiles(t *testing.T) {
	archiver := billingstore.NewStubArchiver()
	store := newStubObjectStore("test-bucket")
	keys := NewStubKeyProvider()

	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	archiver.SetRows(2026, 5, []billingstore.UsageEventRow{
		billingstore.SeedUsageEventRow("id-1", "acc-1", ts),
		billingstore.SeedUsageEventRow("id-2", "acc-1", ts),
	})

	act, err := NewExportActivity(archiver, store, keys, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}

	result, err := act.Execute(context.Background(), ExportInput{Year: 2026, Month: 5})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.RowCount != 2 {
		t.Errorf("RowCount=%d want 2", result.RowCount)
	}
	if result.BytesWritten == 0 {
		t.Error("BytesWritten=0, expected >0")
	}
	if result.LakeURI == "" {
		t.Error("LakeURI empty")
	}
	if !strings.Contains(result.LakeURI, "year=2026/month=05") {
		t.Errorf("LakeURI=%s missing Hive prefix", result.LakeURI)
	}

	parquetKey := "billing/usage_events/year=2026/month=05/usage_events_2026_05.parquet"
	if store.Get(parquetKey) == nil {
		t.Error("parquet file not uploaded")
	}

	manifestKey := fmt.Sprintf("%s.manifest.json", strings.TrimSuffix(parquetKey, ".parquet"))
	manifestBytes := store.Get(manifestKey)
	if manifestBytes == nil {
		t.Fatal("manifest not uploaded")
	}
	var manifest archiveManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if manifest.RowCount != 2 {
		t.Errorf("manifest.RowCount=%d want 2", manifest.RowCount)
	}
	if manifest.SchemaVersion != 1 {
		t.Errorf("manifest.SchemaVersion=%d want 1", manifest.SchemaVersion)
	}
	if manifest.SourcePartition != "billing.usage_events_2026_05" {
		t.Errorf("manifest.SourcePartition=%s", manifest.SourcePartition)
	}
	if manifest.EncFormat != "aes-256-gcm-v1-4mib" {
		t.Errorf("manifest.EncFormat=%q want %q", manifest.EncFormat, "aes-256-gcm-v1-4mib")
	}

	checksumKey := fmt.Sprintf("%s.checksum.sha256", strings.TrimSuffix(parquetKey, ".parquet"))
	if store.Get(checksumKey) == nil {
		t.Error("checksum file not uploaded")
	}
}

// TestExportActivity_uploadFailure_doesNotLeak verifies that when the
// ObjectStore returns an upload error mid-stream, the activity (a) returns the
// wrapped upload error, (b) drains all pipeline goroutines (no deadlock), and
// (c) closes the row cursor (no DB connection leak).
func TestExportActivity_uploadFailure_doesNotLeak(t *testing.T) {
	archiver := billingstore.NewStubArchiver()
	store := newStubObjectStore("test-bucket")
	keys := NewStubKeyProvider()

	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	archiver.SetRows(2026, 5, []billingstore.UsageEventRow{
		billingstore.SeedUsageEventRow("id-1", "acc-1", ts),
		billingstore.SeedUsageEventRow("id-2", "acc-1", ts),
	})

	uploadBoom := errors.New("upload boom")
	store.SetUploadFn(func(_ string) error { return uploadBoom })

	act, err := NewExportActivity(archiver, store, keys, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct {
		res ExportResult
		err error
	}, 1)
	go func() {
		res, err := act.Execute(context.Background(), ExportInput{Year: 2026, Month: 5})
		done <- struct {
			res ExportResult
			err error
		}{res, err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("expected upload error, got nil")
		}
		if !errors.Is(got.err, uploadBoom) {
			t.Errorf("err=%v, want wrap of %v", got.err, uploadBoom)
		}
		if !strings.Contains(got.err.Error(), "export: upload:") {
			t.Errorf("err=%v missing 'export: upload:' wrap", got.err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Execute deadlocked: did not return within 1s on upload failure")
	}

	if got := archiver.LastCursorCloses(); got != 1 {
		t.Errorf("cursor close count=%d want 1 (cursor leak)", got)
	}
}

func TestNewExportActivity_nilDepsRejected(t *testing.T) {
	store := newStubObjectStore("b")
	keys := NewStubKeyProvider()
	archiver := billingstore.NewStubArchiver()

	if _, err := NewExportActivity(nil, store, keys, "b"); err == nil {
		t.Error("nil archiver: expected error")
	}
	if _, err := NewExportActivity(archiver, nil, keys, "b"); err == nil {
		t.Error("nil store: expected error")
	}
	if _, err := NewExportActivity(archiver, store, nil, "b"); err == nil {
		t.Error("nil keys: expected error")
	}
}
