package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// cloneTokenSecretBytes is the raw entropy length of a minted token.
// 32 bytes = 256 bits, encoded base64-url for transport safety.
const cloneTokenSecretBytes = 32

// generateCloneTokenSecret returns a fresh, opaque, URL-safe token.
// Variable, not constant, so tests can substitute a deterministic source
// — but in practice both prod and stub call this directly.
func generateCloneTokenSecret() (string, error) {
	buf := make([]byte, cloneTokenSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("identity: clone-token entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// mintCloneTokenInTx is the shared single-Tx mint procedure used by both
// the postgres- and stub-backed services. The caller supplies the
// transactional writer; we pre-generate ID/Token/ExpiresAt outside the
// retry loop (NOT for security — secret is rolled back on error).
//
// ADR-008: the source row + the outbox row are written in the same Tx.
// The acknowledgement to the caller happens on Commit.
func mintCloneTokenInTx(
	ctx context.Context,
	tx store.Tx,
	now time.Time,
	principalID, repoID uuid.UUID,
	secret string,
) (CloneToken, error) {
	tokenID := uuid.New()
	expiresAt := now.Add(CloneTokenTTL)
	row := store.CloneToken{
		ID:          tokenID,
		Token:       secret,
		PrincipalID: principalID,
		RepoID:      repoID,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}
	if err := tx.Identity().InsertCloneToken(ctx, row); err != nil {
		return CloneToken{}, err
	}
	if err := tx.WriteOutbox(ctx, store.DomainIdentity, "clone_token", tokenID,
		EventCloneTokenMinted, newCloneTokenMintedPayload(tokenID, principalID, repoID, expiresAt, now)); err != nil {
		return CloneToken{}, err
	}
	return CloneToken{
		TokenID:     tokenID,
		Token:       secret,
		PrincipalID: principalID,
		RepoID:      repoID,
		ExpiresAt:   expiresAt,
	}, nil
}
