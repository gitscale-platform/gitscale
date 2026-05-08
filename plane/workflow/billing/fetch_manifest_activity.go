package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"
)

// ActivityNameFetchManifest is the registered name for FetchManifestActivity.
const ActivityNameFetchManifest = "billing.FetchManifest"

// FetchManifestInput is the input to FetchManifestActivity.Execute.
type FetchManifestInput struct {
	Year  int
	Month int
}

// FetchManifestResult exposes the parsed manifest fields the workflow needs to
// validate and dispatch the rest of the restore pipeline. The full archived
// manifest type stays internal — this is the workflow's view of it.
type FetchManifestResult struct {
	SourcePartition string
	RowCount        int64
	BytesWritten    int64
	KEKHint         string
	EncFormat       string
	ChecksumAlg     string
	ParquetKey      string
	ChecksumKey     string
}

// FetchManifestActivity reads the .manifest.json sidecar for the archived
// (year, month) partition and parses it. Manifest absence surfaces as a
// non-retryable ErrObjectNotFound — operators must verify the archive exists
// before re-running the workflow.
type FetchManifestActivity struct {
	store ObjectStore
}

// NewFetchManifestActivity returns a FetchManifestActivity.
func NewFetchManifestActivity(store ObjectStore) (*FetchManifestActivity, error) {
	if store == nil {
		return nil, errors.New("billing.NewFetchManifestActivity: store is nil")
	}
	return &FetchManifestActivity{store: store}, nil
}

// Execute fetches and parses the manifest. Format dispatch (rejecting unknown
// enc_format) is the workflow's responsibility — keep this activity focused
// on I/O so it can be retried on transient S3 errors.
func (a *FetchManifestActivity) Execute(ctx context.Context, in FetchManifestInput) (FetchManifestResult, error) {
	parquetKey := archivedParquetKey(in.Year, in.Month)
	manifestKey := archivedSidecarKey(in.Year, in.Month, ".manifest.json")
	checksumKey := archivedSidecarKey(in.Year, in.Month, ".checksum.sha256")

	raw, err := a.store.GetBytes(ctx, manifestKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return FetchManifestResult{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("fetch manifest: %v", err),
				"ErrObjectNotFound", err,
			)
		}
		return FetchManifestResult{}, fmt.Errorf("fetch manifest: %w", err)
	}
	var m archiveManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return FetchManifestResult{}, fmt.Errorf("fetch manifest: parse %s: %w", manifestKey, err)
	}
	expected := fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month)
	if m.SourcePartition != expected {
		return FetchManifestResult{}, fmt.Errorf(
			"fetch manifest: source_partition=%q want %q (manifest does not match requested partition)",
			m.SourcePartition, expected,
		)
	}
	if m.SchemaVersion != 1 {
		return FetchManifestResult{}, fmt.Errorf(
			"fetch manifest: schema_version=%d unsupported (this worker reads only v1)",
			m.SchemaVersion,
		)
	}
	return FetchManifestResult{
		SourcePartition: m.SourcePartition,
		RowCount:        m.RowCount,
		BytesWritten:    m.BytesWritten,
		KEKHint:         m.KEKHint,
		EncFormat:       m.EncFormat,
		ChecksumAlg:     m.ChecksumAlg,
		ParquetKey:      parquetKey,
		ChecksumKey:     checksumKey,
	}, nil
}

// archivedParquetKey is the canonical encrypted parquet key for (year, month).
// Mirror of the layout in export_activity.go — keeping a single helper avoids
// drift between encode and decode sides.
func archivedParquetKey(year, month int) string {
	return fmt.Sprintf(
		"billing/usage_events/year=%04d/month=%02d/usage_events_%04d_%02d.parquet",
		year, month, year, month,
	)
}

// archivedSidecarKey returns the sidecar key (manifest, checksum) alongside
// the parquet object.
func archivedSidecarKey(year, month int, suffix string) string {
	return fmt.Sprintf(
		"billing/usage_events/year=%04d/month=%02d/usage_events_%04d_%02d%s",
		year, month, year, month, suffix,
	)
}
