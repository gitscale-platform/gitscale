package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// ActivityNameDownloadDecrypt is the registered name for DownloadAndDecryptActivity.
const ActivityNameDownloadDecrypt = "billing.DownloadAndDecrypt"

// DownloadDecryptInput is the input to DownloadAndDecryptActivity.Execute.
type DownloadDecryptInput struct {
	Year            int
	Month           int
	ParquetKey      string
	SourcePartition string // mirrors manifest.source_partition (used as AAD prefix)
	EncFormat       string // mirrors manifest.enc_format — must equal encFormatV1
	KEKHint         string // mirrors manifest.kek_hint — must equal DEK.KEKHint
}

// DownloadDecryptResult points the next activity at the plaintext parquet on
// the worker's scratch volume. The path is intentionally per-attempt: the
// activity is registered with a non-zero start-to-close timeout and the
// caller is responsible for cleanup on workflow failure.
type DownloadDecryptResult struct {
	PlaintextPath string
}

// DownloadAndDecryptActivity streams the encrypted parquet through the
// chunked-frame decoder under the DEK derived for (year, month), writing
// plaintext to a worker-local scratch file. The same activity-host runs the
// LoadIntoQuarantine activity that consumes the file (Temporal task-queue
// affinity holds inside a single workflow run for this stage; if affinity is
// later relaxed, switch the artifact to an object-store handoff).
type DownloadAndDecryptActivity struct {
	store      ObjectStore
	keys       KeyProvider
	scratchDir string
}

// NewDownloadAndDecryptActivity returns a DownloadAndDecryptActivity.
// scratchDir defaults to os.TempDir() when empty.
func NewDownloadAndDecryptActivity(store ObjectStore, keys KeyProvider, scratchDir string) (*DownloadAndDecryptActivity, error) {
	if store == nil {
		return nil, errors.New("billing.NewDownloadAndDecryptActivity: store is nil")
	}
	if keys == nil {
		return nil, errors.New("billing.NewDownloadAndDecryptActivity: keys is nil")
	}
	if scratchDir == "" {
		scratchDir = os.TempDir()
	}
	return &DownloadAndDecryptActivity{store: store, keys: keys, scratchDir: scratchDir}, nil
}

// Execute resolves the DEK, validates the manifest's enc_format + kek_hint,
// streams the encrypted parquet through ChunkedDecoder, and writes the
// plaintext to a scratch file. Returns the scratch path so LoadIntoQuarantine
// can stream from it.
func (a *DownloadAndDecryptActivity) Execute(ctx context.Context, in DownloadDecryptInput) (DownloadDecryptResult, error) {
	if in.EncFormat != encFormatV1 {
		return DownloadDecryptResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("%v: %q (worker only reads %q)", ErrUnsupportedEncFormat, in.EncFormat, encFormatV1),
			"ErrUnsupportedEncFormat",
			ErrUnsupportedEncFormat,
		)
	}

	dek, err := a.keys.GetDEK(ctx, in.Year, in.Month)
	if err != nil {
		return DownloadDecryptResult{}, fmt.Errorf("download+decrypt: get dek: %w", err)
	}
	defer func() {
		for i := range dek.Bytes {
			dek.Bytes[i] = 0
		}
	}()
	if dek.KEKHint != in.KEKHint {
		return DownloadDecryptResult{}, fmt.Errorf(
			"download+decrypt: kek_hint mismatch — manifest=%q worker=%q "+
				"(operator must pin the historical key version; see runbook)",
			in.KEKHint, dek.KEKHint,
		)
	}

	body, err := a.store.Download(ctx, in.ParquetKey)
	if err != nil {
		return DownloadDecryptResult{}, fmt.Errorf("download+decrypt: download: %w", err)
	}
	defer func() { _ = body.Close() }()

	plaintextName := fmt.Sprintf("usage_events_%04d_%02d_restore.parquet", in.Year, in.Month)
	plaintextPath := filepath.Join(a.scratchDir, plaintextName)
	out, err := os.Create(plaintextPath)
	if err != nil {
		return DownloadDecryptResult{}, fmt.Errorf("download+decrypt: open scratch %s: %w", plaintextPath, err)
	}
	closeErr := out.Close
	defer func() {
		if closeErr != nil {
			_ = closeErr()
		}
	}()

	hb := &heartbeatWriter{ctx: ctx, dst: out, every: 4 << 20}
	dec := &ChunkedDecoder{DEK: dek.Bytes}
	if err := dec.DecodeStream(body, hb, in.SourcePartition); err != nil {
		_ = out.Close()
		closeErr = nil
		_ = os.Remove(plaintextPath)
		if errors.Is(err, ErrFrameTampered) {
			return DownloadDecryptResult{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("download+decrypt: decode: %v", err),
				"ErrFrameTampered", err,
			)
		}
		return DownloadDecryptResult{}, fmt.Errorf("download+decrypt: decode: %w", err)
	}
	if err := out.Close(); err != nil {
		closeErr = nil
		_ = os.Remove(plaintextPath)
		return DownloadDecryptResult{}, fmt.Errorf("download+decrypt: close scratch: %w", err)
	}
	closeErr = nil
	return DownloadDecryptResult{PlaintextPath: plaintextPath}, nil
}

// heartbeatWriter wraps an io.Writer and emits a Temporal heartbeat every
// `every` bytes written. Restore parquet streams are bounded by ExportActivity
// output (≈GB-scale max), so a 4 MiB cadence keeps the worker responsive
// without hammering the heartbeat path.
type heartbeatWriter struct {
	ctx     context.Context
	dst     interface{ Write(p []byte) (int, error) }
	every   int64
	written int64
	since   int64
}

func (h *heartbeatWriter) Write(p []byte) (int, error) {
	n, err := h.dst.Write(p)
	h.written += int64(n)
	h.since += int64(n)
	if h.since >= h.every {
		activity.RecordHeartbeat(h.ctx, h.written)
		h.since = 0
	}
	return n, err
}
