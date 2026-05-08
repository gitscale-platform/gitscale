package billing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

// DEK is a 32-byte AES-256 data encryption key together with the manifest
// hint required to reconstruct it later. Bytes is sensitive and must not be
// logged. KEKHint is opaque to the caller; restore-side code parses it to
// pick the right derivation path (e.g. Vault key version).
type DEK struct {
	Bytes   []byte
	KEKHint string
}

// KeyProvider derives DEKs for archive files.
// Production wires HashiCorp Vault transit (see VaultKeyProvider, ADR-018);
// the stub uses a deterministic derivation safe for tests and local dev.
type KeyProvider interface {
	GetDEK(ctx context.Context, year, month int) (DEK, error)
}

// stubKeyProvider derives keys deterministically from (year, month).
// Never use in production — keys are predictable.
type stubKeyProvider struct{}

// NewStubKeyProvider returns a deterministic KeyProvider for tests.
func NewStubKeyProvider() KeyProvider { return stubKeyProvider{} }

// stubKEKHint is the constant manifest hint emitted by stubKeyProvider.
const stubKEKHint = "stub-v0"

func (stubKeyProvider) GetDEK(_ context.Context, year, month int) (DEK, error) {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(year))
	binary.BigEndian.PutUint32(buf[4:], uint32(month))
	h.Write(buf[:])
	return DEK{Bytes: h.Sum(nil), KEKHint: stubKEKHint}, nil
}
