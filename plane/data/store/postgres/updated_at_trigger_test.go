//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUpdatedAtTriggerBumpsOnUpdate asserts that migration 008 attaches
// a BEFORE UPDATE trigger to every covered table, advancing updated_at
// on a no-op UPDATE. One subtest per table; each seeds its own row(s).
func TestUpdatedAtTriggerBumpsOnUpdate(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	// quota account is needed both for its own subtest and as a parent for
	// billing.invoices. Seed once outside the table-list and reference it.
	quotaAccountID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO billing.quota_accounts (id, org_id) VALUES ($1, $2)`,
		quotaAccountID, uuid.New(),
	); err != nil {
		t.Fatalf("seed billing.quota_accounts: %v", err)
	}

	// repositories.repositories needs an org row by FK convention (soft ref,
	// no FK), but pull_requests/issues are also soft refs on repo_id. Seed
	// a repo row to anchor PR/issue subtests.
	repoID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO repositories.repositories (id, org_id, name, slug, owner_id)
		 VALUES ($1, $2, 'trg-repo', 'trg-repo', $3)`,
		repoID, uuid.New(), uuid.New(),
	); err != nil {
		t.Fatalf("seed repositories.repositories anchor: %v", err)
	}

	type triggerCase struct {
		name      string             // schema.table for fully-qualified SELECT
		idCol     string             // column used to look up the row
		seed      func() (any, error) // returns the id of the seeded row
		updateSQL string             // takes the id as $1
	}

	cases := []triggerCase{
		{
			name:  "identity.organisations",
			idCol: "id",
			seed: func() (any, error) {
				id := uuid.New()
				_, err := pool.Exec(ctx,
					`INSERT INTO identity.organisations (id, slug) VALUES ($1, 'trg-org')`,
					id,
				)
				return id, err
			},
			updateSQL: `UPDATE identity.organisations SET display_name='renamed' WHERE id=$1`,
		},
		{
			name:  "repositories.repositories",
			idCol: "id",
			seed: func() (any, error) {
				id := uuid.New()
				_, err := pool.Exec(ctx,
					`INSERT INTO repositories.repositories (id, org_id, name, slug, owner_id)
					 VALUES ($1, $2, 'trg-r', 'trg-r-`+uuid.NewString()[:8]+`', $3)`,
					id, uuid.New(), uuid.New(),
				)
				return id, err
			},
			updateSQL: `UPDATE repositories.repositories SET default_branch='trunk' WHERE id=$1`,
		},
		{
			name:  "collaboration.pull_requests",
			idCol: "id",
			seed: func() (any, error) {
				id := uuid.New()
				_, err := pool.Exec(ctx,
					`INSERT INTO collaboration.pull_requests
					 (id, repo_id, number, title, author_id, author_type, base_branch, head_branch)
					 VALUES ($1, $2, 1, 'trg-pr', $3, 'human', 'main', 'feat')`,
					id, repoID, uuid.New(),
				)
				return id, err
			},
			updateSQL: `UPDATE collaboration.pull_requests SET title='trg-pr-2' WHERE id=$1`,
		},
		{
			name:  "collaboration.issues",
			idCol: "id",
			seed: func() (any, error) {
				id := uuid.New()
				_, err := pool.Exec(ctx,
					`INSERT INTO collaboration.issues
					 (id, repo_id, number, title, author_id, author_type)
					 VALUES ($1, $2, 1, 'trg-iss', $3, 'human')`,
					id, repoID, uuid.New(),
				)
				return id, err
			},
			updateSQL: `UPDATE collaboration.issues SET title='trg-iss-2' WHERE id=$1`,
		},
		{
			name:  "collaboration.comments",
			idCol: "id",
			seed: func() (any, error) {
				id := uuid.New()
				_, err := pool.Exec(ctx,
					`INSERT INTO collaboration.comments
					 (id, parent_type, parent_id, author_id, author_type, body)
					 VALUES ($1, 'pr', $2, $3, 'human', 'first')`,
					id, uuid.New(), uuid.New(),
				)
				return id, err
			},
			updateSQL: `UPDATE collaboration.comments SET body='edited' WHERE id=$1`,
		},
		{
			name:  "billing.quota_accounts",
			idCol: "id",
			seed: func() (any, error) {
				// Reuse the pre-seeded quota account; the row already exists.
				return quotaAccountID, nil
			},
			updateSQL: `UPDATE billing.quota_accounts SET plan_tier='pro' WHERE id=$1`,
		},
		{
			name:  "billing.invoices",
			idCol: "id",
			seed: func() (any, error) {
				id := uuid.New()
				_, err := pool.Exec(ctx,
					`INSERT INTO billing.invoices
					 (id, account_id, period_start, period_end)
					 VALUES ($1, $2, '2026-05-01T00:00:00Z', '2026-06-01T00:00:00Z')`,
					id, quotaAccountID,
				)
				return id, err
			},
			updateSQL: `UPDATE billing.invoices SET status='finalized' WHERE id=$1`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			id, err := tc.seed()
			if err != nil {
				t.Fatalf("seed %s: %v", tc.name, err)
			}

			selectSQL := `SELECT updated_at FROM ` + tc.name + ` WHERE ` + tc.idCol + `=$1`

			var before time.Time
			if err := pool.QueryRow(ctx, selectSQL, id).Scan(&before); err != nil {
				t.Fatalf("read updated_at before update: %v", err)
			}

			// PostgreSQL now() resolves to statement-start with microsecond
			// precision; sleep long enough to guarantee a strictly-greater
			// timestamp regardless of clock granularity.
			time.Sleep(10 * time.Millisecond)

			if _, err := pool.Exec(ctx, tc.updateSQL, id); err != nil {
				t.Fatalf("update %s: %v", tc.name, err)
			}

			var after time.Time
			if err := pool.QueryRow(ctx, selectSQL, id).Scan(&after); err != nil {
				t.Fatalf("read updated_at after update: %v", err)
			}

			if !after.After(before) {
				t.Fatalf("%s: updated_at not bumped (before=%v after=%v)", tc.name, before, after)
			}
		})
	}
}
