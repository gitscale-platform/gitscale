package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type repositoryReader struct {
	q querier
}

func (r *repositoryReader) GetByID(ctx context.Context, id uuid.UUID) (*store.Repository, error) {
	const q = `
		SELECT id, org_id, name, slug, owner_id, default_branch, visibility,
		       replica_set_id, home_region, created_at, updated_at
		FROM repositories.repositories WHERE id = $1`
	out := &store.Repository{}
	var replicaSet, homeRegion *string
	err := r.q.QueryRow(ctx, q, id).Scan(
		&out.ID, &out.OrgID, &out.Name, &out.Slug, &out.OwnerID,
		&out.DefaultBranch, &out.Visibility,
		&replicaSet, &homeRegion, &out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: GetByID: %w", err)
	}
	if replicaSet != nil {
		out.ReplicaSetID = *replicaSet
	}
	if homeRegion != nil {
		out.HomeRegion = *homeRegion
	}
	return out, nil
}

func (r *repositoryReader) GetBySlug(ctx context.Context, slug string) (*store.Repository, error) {
	const q = `
		SELECT id, org_id, name, slug, owner_id, default_branch, visibility,
		       replica_set_id, home_region, created_at, updated_at
		FROM repositories.repositories WHERE slug = $1
		ORDER BY created_at, id LIMIT 1`
	out := &store.Repository{}
	var replicaSet, homeRegion *string
	err := r.q.QueryRow(ctx, q, slug).Scan(
		&out.ID, &out.OrgID, &out.Name, &out.Slug, &out.OwnerID,
		&out.DefaultBranch, &out.Visibility,
		&replicaSet, &homeRegion, &out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: GetBySlug: %w", err)
	}
	if replicaSet != nil {
		out.ReplicaSetID = *replicaSet
	}
	if homeRegion != nil {
		out.HomeRegion = *homeRegion
	}
	return out, nil
}

// ListByOrg implements keyset pagination on (created_at, id). When both
// afterCreatedAt and afterID are non-nil the WHERE clause uses the row-value
// comparison `(created_at, id) > ($2, $3)`. nil cursors fall through the
// equivalent `IS NULL` branch which returns the first page.
func (r *repositoryReader) ListByOrg(
	ctx context.Context,
	orgID uuid.UUID,
	afterCreatedAt *time.Time,
	afterID *uuid.UUID,
	limit int,
) ([]store.Repository, error) {
	if limit <= 0 {
		return nil, nil
	}
	const q = `
		SELECT id, org_id, name, slug, owner_id, default_branch, visibility,
		       replica_set_id, home_region, created_at, updated_at
		FROM repositories.repositories
		WHERE org_id = $1
		  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3))
		ORDER BY created_at, id
		LIMIT $4`
	rows, err := r.q.Query(ctx, q, orgID, afterCreatedAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: ListByOrg: %w", err)
	}
	defer rows.Close()

	var out []store.Repository
	for rows.Next() {
		var rep store.Repository
		var replicaSet, homeRegion *string
		if err := rows.Scan(
			&rep.ID, &rep.OrgID, &rep.Name, &rep.Slug, &rep.OwnerID,
			&rep.DefaultBranch, &rep.Visibility,
			&replicaSet, &homeRegion, &rep.CreatedAt, &rep.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: ListByOrg scan: %w", err)
		}
		if replicaSet != nil {
			rep.ReplicaSetID = *replicaSet
		}
		if homeRegion != nil {
			rep.HomeRegion = *homeRegion
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}

type repositoryWriter struct {
	repositoryReader
}

// Insert writes a row to repositories.repositories. The caller has already
// generated r.ID and is expected to be inside a Tx; the same Tx must also
// write the outbox row (ADR-008).
func (w *repositoryWriter) Insert(ctx context.Context, r store.Repository) error {
	const q = `
		INSERT INTO repositories.repositories
		  (id, org_id, name, slug, owner_id, default_branch, visibility,
		   replica_set_id, home_region)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	visibility := r.Visibility
	if visibility == "" {
		visibility = "private"
	}
	defaultBranch := r.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	var replicaSet, homeRegion *string
	if r.ReplicaSetID != "" {
		s := r.ReplicaSetID
		replicaSet = &s
	}
	if r.HomeRegion != "" {
		s := r.HomeRegion
		homeRegion = &s
	}
	if _, err := w.q.Exec(ctx, q,
		r.ID, r.OrgID, r.Name, r.Slug, r.OwnerID,
		defaultBranch, visibility, replicaSet, homeRegion,
	); err != nil {
		return fmt.Errorf("postgres: Insert repository: %w", err)
	}
	return nil
}

// UpdatePermissions is intentionally a no-op stub awaiting issue #112-permissions.
// The REST API does not expose this surface in #111.
func (w *repositoryWriter) UpdatePermissions(_ context.Context, _ uuid.UUID, _ string) error {
	return errNotImplemented
}
