package billing

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrFrameTampered is returned by ChunkedDecoder.DecodeStream when a frame
// fails AEAD verification (wrong DEK, wrong AAD, truncated chunk, or
// out-of-order chunk index). All four collapse to "tampered" because GCM tag
// verification is the integrity oracle.
var ErrFrameTampered = errors.New("billing/restore: frame tampered or DEK mismatch")

// ErrUnsupportedEncFormat is returned when a manifest's enc_format is not
// encFormatV1. Restore intentionally has no compatibility shim: a future
// version (e.g. ChaCha20-Poly1305 or different chunk size) requires a new
// decoder, not graceful fallback.
var ErrUnsupportedEncFormat = errors.New("billing/restore: unsupported enc_format")

// maxFramePayloadBytes caps the payload portion of a single frame at the
// plaintext chunk size + nonce + GCM tag + 1 KiB headroom. The encrypt loop
// in export_activity.go uses 4 MiB plaintext chunks; legitimate frames are
// always at most 4 MiB + 12 (nonce) + 16 (tag) = 4 MiB + 28 bytes. The
// extra slack absorbs format-version drift without exposing the worker to
// unbounded allocation from a maliciously crafted length prefix.
const maxFramePayloadBytes = (4 << 20) + 1024

// ChunkedDecoder is the inverse of the encrypt loop in export_activity.go.
// It expects a stream of frames in the format
//
//	[4-byte BE payload_len][12-byte nonce][GCM ciphertext+tag]
//
// where AAD per chunk is "<partitionName>:<chunkIndex>" with chunkIndex
// starting at 0 and incrementing per frame. The decoder verifies AEAD on
// every chunk; any deviation surfaces as ErrFrameTampered.
type ChunkedDecoder struct {
	DEK []byte // 32 bytes (AES-256)
}

// DecodeStream reads encrypted frames from r, decrypts each chunk under the
// DEK with AAD "<partitionName>:<chunkIndex>", and writes plaintext chunks
// to w in order. EOF on a frame boundary is normal termination; EOF mid-frame
// surfaces as ErrFrameTampered.
func (d *ChunkedDecoder) DecodeStream(r io.Reader, w io.Writer, partitionName string) error {
	if len(d.DEK) != 32 {
		return fmt.Errorf("billing/restore: DEK must be 32 bytes, got %d", len(d.DEK))
	}
	block, err := aes.NewCipher(d.DEK)
	if err != nil {
		return fmt.Errorf("billing/restore: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("billing/restore: new gcm: %w", err)
	}
	nonceSize := aead.NonceSize()
	overhead := aead.Overhead()

	var lenBuf [4]byte
	var chunkIndex uint64
	for {
		// Read the 4-byte BE payload length. EOF here = clean stream end.
		_, err := io.ReadFull(r, lenBuf[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// io.ErrUnexpectedEOF on the length prefix means the producer
			// truncated mid-header; treat as tampered.
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated length prefix at chunk %d", ErrFrameTampered, chunkIndex)
			}
			return fmt.Errorf("billing/restore: read length at chunk %d: %w", chunkIndex, err)
		}
		payloadLen := binary.BigEndian.Uint32(lenBuf[:])
		if int(payloadLen) < nonceSize+overhead {
			return fmt.Errorf("%w: payload_len %d < nonce+tag at chunk %d",
				ErrFrameTampered, payloadLen, chunkIndex)
		}
		if int(payloadLen) > maxFramePayloadBytes {
			return fmt.Errorf("%w: payload_len %d exceeds cap at chunk %d",
				ErrFrameTampered, payloadLen, chunkIndex)
		}

		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated payload at chunk %d", ErrFrameTampered, chunkIndex)
			}
			return fmt.Errorf("billing/restore: read payload at chunk %d: %w", chunkIndex, err)
		}
		nonce := payload[:nonceSize]
		ct := payload[nonceSize:]

		aad := []byte(fmt.Sprintf("%s:%d", partitionName, chunkIndex))
		pt, err := aead.Open(nil, nonce, ct, aad)
		if err != nil {
			// AEAD failure is the canonical "tampered" oracle: includes wrong
			// DEK, wrong partition name, out-of-order index, bit-flip in ct.
			return fmt.Errorf("%w: chunk %d open: %v", ErrFrameTampered, chunkIndex, err)
		}
		if _, werr := w.Write(pt); werr != nil {
			return fmt.Errorf("billing/restore: write plaintext at chunk %d: %w", chunkIndex, werr)
		}
		chunkIndex++
	}
}
