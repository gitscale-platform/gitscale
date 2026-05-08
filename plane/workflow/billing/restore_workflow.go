package billing

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// RestorePartitionWorkflowName is the registered name for the restore
// workflow. The workflow ID convention is
// "billing.restore-partition.YYYY-MM" so re-runs for the same (year, month)
// are idempotent under Temporal's existing-workflow handling.
const RestorePartitionWorkflowName = "billing.RestorePartitionWorkflow"

// RestoreInput is the input to RestorePartitionWorkflow.
type RestoreInput struct {
	Year  int
	Month int
}

// RestoreResult is returned by RestorePartitionWorkflow on success.
type RestoreResult struct {
	QuarantineTable string
	RowsImported    int64
	DEKVersionUsed  int // parsed from manifest.kek_hint ("platform-billing-v<N>")
}

// nonRetryableRestoreErrors collects error types we never want Temporal to
// retry: format/integrity mismatches that will fail the same way every time.
var nonRetryableRestoreErrors = []string{
	"ErrUnsupportedEncFormat",
	"ErrChecksumMismatch",
	"ErrFrameTampered",
	"ErrObjectNotFound",
}

// RestorePartitionWorkflow restores an archived monthly partition to a
// quarantine table for dispute investigation / audit-restore.
//
// Activity sequence (sequential):
//  1. FetchManifest        — read .manifest.json, validate source_partition + schema
//  2. VerifyChecksum       — recompute SHA-256 over the encrypted parquet
//  3. DownloadAndDecrypt   — stream + AES-256-GCM-v1-4mib decode → scratch file
//  4. LoadIntoQuarantine   — CREATE quarantine table, COPY plaintext rows, REVOKE writes
//
// Workflow body is purely deterministic — no time/random/IO. Retry policy is
// the platform default; non-retryable errors (ErrUnsupportedEncFormat,
// ErrChecksumMismatch, ErrFrameTampered, ErrObjectNotFound) are wrapped with
// temporal.NewNonRetryableApplicationError at the activity boundary so they
// short-circuit the policy.
func RestorePartitionWorkflow(ctx workflow.Context, in RestoreInput) (RestoreResult, error) {
	if in.Year < 2026 || in.Year > 2099 {
		return RestoreResult{}, fmt.Errorf("restore: year %d out of supported range [2026, 2099]", in.Year)
	}
	if in.Month < 1 || in.Month > 12 {
		return RestoreResult{}, fmt.Errorf("restore: month %d out of range [1, 12]", in.Month)
	}

	rp := gswf.DefaultRetryPolicy()
	rp.NonRetryableErrorTypes = nonRetryableRestoreErrors

	shortOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         rp,
	}
	longOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 4 * time.Hour,
		HeartbeatTimeout:    5 * time.Minute,
		RetryPolicy:         rp,
	}

	// 1. FetchManifest
	fetchCtx := workflow.WithActivityOptions(ctx, shortOpts)
	var manifest FetchManifestResult
	if err := workflow.ExecuteActivity(fetchCtx, ActivityNameFetchManifest,
		FetchManifestInput{Year: in.Year, Month: in.Month},
	).Get(fetchCtx, &manifest); err != nil {
		return RestoreResult{}, fmt.Errorf("restore: fetch manifest: %w", err)
	}

	// Workflow-side enc_format gate. We also enforce inside the activity, but
	// rejecting here saves a checksum re-hash for the unsupported case.
	if manifest.EncFormat != encFormatV1 {
		return RestoreResult{}, fmt.Errorf(
			"restore: unsupported enc_format %q in manifest (worker reads only %q)",
			manifest.EncFormat, encFormatV1,
		)
	}

	// 2. VerifyChecksum
	verifyCtx := workflow.WithActivityOptions(ctx, longOpts)
	if err := workflow.ExecuteActivity(verifyCtx, ActivityNameVerifyChecksum,
		VerifyChecksumInput{ParquetKey: manifest.ParquetKey, ChecksumKey: manifest.ChecksumKey},
	).Get(verifyCtx, nil); err != nil {
		return RestoreResult{}, fmt.Errorf("restore: verify checksum: %w", err)
	}

	// 3. DownloadAndDecrypt
	decCtx := workflow.WithActivityOptions(ctx, longOpts)
	var decResult DownloadDecryptResult
	if err := workflow.ExecuteActivity(decCtx, ActivityNameDownloadDecrypt,
		DownloadDecryptInput{
			Year:            in.Year,
			Month:           in.Month,
			ParquetKey:      manifest.ParquetKey,
			SourcePartition: manifest.SourcePartition,
			EncFormat:       manifest.EncFormat,
			KEKHint:         manifest.KEKHint,
		},
	).Get(decCtx, &decResult); err != nil {
		return RestoreResult{}, fmt.Errorf("restore: decrypt: %w", err)
	}

	// 4. LoadIntoQuarantine — drop quarantine on failure (compensation).
	loadCtx := workflow.WithActivityOptions(ctx, longOpts)
	var loadResult LoadQuarantineResult
	loadErr := workflow.ExecuteActivity(loadCtx, ActivityNameLoadQuarantine,
		LoadQuarantineInput{Year: in.Year, Month: in.Month, PlaintextPath: decResult.PlaintextPath},
	).Get(loadCtx, &loadResult)
	if loadErr != nil {
		// Compensation: drop the partially-loaded quarantine table. We don't
		// surface a compensation error — it would mask the root cause.
		dropCtx := workflow.WithActivityOptions(ctx, shortOpts)
		_ = workflow.ExecuteActivity(dropCtx, ActivityNameDropQuarantine,
			DropQuarantineInput{Year: in.Year, Month: in.Month},
		).Get(dropCtx, nil)
		return RestoreResult{}, fmt.Errorf("restore: load quarantine: %w", loadErr)
	}

	return RestoreResult{
		QuarantineTable: loadResult.QuarantineTable,
		RowsImported:    loadResult.RowsImported,
		DEKVersionUsed:  parseKEKVersion(manifest.KEKHint),
	}, nil
}

// parseKEKVersion extracts <N> from a KEK hint of the form
// "platform-billing-v<N>". Returns 0 for the stub provider's hint or any
// unparseable form — the operator-facing surface is informational, not load-
// bearing.
func parseKEKVersion(hint string) int {
	const prefix = "platform-billing-v"
	if !strings.HasPrefix(hint, prefix) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(hint, prefix))
	if err != nil {
		return 0
	}
	return n
}

// ErrRestoreInputInvalid is exposed for callers that want to distinguish input
// validation errors from activity failures.
var ErrRestoreInputInvalid = errors.New("billing/restore: invalid input")
