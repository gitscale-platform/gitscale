package mcp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MinSessionHMACSecretBytes is the minimum acceptable length of the
// session-signing key. Below this we refuse to construct a server,
// because a 16-byte secret is a 2-billion-attempt offline-brute-force
// surface for an attacker who captures one valid session token.
const MinSessionHMACSecretBytes = 32

// Session is the value carried inside a signed MCP session token. It is
// stateless: the server keeps no in-process map of active sessions
// (ADR-008-adjacent loose-coupling — handlers do not share memory).
type Session struct {
	PrincipalID     uuid.UUID
	ProtocolVersion string
	ExpiresAt       time.Time
}

// MintSession returns a base64url-encoded "<payload>.<sig>" tuple. The
// payload is "<principal_id>|<protocol_version>|<unix_seconds>" and the
// signature is HMAC-SHA-256(secret, payload). Truncating the signature
// would shrink the search space; we keep the full 32 bytes.
func MintSession(secret []byte, s Session) (string, error) {
	if len(secret) < MinSessionHMACSecretBytes {
		return "", fmt.Errorf("mcp: session secret < %d bytes", MinSessionHMACSecretBytes)
	}
	payload := s.PrincipalID.String() + "|" + s.ProtocolVersion + "|" + strconv.FormatInt(s.ExpiresAt.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifySession validates a token minted by MintSession and returns the
// embedded Session on success. Tampered, expired, malformed, and
// wrong-secret tokens all map to a non-nil error; callers translate
// that to CodeUnauthenticated.
func VerifySession(secret []byte, now time.Time, token string) (Session, error) {
	if len(secret) < MinSessionHMACSecretBytes {
		return Session{}, errors.New("mcp: session secret too short")
	}
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return Session{}, errors.New("mcp: malformed session token")
	}
	payloadB64 := token[:dot]
	sigB64 := token[dot+1:]
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Session{}, fmt.Errorf("mcp: decode session payload: %w", err)
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return Session{}, fmt.Errorf("mcp: decode session sig: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	wantSig := mac.Sum(nil)
	if !hmac.Equal(gotSig, wantSig) {
		return Session{}, errors.New("mcp: session signature mismatch")
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 {
		return Session{}, errors.New("mcp: malformed session payload")
	}
	pid, err := uuid.Parse(parts[0])
	if err != nil {
		return Session{}, fmt.Errorf("mcp: session principal id: %w", err)
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Session{}, fmt.Errorf("mcp: session expiry: %w", err)
	}
	expiresAt := time.Unix(exp, 0).UTC()
	if !now.Before(expiresAt) {
		return Session{}, errors.New("mcp: session expired")
	}
	return Session{
		PrincipalID:     pid,
		ProtocolVersion: parts[1],
		ExpiresAt:       expiresAt,
	}, nil
}
