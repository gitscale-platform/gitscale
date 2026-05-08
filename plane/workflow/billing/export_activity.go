package billing

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/parquet-go/parquet-go"
	"go.temporal.io/sdk/activity"
)

const ActivityNameExport = "billing.Export"

// ExportInput is the input to ExportActivity.Execute.
type ExportInput struct {
	Year  int
	Month int
}

// ExportResult is returned by ExportActivity.Execute.
type ExportResult struct {
	LakeURI      string
	RowCount     int64
	BytesWritten int64
	SHA256Hex    string
}

// archiveManifest is the .manifest.json written alongside each Parquet file.
type archiveManifest struct {
	SchemaVersion   int    `json:"schema_version"`
	SourcePartition string `json:"source_partition"`
	RowCount        int64  `json:"row_count"`
	BytesWritten    int64  `json:"bytes_written"`
	KEKHint         string `json:"kek_hint"`
	EncFormat       string `json:"enc_format"`
	ArchiveTS       string `json:"archive_ts"`
	ChecksumAlg     string `json:"checksum_alg"`
}

// encFormatV1 identifies the chunked AES-256-GCM frame format with 4 MiB
// plaintext chunks. RestorePartition decoders dispatch on this string.
const encFormatV1 = "aes-256-gcm-v1-4mib"

// ExportActivity streams rows from a detached partition to the object store as
// Parquet+zstd encrypted with AES-256-GCM (chunked streaming format).
type ExportActivity struct {
	archiver billingstore.Archiver
	store    ObjectStore
	keys     KeyProvider
	bucket   string
}

// NewExportActivity returns an ExportActivity. All deps must be non-nil.
func NewExportActivity(
	archiver billingstore.Archiver,
	store ObjectStore,
	keys KeyProvider,
	bucket string,
) (*ExportActivity, error) {
	if archiver == nil {
		return nil, errors.New("billing.NewExportActivity: archiver is nil")
	}
	if store == nil {
		return nil, errors.New("billing.NewExportActivity: store is nil")
	}
	if keys == nil {
		return nil, errors.New("billing.NewExportActivity: keys is nil")
	}
	return &ExportActivity{archiver: archiver, store: store, keys: keys, bucket: bucket}, nil
}

// Execute streams the partition to the object store.
//
// Pipeline (three concurrent stages connected by io.Pipe):
//  1. Parquet goroutine: rows → parquet-go writer → plaintextW
//  2. Encrypt goroutine: plaintextR → chunked AES-256-GCM → cipherW; computes SHA-256
//  3. Main:             cipherR → ObjectStore.Upload
//
// Heartbeats every 10 000 rows so Temporal can cancel on timeout.
func (a *ExportActivity) Execute(ctx context.Context, in ExportInput) (ExportResult, error) {
	dek, err := a.keys.GetDEK(ctx, in.Year, in.Month)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: get dek: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: new gcm: %w", err)
	}

	cursor, err := a.archiver.ScanPartitionRows(ctx, in.Year, in.Month)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export: scan rows: %w", err)
	}

	partitionName := fmt.Sprintf("billing.usage_events_%04d_%02d", in.Year, in.Month)

	plaintextR, plaintextW := io.Pipe()
	rowCountCh := make(chan int64, 1)
	writeErrCh := make(chan error, 1)
	go func() {
		defer func() { _ = cursor.Close() }()
		pw := parquet.NewGenericWriter[billingstore.UsageEventRow](plaintextW)
		var rows int64
		for cursor.Next(ctx) {
			row := cursor.Row()
			if _, werr := pw.Write([]billingstore.UsageEventRow{row}); werr != nil {
				plaintextW.CloseWithError(werr)
				rowCountCh <- rows
				writeErrCh <- werr
				return
			}
			rows++
			if rows%10000 == 0 {
				activity.RecordHeartbeat(ctx, rows)
			}
		}
		if cerr := cursor.Err(); cerr != nil {
			plaintextW.CloseWithError(cerr)
			rowCountCh <- rows
			writeErrCh <- cerr
			return
		}
		if cerr := pw.Close(); cerr != nil {
			plaintextW.CloseWithError(cerr)
			rowCountCh <- rows
			writeErrCh <- cerr
			return
		}
		_ = plaintextW.Close()
		rowCountCh <- rows
		writeErrCh <- nil
	}()

	cipherR, cipherW := io.Pipe()
	h := sha256.New()
	var bytesWritten int64
	encErrCh := make(chan error, 1)
	go func() {
		// Chunk frame: [4-byte BE payload_len][12-byte nonce][GCM ciphertext+tag]
		// AEAD AAD: "<partition_name>:<chunk_index>" — binds chunk to source partition
		//   and prevents cross-file splicing. RestorePartition recomputes AAD on decode.
		// Format identifier: "aes-256-gcm-v1-4mib" (see manifest.enc_format).
		defer func() { _ = cipherW.Close() }()
		buf := make([]byte, 4<<20) // 4 MiB
		var chunkIndex uint64
		for {
			n, readErr := io.ReadFull(plaintextR, buf)
			if n > 0 {
				nonce := make([]byte, aead.NonceSize())
				if _, rerr := rand.Read(nonce); rerr != nil {
					cipherW.CloseWithError(rerr)
					encErrCh <- rerr
					return
				}
				aad := []byte(fmt.Sprintf("%s:%d", partitionName, chunkIndex))
				ct := aead.Seal(nil, nonce, buf[:n], aad)
				chunkIndex++

				payloadLen := uint32(len(nonce) + len(ct))
				frame := make([]byte, 4+int(payloadLen))
				binary.BigEndian.PutUint32(frame[:4], payloadLen)
				copy(frame[4:], nonce)
				copy(frame[4+len(nonce):], ct)

				h.Write(frame)
				bytesWritten += int64(len(frame))
				if _, werr := cipherW.Write(frame); werr != nil {
					encErrCh <- werr
					return
				}
			}
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				encErrCh <- nil
				return
			}
			if readErr != nil {
				cipherW.CloseWithError(readErr)
				encErrCh <- readErr
				return
			}
		}
	}()

	parquetKey := fmt.Sprintf(
		"billing/usage_events/year=%04d/month=%02d/usage_events_%04d_%02d.parquet",
		in.Year, in.Month, in.Year, in.Month,
	)
	uri, uploadErr := a.store.Upload(ctx, parquetKey, cipherR, -1)

	// Always close pipe readers — unblocks goroutines if Upload errored mid-stream.
	// CloseWithError on a closed pipe is a no-op.
	_ = cipherR.CloseWithError(uploadErr)
	_ = plaintextR.CloseWithError(uploadErr)

	// Always drain — channels are buffered(1). The parquet goroutine always sends
	// exactly once on rowCountCh and writeErrCh; the encrypt goroutine always sends
	// exactly once on encErrCh.
	writeErr := <-writeErrCh
	encErr := <-encErrCh
	rowCount := <-rowCountCh

	if uploadErr != nil {
		return ExportResult{}, fmt.Errorf("export: upload: %w", uploadErr)
	}
	if writeErr != nil {
		return ExportResult{}, fmt.Errorf("export: parquet write: %w", writeErr)
	}
	if encErr != nil {
		return ExportResult{}, fmt.Errorf("export: encrypt: %w", encErr)
	}
	sha256hex := fmt.Sprintf("%x", h.Sum(nil))

	base := strings.TrimSuffix(parquetKey, ".parquet")
	manifest := archiveManifest{
		SchemaVersion:   1,
		SourcePartition: partitionName,
		RowCount:        rowCount,
		BytesWritten:    bytesWritten,
		KEKHint:         "platform-billing-v1",
		EncFormat:       encFormatV1,
		ArchiveTS:       time.Now().UTC().Format(time.RFC3339),
		ChecksumAlg:     "sha256",
	}
	manifestJSON, _ := json.Marshal(manifest)
	if merr := a.store.PutBytes(ctx, base+".manifest.json", manifestJSON); merr != nil {
		return ExportResult{}, fmt.Errorf("export: manifest: %w", merr)
	}
	if cerr := a.store.PutBytes(ctx, base+".checksum.sha256", []byte(sha256hex)); cerr != nil {
		return ExportResult{}, fmt.Errorf("export: checksum: %w", cerr)
	}

	return ExportResult{
		LakeURI:      uri,
		RowCount:     rowCount,
		BytesWritten: bytesWritten,
		SHA256Hex:    sha256hex,
	}, nil
}
