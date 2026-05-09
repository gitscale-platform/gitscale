package restapi

import (
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/google/uuid"
)

func TestEncodeDecodeCursor_roundTrip(t *testing.T) {
	c := repositories.Cursor{
		AfterID:        uuid.New(),
		AfterCreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	s := EncodeCursor(c)
	if s == "" {
		t.Fatal("encoded empty for non-zero cursor")
	}
	got, err := DecodeCursor(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AfterID != c.AfterID || !got.AfterCreatedAt.Equal(c.AfterCreatedAt) {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, c)
	}
}

func TestEncodeCursor_zeroIsEmpty(t *testing.T) {
	if got := EncodeCursor(repositories.Cursor{}); got != "" {
		t.Errorf("zero cursor encoded to %q, want empty", got)
	}
}

func TestDecodeCursor_emptyIsZero(t *testing.T) {
	got, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero cursor, got %+v", got)
	}
}

func TestDecodeCursor_malformed(t *testing.T) {
	if _, err := DecodeCursor("not-base64!"); err == nil {
		t.Error("expected error on malformed base64")
	}
	if _, err := DecodeCursor("YWJjZA"); err == nil { // valid base64, not JSON
		t.Error("expected error on non-JSON payload")
	}
}
