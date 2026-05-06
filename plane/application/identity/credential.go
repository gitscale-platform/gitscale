package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// CredentialHasher hides the credential-hash policy from callers. The default
// impl (Argon2idHasher) follows OWASP's 2026 baseline parameters.
type CredentialHasher interface {
	// Hash returns the encoded argon2id string for plaintext.
	Hash(plaintext string) (hashed string, err error)
	// Verify reports whether plaintext matches hashed. needsRehash is true
	// when hashed was produced with weaker parameters than current defaults.
	Verify(plaintext, hashed string) (ok bool, needsRehash bool)
}

// argon2 parameter constants. OWASP 2026 baseline for argon2id:
//   memory = 64 MiB, iterations = 3, parallelism = 2.
//
// Bumping any of these requires a coordinated update with the verifier so
// the needsRehash flag fires for older hashes.
const (
	argon2MemoryKB    uint32 = 64 * 1024
	argon2Iterations  uint32 = 3
	argon2Parallelism uint8  = 2
	argon2SaltLen     uint32 = 16
	argon2KeyLen      uint32 = 32
)

// Argon2idHasher implements CredentialHasher using argon2id with pinned
// parameters. Use NewArgon2idHasher for production; tests may override
// parameters via the internal constructor for runtime.
type Argon2idHasher struct {
	memoryKB    uint32
	iterations  uint32
	parallelism uint8
	saltLen     uint32
	keyLen      uint32
}

// NewArgon2idHasher returns the production-tuned hasher.
func NewArgon2idHasher() *Argon2idHasher {
	return &Argon2idHasher{
		memoryKB:    argon2MemoryKB,
		iterations:  argon2Iterations,
		parallelism: argon2Parallelism,
		saltLen:     argon2SaltLen,
		keyLen:      argon2KeyLen,
	}
}

// newArgon2idHasherFast returns a hasher with low params suitable for unit
// tests. Not for production.
func newArgon2idHasherFast() *Argon2idHasher {
	return &Argon2idHasher{
		memoryKB:    8 * 1024, // 8 MiB
		iterations:  1,
		parallelism: 1,
		saltLen:     argon2SaltLen,
		keyLen:      argon2KeyLen,
	}
}

// Hash encodes the result as
//   $argon2id$v=19$m=<mem>,t=<iter>,p=<par>$<salt-b64>$<hash-b64>
// matching the format used by the reference argon2 CLI.
func (h *Argon2idHasher) Hash(plaintext string) (string, error) {
	salt := make([]byte, h.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2id: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, h.iterations, h.memoryKB, h.parallelism, h.keyLen)
	encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		h.memoryKB, h.iterations, h.parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return encoded, nil
}

// Verify parses an encoded argon2id hash and constant-time-compares it
// against a fresh hash of plaintext. needsRehash signals that hashed was
// produced with parameters weaker than this hasher's current defaults.
func (h *Argon2idHasher) Verify(plaintext, hashed string) (ok bool, needsRehash bool) {
	parts := strings.Split(hashed, "$")
	// Expected: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, false
	}
	var mem, iter uint32
	var par uint8
	if n, _ := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &par); n != 3 {
		return false, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false
	}
	got := argon2.IDKey([]byte(plaintext), salt, iter, mem, par, uint32(len(want)))
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return false, false
	}
	rehash := mem < h.memoryKB || iter < h.iterations || par < h.parallelism
	return true, rehash
}

// ErrCredentialEmpty is returned by Hash when plaintext is the empty string.
var ErrCredentialEmpty = errors.New("identity: credential plaintext is empty")
