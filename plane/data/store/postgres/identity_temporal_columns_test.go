//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestOrgMembershipsTemporalColumnsBumpOnUpdate asserts that migration 009
// added created_at + updated_at to identity.org_memberships and that an
// UPDATE advances updated_at via the identity.set_updated_at() trigger.
func TestOrgMembershipsTemporalColumnsBumpOnUpdate(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	orgID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.organisations (id, slug) VALUES ($1, 'org-temporal')`,
		orgID,
	); err != nil {
		t.Fatalf("seed identity.organisations: %v", err)
	}

	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.human_users (id, email, credential_hash) VALUES ($1, 'temporal@example.com', 'x')`,
		userID,
	); err != nil {
		t.Fatalf("seed identity.human_users: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.org_memberships (org_id, user_id, role) VALUES ($1, $2, 'developer')`,
		orgID, userID,
	); err != nil {
		t.Fatalf("seed identity.org_memberships: %v", err)
	}

	var createdAt, before time.Time
	if err := pool.QueryRow(ctx,
		`SELECT created_at, updated_at FROM identity.org_memberships WHERE org_id=$1 AND user_id=$2`,
		orgID, userID,
	).Scan(&createdAt, &before); err != nil {
		t.Fatalf("read temporal columns before update: %v", err)
	}
	if createdAt.IsZero() {
		t.Fatalf("created_at was zero; expected default now()")
	}

	// PostgreSQL now() resolves at statement-start; sleep to guarantee a
	// strictly-greater timestamp regardless of clock granularity.
	time.Sleep(10 * time.Millisecond)

	if _, err := pool.Exec(ctx,
		`UPDATE identity.org_memberships SET role='maintainer' WHERE org_id=$1 AND user_id=$2`,
		orgID, userID,
	); err != nil {
		t.Fatalf("update identity.org_memberships: %v", err)
	}

	var after time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM identity.org_memberships WHERE org_id=$1 AND user_id=$2`,
		orgID, userID,
	).Scan(&after); err != nil {
		t.Fatalf("read updated_at after update: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at not bumped: before=%v after=%v", before, after)
	}
}

// TestOAuthAppsUpdatedAtBumpsOnUpdate asserts that migration 009 added
// updated_at to identity.oauth_apps and that an UPDATE advances it via the
// identity.set_updated_at() trigger.
func TestOAuthAppsUpdatedAtBumpsOnUpdate(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	orgID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.organisations (id, slug) VALUES ($1, 'org-oauth')`,
		orgID,
	); err != nil {
		t.Fatalf("seed identity.organisations: %v", err)
	}

	appID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO identity.oauth_apps (id, org_id, name, client_id, client_secret_hash)
		 VALUES ($1, $2, 'app', 'cid-1', 'h')`,
		appID, orgID,
	); err != nil {
		t.Fatalf("seed identity.oauth_apps: %v", err)
	}

	var before time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM identity.oauth_apps WHERE id=$1`, appID,
	).Scan(&before); err != nil {
		t.Fatalf("read updated_at before update: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	if _, err := pool.Exec(ctx,
		`UPDATE identity.oauth_apps SET name='app-2' WHERE id=$1`, appID,
	); err != nil {
		t.Fatalf("update identity.oauth_apps: %v", err)
	}

	var after time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM identity.oauth_apps WHERE id=$1`, appID,
	).Scan(&after); err != nil {
		t.Fatalf("read updated_at after update: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at not bumped: before=%v after=%v", before, after)
	}
}
