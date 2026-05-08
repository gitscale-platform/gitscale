package billing

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

// encodeChunked replicates the encrypt loop in export_activity.go. Mirroring
// it inside the test (rather than calling ExportActivity) keeps the decoder
// test focused on the wire format and avoids pulling in parquet / archiver
// machinery — and any future drift between encode and decode is caught by
// the round-trip integration test.
func encodeChunked(t *testing.T, dek []byte, plaintext []byte, partitionName string) []byte {
	t.Helper()
	block, err := aes.NewCipher(dek)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	const chunkSize = 4 << 20
	var out bytes.Buffer
	var chunkIndex uint64
	r := bytes.NewReader(plaintext)
	buf := make([]byte, chunkSize)
	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			nonce := make([]byte, aead.NonceSize())
			if _, rerr := rand.Read(nonce); rerr != nil {
				t.Fatalf("nonce: %v", rerr)
			}
			aad := []byte(fmt.Sprintf("%s:%d", partitionName, chunkIndex))
			ct := aead.Seal(nil, nonce, buf[:n], aad)
			chunkIndex++

			payloadLen := uint32(len(nonce) + len(ct))
			frame := make([]byte, 4+int(payloadLen))
			binary.BigEndian.PutUint32(frame[:4], payloadLen)
			copy(frame[4:], nonce)
			copy(frame[4+len(nonce):], ct)
			out.Write(frame)
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return out.Bytes()
		}
		if readErr != nil {
			t.Fatalf("read: %v", readErr)
		}
	}
}

func deriveTestDEK(t *testing.T, year, month int) []byte {
	t.Helper()
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(year))
	binary.BigEndian.PutUint32(buf[4:], uint32(month))
	h.Write(buf[:])
	return h.Sum(nil)
}

func TestChunkedDecoder_roundTripSingleChunk(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	plaintext := []byte("hello world — single sub-chunk plaintext")
	partition := "billing.usage_events_2026_05"

	enc := encodeChunked(t, dek, plaintext, partition)
	dec := &ChunkedDecoder{DEK: dek}

	var out bytes.Buffer
	if err := dec.DecodeStream(bytes.NewReader(enc), &out, partition); err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	if !bytes.Equal(out.Bytes(), plaintext) {
		t.Errorf("plaintext mismatch")
	}
}

func TestChunkedDecoder_roundTripMultipleChunks(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	// 10 MiB → 3 chunks (4 + 4 + 2)
	plaintext := bytes.Repeat([]byte("ABCD"), (10<<20)/4)
	partition := "billing.usage_events_2026_05"

	enc := encodeChunked(t, dek, plaintext, partition)
	dec := &ChunkedDecoder{DEK: dek}

	var out bytes.Buffer
	if err := dec.DecodeStream(bytes.NewReader(enc), &out, partition); err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	if !bytes.Equal(out.Bytes(), plaintext) {
		t.Errorf("plaintext mismatch len=%d want=%d", out.Len(), len(plaintext))
	}
}

func TestChunkedDecoder_wrongPartitionAAD(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	plaintext := []byte("abcdef")
	enc := encodeChunked(t, dek, plaintext, "billing.usage_events_2026_05")

	dec := &ChunkedDecoder{DEK: dek}
	var out bytes.Buffer
	err := dec.DecodeStream(bytes.NewReader(enc), &out, "billing.usage_events_2026_06")
	if !errors.Is(err, ErrFrameTampered) {
		t.Errorf("err=%v want ErrFrameTampered", err)
	}
}

func TestChunkedDecoder_wrongDEK(t *testing.T) {
	good := deriveTestDEK(t, 2026, 5)
	bad := deriveTestDEK(t, 2026, 6)
	enc := encodeChunked(t, good, []byte("abc"), "p")

	dec := &ChunkedDecoder{DEK: bad}
	var out bytes.Buffer
	err := dec.DecodeStream(bytes.NewReader(enc), &out, "p")
	if !errors.Is(err, ErrFrameTampered) {
		t.Errorf("err=%v want ErrFrameTampered", err)
	}
}

func TestChunkedDecoder_truncatedPayload(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	enc := encodeChunked(t, dek, []byte("abcdefghij"), "p")
	// Truncate by 5 bytes off the tail (mid-payload).
	truncated := enc[:len(enc)-5]

	dec := &ChunkedDecoder{DEK: dek}
	var out bytes.Buffer
	err := dec.DecodeStream(bytes.NewReader(truncated), &out, "p")
	if !errors.Is(err, ErrFrameTampered) {
		t.Errorf("err=%v want ErrFrameTampered", err)
	}
}

func TestChunkedDecoder_truncatedLengthPrefix(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	enc := encodeChunked(t, dek, []byte("abcdefghij"), "p")
	// Append 2 extra bytes — a partial length prefix of the next "frame".
	tampered := append(append([]byte(nil), enc...), 0x00, 0x01)

	dec := &ChunkedDecoder{DEK: dek}
	var out bytes.Buffer
	err := dec.DecodeStream(bytes.NewReader(tampered), &out, "p")
	if !errors.Is(err, ErrFrameTampered) {
		t.Errorf("err=%v want ErrFrameTampered", err)
	}
}

func TestChunkedDecoder_payloadLenTooSmall(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	// payload_len = 0 — smaller than nonce+tag.
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame[:4], 0)
	dec := &ChunkedDecoder{DEK: dek}
	var out bytes.Buffer
	err := dec.DecodeStream(bytes.NewReader(frame), &out, "p")
	if !errors.Is(err, ErrFrameTampered) {
		t.Errorf("err=%v want ErrFrameTampered", err)
	}
}

func TestChunkedDecoder_payloadLenExceedsCap(t *testing.T) {
	dek := deriveTestDEK(t, 2026, 5)
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame[:4], maxFramePayloadBytes+1)
	dec := &ChunkedDecoder{DEK: dek}
	var out bytes.Buffer
	err := dec.DecodeStream(bytes.NewReader(frame), &out, "p")
	if !errors.Is(err, ErrFrameTampered) {
		t.Errorf("err=%v want ErrFrameTampered", err)
	}
}

func TestChunkedDecoder_emptyStream(t *testing.T) {
	dec := &ChunkedDecoder{DEK: deriveTestDEK(t, 2026, 5)}
	var out bytes.Buffer
	if err := dec.DecodeStream(bytes.NewReader(nil), &out, "p"); err != nil {
		t.Errorf("empty stream should be clean EOF, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("plaintext should be empty")
	}
}

func TestChunkedDecoder_invalidDEKLength(t *testing.T) {
	dec := &ChunkedDecoder{DEK: []byte{1, 2, 3}}
	var out bytes.Buffer
	if err := dec.DecodeStream(bytes.NewReader(nil), &out, "p"); err == nil {
		t.Error("expected error for short DEK")
	}
}
