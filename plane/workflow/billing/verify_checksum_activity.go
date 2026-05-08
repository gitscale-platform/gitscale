package billing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.temporal.io/sdk/temporal"
)

// ActivityNameVerifyChecksum is the registered name for VerifyChecksumActivity.
const ActivityNameVerifyChecksum = "billing.VerifyChecksum"

// VerifyChecksumInput identifies the parquet + checksum sidecar pair to verify.
type VerifyChecksumInput struct {
	ParquetKey  string
	ChecksumKey string
}

// VerifyChecksumActivity recomputes SHA-256 over the encrypted parquet object
// and compares it to the sidecar checksum file. The hash is computed on the
// encrypted bytes — the same way ExportActivity wrote it (h.Write(frame))
// inside its encrypt loop. Verifying the encrypted bytes keeps the integrity
// proof independent of the DEK; tampering surfaces here, before any decrypt
// runs.
type VerifyChecksumActivity struct {
	store ObjectStore
}

// NewVerifyChecksumActivity returns a VerifyChecksumActivity.
func NewVerifyChecksumActivity(store ObjectStore) (*VerifyChecksumActivity, error) {
	if store == nil {
		return nil, errors.New("billing.NewVerifyChecksumActivity: store is nil")
	}
	return &VerifyChecksumActivity{store: store}, nil
}

// ErrChecksumMismatch is returned when the recomputed SHA-256 does not match
// the sidecar value. Surfaces as a non-retryable workflow error.
var ErrChecksumMismatch = errors.New("billing/restore: parquet checksum mismatch")

// Execute streams the encrypted parquet through SHA-256 and compares it to
// the hex-encoded sidecar value.
func (a *VerifyChecksumActivity) Execute(ctx context.Context, in VerifyChecksumInput) error {
	expectedRaw, err := a.store.GetBytes(ctx, in.ChecksumKey)
	if err != nil {
		return fmt.Errorf("verify checksum: fetch sidecar: %w", err)
	}
	expected := strings.TrimSpace(string(expectedRaw))

	body, err := a.store.Download(ctx, in.ParquetKey)
	if err != nil {
		return fmt.Errorf("verify checksum: download parquet: %w", err)
	}
	defer func() { _ = body.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return fmt.Errorf("verify checksum: hash stream: %w", err)
	}
	actual := fmt.Sprintf("%x", h.Sum(nil))
	if actual != expected {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("%v: expected=%s actual=%s key=%s",
				ErrChecksumMismatch, expected, actual, in.ParquetKey),
			"ErrChecksumMismatch",
			ErrChecksumMismatch,
		)
	}
	return nil
}
