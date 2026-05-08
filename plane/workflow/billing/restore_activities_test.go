package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
)

// fixedKeyProvider is a KeyProvider that returns a constant DEK + KEK hint —
// used by the restore-side unit tests to drive the kek_hint mismatch path.
type fixedKeyProvider struct {
	dek     []byte
	kekHint string
	err     error
}

func (f fixedKeyProvider) GetDEK(_ context.Context, _, _ int) (DEK, error) {
	if f.err != nil {
		return DEK{}, f.err
	}
	return DEK{Bytes: append([]byte(nil), f.dek...), KEKHint: f.kekHint}, nil
}

func TestFetchManifestActivity_happyPath(t *testing.T) {
	store := newStubObjectStore("b")
	m := archiveManifest{
		SchemaVersion:   1,
		SourcePartition: "billing.usage_events_2026_05",
		RowCount:        7,
		BytesWritten:    1024,
		KEKHint:         "platform-billing-v1",
		EncFormat:       encFormatV1,
		ChecksumAlg:     "sha256",
	}
	store.Set(archivedSidecarKey(2026, 5, ".manifest.json"), jsonMarshalManifest(m))

	act, err := NewFetchManifestActivity(store)
	if err != nil {
		t.Fatal(err)
	}
	out, err := act.Execute(context.Background(), FetchManifestInput{Year: 2026, Month: 5})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.SourcePartition != m.SourcePartition || out.RowCount != 7 || out.EncFormat != encFormatV1 {
		t.Errorf("unexpected result %+v", out)
	}
	if out.ParquetKey == "" || out.ChecksumKey == "" {
		t.Error("missing key fields")
	}
}

func TestFetchManifestActivity_rejectsWrongSourcePartition(t *testing.T) {
	store := newStubObjectStore("b")
	m := archiveManifest{
		SchemaVersion:   1,
		SourcePartition: "billing.usage_events_2026_06",
		EncFormat:       encFormatV1,
	}
	store.Set(archivedSidecarKey(2026, 5, ".manifest.json"), jsonMarshalManifest(m))

	act, _ := NewFetchManifestActivity(store)
	_, err := act.Execute(context.Background(), FetchManifestInput{Year: 2026, Month: 5})
	if err == nil || !strings.Contains(err.Error(), "source_partition") {
		t.Errorf("err=%v want source_partition mismatch", err)
	}
}

func TestFetchManifestActivity_missingManifestNonRetryable(t *testing.T) {
	store := newStubObjectStore("b")
	act, _ := NewFetchManifestActivity(store)
	_, err := act.Execute(context.Background(), FetchManifestInput{Year: 2026, Month: 5})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err=%v want not-found", err)
	}
}

func TestVerifyChecksumActivity_happyPath(t *testing.T) {
	store := newStubObjectStore("b")
	parquetBytes := []byte("encrypted-parquet-bytes")
	store.Set("p.parquet", parquetBytes)
	h := sha256.Sum256(parquetBytes)
	store.Set("p.checksum", []byte(fmt.Sprintf("%x", h[:])))

	act, _ := NewVerifyChecksumActivity(store)
	if err := act.Execute(context.Background(), VerifyChecksumInput{
		ParquetKey: "p.parquet", ChecksumKey: "p.checksum",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestVerifyChecksumActivity_mismatch(t *testing.T) {
	store := newStubObjectStore("b")
	store.Set("p.parquet", []byte("real"))
	store.Set("p.checksum", []byte(strings.Repeat("0", 64)))
	act, _ := NewVerifyChecksumActivity(store)
	err := act.Execute(context.Background(), VerifyChecksumInput{ParquetKey: "p.parquet", ChecksumKey: "p.checksum"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("err=%v want checksum mismatch", err)
	}
}

func TestDownloadAndDecryptActivity_unsupportedEncFormatRejected(t *testing.T) {
	store := newStubObjectStore("b")
	keys := fixedKeyProvider{dek: deriveTestDEK(t, 2026, 5), kekHint: "platform-billing-v1"}
	tmp := t.TempDir()
	act, err := NewDownloadAndDecryptActivity(store, keys, tmp)
	if err != nil {
		t.Fatal(err)
	}
	_, err = act.Execute(context.Background(), DownloadDecryptInput{
		Year: 2026, Month: 5, EncFormat: "future-format-v9",
	})
	if !errors.Is(err, ErrUnsupportedEncFormat) {
		t.Errorf("err=%v want ErrUnsupportedEncFormat", err)
	}
}

func TestDownloadAndDecryptActivity_kekHintMismatch(t *testing.T) {
	store := newStubObjectStore("b")
	keys := fixedKeyProvider{dek: deriveTestDEK(t, 2026, 5), kekHint: "platform-billing-v3"}
	tmp := t.TempDir()
	act, _ := NewDownloadAndDecryptActivity(store, keys, tmp)
	_, err := act.Execute(context.Background(), DownloadDecryptInput{
		Year: 2026, Month: 5, EncFormat: encFormatV1, KEKHint: "platform-billing-v1",
	})
	if err == nil || !strings.Contains(err.Error(), "kek_hint mismatch") {
		t.Errorf("err=%v want kek_hint mismatch", err)
	}
}

func TestDownloadAndDecryptActivity_roundTrip(t *testing.T) {
	store := newStubObjectStore("b")
	dek := deriveTestDEK(t, 2026, 5)
	plaintext := bytes.Repeat([]byte("xyz "), 1024)
	partition := "billing.usage_events_2026_05"
	enc := encodeChunked(t, dek, plaintext, partition)
	parquetKey := archivedParquetKey(2026, 5)
	store.Set(parquetKey, enc)

	keys := fixedKeyProvider{dek: dek, kekHint: "platform-billing-v1"}
	tmp := t.TempDir()
	act, _ := NewDownloadAndDecryptActivity(store, keys, tmp)

	out, err := act.Execute(context.Background(), DownloadDecryptInput{
		Year: 2026, Month: 5,
		ParquetKey:      parquetKey,
		SourcePartition: partition,
		EncFormat:       encFormatV1,
		KEKHint:         "platform-billing-v1",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, err := os.ReadFile(out.PlaintextPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("plaintext mismatch")
	}
	// scratch file landed under the configured scratch dir
	if filepath.Dir(out.PlaintextPath) != tmp {
		t.Errorf("scratch dir=%q want under %q", out.PlaintextPath, tmp)
	}
}

func TestDownloadAndDecryptActivity_tamperedScratchCleanedUp(t *testing.T) {
	store := newStubObjectStore("b")
	dek := deriveTestDEK(t, 2026, 5)
	enc := encodeChunked(t, dek, []byte("hello"), "p")
	// flip a byte in the ciphertext payload region (after the 4-byte length + 12-byte nonce)
	tampered := append([]byte(nil), enc...)
	tampered[20] ^= 0xff
	parquetKey := archivedParquetKey(2026, 5)
	store.Set(parquetKey, tampered)

	keys := fixedKeyProvider{dek: dek, kekHint: "h"}
	tmp := t.TempDir()
	act, _ := NewDownloadAndDecryptActivity(store, keys, tmp)
	_, err := act.Execute(context.Background(), DownloadDecryptInput{
		Year: 2026, Month: 5,
		ParquetKey:      parquetKey,
		SourcePartition: "p",
		EncFormat:       encFormatV1,
		KEKHint:         "h",
	})
	if err == nil {
		t.Fatal("expected tampered error")
	}
	files, _ := os.ReadDir(tmp)
	if len(files) != 0 {
		t.Errorf("scratch dir not cleaned: %d files left", len(files))
	}
}

func TestLoadIntoQuarantineActivity_orchestratesEnsureLoadSeal(t *testing.T) {
	restorer := billingstore.NewStubRestorer()
	restorer.SetRowReader(func(_, _ int, r io.Reader) (int64, error) {
		_, _ = io.Copy(io.Discard, r)
		return 42, nil
	})
	tmp := t.TempDir()
	scratch := filepath.Join(tmp, "p.parquet")
	if err := os.WriteFile(scratch, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	act, err := NewLoadIntoQuarantineActivity(restorer)
	if err != nil {
		t.Fatal(err)
	}
	out, err := act.Execute(context.Background(), LoadQuarantineInput{
		Year: 2026, Month: 5, PlaintextPath: scratch,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.RowsImported != 42 {
		t.Errorf("RowsImported=%d want 42", out.RowsImported)
	}
	if !restorer.IsCreated(2026, 5) {
		t.Error("EnsureQuarantineTable not called")
	}
	if !restorer.IsSealed(2026, 5) {
		t.Error("SealQuarantineTable not called")
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Errorf("scratch %s not cleaned: %v", scratch, statErr)
	}
}

func TestDropQuarantineActivity_invokesRestorer(t *testing.T) {
	restorer := billingstore.NewStubRestorer()
	act, err := NewDropQuarantineActivity(restorer)
	if err != nil {
		t.Fatal(err)
	}
	if err := act.Execute(context.Background(), DropQuarantineInput{Year: 2026, Month: 5}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !restorer.IsDropped(2026, 5) {
		t.Error("DropQuarantineTable not called")
	}
}

func TestRestoreActivities_nilDepsRejected(t *testing.T) {
	if _, err := NewFetchManifestActivity(nil); err == nil {
		t.Error("nil store: expected error")
	}
	if _, err := NewVerifyChecksumActivity(nil); err == nil {
		t.Error("nil store: expected error")
	}
	if _, err := NewDownloadAndDecryptActivity(nil, fixedKeyProvider{}, ""); err == nil {
		t.Error("nil store: expected error")
	}
	if _, err := NewDownloadAndDecryptActivity(newStubObjectStore("b"), nil, ""); err == nil {
		t.Error("nil keys: expected error")
	}
	if _, err := NewLoadIntoQuarantineActivity(nil); err == nil {
		t.Error("nil restorer: expected error")
	}
	if _, err := NewDropQuarantineActivity(nil); err == nil {
		t.Error("nil restorer: expected error")
	}
}
