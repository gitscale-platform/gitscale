package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testSecret() []byte {
	// 32 bytes; deterministic for round-trip tests.
	return []byte("01234567890123456789012345678901")
}

func TestSession_RoundTrip(t *testing.T) {
	s := Session{
		PrincipalID:     uuid.New(),
		ProtocolVersion: "2025-06-18",
		ExpiresAt:       time.Now().Add(time.Hour).UTC(),
	}
	tok, err := MintSession(testSecret(), s)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	got, err := VerifySession(testSecret(), time.Now(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.PrincipalID != s.PrincipalID {
		t.Errorf("principal: got %s want %s", got.PrincipalID, s.PrincipalID)
	}
	if got.ProtocolVersion != s.ProtocolVersion {
		t.Errorf("version: got %q want %q", got.ProtocolVersion, s.ProtocolVersion)
	}
}

func TestSession_Expired(t *testing.T) {
	s := Session{
		PrincipalID:     uuid.New(),
		ProtocolVersion: "v",
		ExpiresAt:       time.Now().Add(-time.Minute).UTC(),
	}
	tok, _ := MintSession(testSecret(), s)
	if _, err := VerifySession(testSecret(), time.Now(), tok); err == nil {
		t.Fatal("expected error for expired session")
	}
}

func TestSession_TamperedSig(t *testing.T) {
	s := Session{PrincipalID: uuid.New(), ProtocolVersion: "v", ExpiresAt: time.Now().Add(time.Hour)}
	tok, _ := MintSession(testSecret(), s)
	// Flip the last character.
	flipped := tok[:len(tok)-1] + flip(tok[len(tok)-1])
	if _, err := VerifySession(testSecret(), time.Now(), flipped); err == nil {
		t.Fatal("expected error on tampered signature")
	}
}

func TestSession_WrongSecret(t *testing.T) {
	s := Session{PrincipalID: uuid.New(), ProtocolVersion: "v", ExpiresAt: time.Now().Add(time.Hour)}
	tok, _ := MintSession(testSecret(), s)
	other := []byte(strings.Repeat("x", 32))
	if _, err := VerifySession(other, time.Now(), tok); err == nil {
		t.Fatal("expected error with wrong secret")
	}
}

func TestSession_ShortSecret(t *testing.T) {
	if _, err := MintSession([]byte("short"), Session{}); err == nil {
		t.Fatal("expected error for short secret")
	}
}

func TestSession_Malformed(t *testing.T) {
	if _, err := VerifySession(testSecret(), time.Now(), "not-a-token"); err == nil {
		t.Fatal("expected error on malformed token")
	}
}

func flip(b byte) string {
	// Toggle bit 0 to produce a different valid base64-url char.
	if b == 'A' {
		return "B"
	}
	return "A"
}
