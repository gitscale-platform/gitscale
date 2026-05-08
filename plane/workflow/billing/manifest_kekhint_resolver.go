package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ManifestKEKHintResolver resolves a partition's kek_hint by fetching the
// archiveManifest JSON sibling from the object store. The manifest key is
// derived from the lake URI by replacing ".parquet" with ".manifest.json".
//
// Used by ListEligiblePartitionsActivity (#80). Failures (missing manifest,
// malformed JSON, empty kek_hint) return an error; the caller treats this
// as a "missing_kek_hint" skip rather than aborting the run.
type ManifestKEKHintResolver struct {
	store  ObjectStore
	bucket string
}

// NewManifestKEKHintResolver wraps the supplied ObjectStore. The bucket is
// used to strip the canonical "s3://<bucket>/" prefix from the lake URI to
// recover the object key. Both deps must be non-nil.
func NewManifestKEKHintResolver(store ObjectStore, bucket string) (*ManifestKEKHintResolver, error) {
	if store == nil {
		return nil, errors.New("billing.NewManifestKEKHintResolver: store is nil")
	}
	if bucket == "" {
		return nil, errors.New("billing.NewManifestKEKHintResolver: bucket is empty")
	}
	return &ManifestKEKHintResolver{store: store, bucket: bucket}, nil
}

// ResolveKEKHint reads the manifest.json next to the parquet at lakeURI and
// returns the kek_hint string. lakeURI is expected to end in ".parquet"; any
// other suffix returns an error. A missing kek_hint key in the manifest is
// reported as an error so the workflow can skip the partition explicitly.
func (r *ManifestKEKHintResolver) ResolveKEKHint(ctx context.Context, lakeURI string) (string, error) {
	prefix := fmt.Sprintf("s3://%s/", r.bucket)
	if !strings.HasPrefix(lakeURI, prefix) {
		return "", fmt.Errorf("manifest resolver: lake_uri %q does not start with %q", lakeURI, prefix)
	}
	parquetKey := strings.TrimPrefix(lakeURI, prefix)
	if !strings.HasSuffix(parquetKey, ".parquet") {
		return "", fmt.Errorf("manifest resolver: lake_uri %q does not end in .parquet", lakeURI)
	}
	manifestKey := strings.TrimSuffix(parquetKey, ".parquet") + ".manifest.json"
	data, err := r.store.GetBytes(ctx, manifestKey)
	if err != nil {
		return "", fmt.Errorf("manifest resolver: %w", err)
	}
	var m struct {
		KEKHint string `json:"kek_hint"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("manifest resolver: unmarshal %s: %w", manifestKey, err)
	}
	if m.KEKHint == "" {
		return "", fmt.Errorf("manifest resolver: empty kek_hint in %s", manifestKey)
	}
	return m.KEKHint, nil
}
