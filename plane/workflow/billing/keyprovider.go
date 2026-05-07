package billing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

// KeyProvider derives encryption keys for archive files.
// Production wires HashiCorp Vault transit HKDF; the stub uses a deterministic
// derivation safe for tests and local dev.
type KeyProvider interface {
	// GetDEK returns a 32-byte AES-256 key for the given (year, month).
	GetDEK(ctx context.Context, year, month int) ([]byte, error)
}

// stubKeyProvider derives keys deterministically from (year, month).
// Never use in production — keys are predictable.
type stubKeyProvider struct{}

// NewStubKeyProvider returns a deterministic KeyProvider for tests.
func NewStubKeyProvider() KeyProvider { return stubKeyProvider{} }

func (stubKeyProvider) GetDEK(_ context.Context, year, month int) ([]byte, error) {
	// Deterministic: SHA-256(year || month). Predictable but sufficient for tests.
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(year))
	binary.BigEndian.PutUint32(buf[4:], uint32(month))
	h.Write(buf[:])
	return h.Sum(nil), nil
}
