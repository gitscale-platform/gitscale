package billing

import (
	"context"
	"encoding/json"
	"testing"
)

func TestManifestKEKHintResolver_ReadsKEKHintFromManifest(t *testing.T) {
	store := newStubObjectStore("test-bucket")
	manifest := archiveManifest{
		SchemaVersion: 1, KEKHint: "platform-billing-v3", EncFormat: encFormatV1,
	}
	raw, _ := json.Marshal(manifest)
	if err := store.PutBytes(context.Background(),
		"billing/usage_events/year=2027/month=01/usage_events_2027_01.manifest.json", raw); err != nil {
		t.Fatalf("PutBytes: %v", err)
	}

	r, err := NewManifestKEKHintResolver(store, "test-bucket")
	if err != nil {
		t.Fatalf("NewManifestKEKHintResolver: %v", err)
	}
	hint, err := r.ResolveKEKHint(context.Background(),
		"s3://test-bucket/billing/usage_events/year=2027/month=01/usage_events_2027_01.parquet")
	if err != nil {
		t.Fatalf("ResolveKEKHint: %v", err)
	}
	if hint != "platform-billing-v3" {
		t.Errorf("hint=%q want platform-billing-v3", hint)
	}
}

func TestManifestKEKHintResolver_BadURI(t *testing.T) {
	store := newStubObjectStore("test-bucket")
	r, _ := NewManifestKEKHintResolver(store, "test-bucket")

	cases := []string{
		"https://example.com/x.parquet",       // wrong scheme
		"s3://other/x.parquet",                // wrong bucket
		"s3://test-bucket/x.txt",              // wrong suffix
	}
	for _, uri := range cases {
		if _, err := r.ResolveKEKHint(context.Background(), uri); err == nil {
			t.Errorf("expected error for %q", uri)
		}
	}
}

func TestManifestKEKHintResolver_MissingManifest(t *testing.T) {
	store := newStubObjectStore("test-bucket")
	r, _ := NewManifestKEKHintResolver(store, "test-bucket")
	_, err := r.ResolveKEKHint(context.Background(),
		"s3://test-bucket/no/such/object.parquet")
	if err == nil {
		t.Error("expected error for missing manifest")
	}
}

func TestManifestKEKHintResolver_EmptyKEKHint(t *testing.T) {
	store := newStubObjectStore("test-bucket")
	raw, _ := json.Marshal(archiveManifest{SchemaVersion: 1, KEKHint: ""})
	_ = store.PutBytes(context.Background(),
		"x.manifest.json", raw)
	r, _ := NewManifestKEKHintResolver(store, "test-bucket")
	if _, err := r.ResolveKEKHint(context.Background(), "s3://test-bucket/x.parquet"); err == nil {
		t.Error("expected error for empty kek_hint")
	}
}

func TestNewManifestKEKHintResolver_NilDeps(t *testing.T) {
	if _, err := NewManifestKEKHintResolver(nil, "b"); err == nil {
		t.Error("expected error for nil store")
	}
	if _, err := NewManifestKEKHintResolver(newStubObjectStore("b"), ""); err == nil {
		t.Error("expected error for empty bucket")
	}
}
