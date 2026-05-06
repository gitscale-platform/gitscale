package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type identityReader struct {
	q querier
}

func (r *identityReader) GetUserByID(ctx context.Context, id uuid.UUID) (*store.HumanUser, error) {
	const q = `
		SELECT id, email, credential_hash, rate_bucket, quota_account_id, created_at, updated_at
		FROM identity.human_users WHERE id = $1`
	u := &store.HumanUser{}
	err := r.q.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.Email, &u.CredentialHash, &u.RateBucket,
		&u.QuotaAccountID, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: GetUserByID: %w", err)
	}
	return u, nil
}

func (r *identityReader) GetUserByEmail(ctx context.Context, email string) (*store.HumanUser, error) {
	const q = `
		SELECT id, email, credential_hash, rate_bucket, quota_account_id, created_at, updated_at
		FROM identity.human_users WHERE email = $1`
	u := &store.HumanUser{}
	err := r.q.QueryRow(ctx, q, email).Scan(
		&u.ID, &u.Email, &u.CredentialHash, &u.RateBucket,
		&u.QuotaAccountID, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: GetUserByEmail: %w", err)
	}
	return u, nil
}

func (r *identityReader) GetAgentByID(ctx context.Context, id uuid.UUID) (*store.AgentIdentity, error) {
	const q = `
		SELECT id, display_name, parent_user_id, permission_scope, rate_bucket,
		       session_quota, tokens_per_week_cap, reputation_score, quota_account_id,
		       created_at, updated_at
		FROM identity.agent_identities WHERE id = $1`
	a := &store.AgentIdentity{}
	err := r.q.QueryRow(ctx, q, id).Scan(
		&a.ID, &a.DisplayName, &a.ParentUserID, &a.PermissionScope, &a.RateBucket,
		&a.SessionQuota, &a.TokensPerWeekCap, &a.ReputationScore, &a.QuotaAccountID,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: GetAgentByID: %w", err)
	}
	return a, nil
}

func (r *identityReader) GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]store.AgentIdentity, error) {
	const q = `
		SELECT id, display_name, parent_user_id, permission_scope, rate_bucket,
		       session_quota, tokens_per_week_cap, reputation_score, quota_account_id,
		       created_at, updated_at
		FROM identity.agent_identities WHERE parent_user_id = $1
		ORDER BY created_at`
	rows, err := r.q.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: GetAgentsByParentUser: %w", err)
	}
	defer rows.Close()

	var agents []store.AgentIdentity
	for rows.Next() {
		var a store.AgentIdentity
		if err := rows.Scan(
			&a.ID, &a.DisplayName, &a.ParentUserID, &a.PermissionScope, &a.RateBucket,
			&a.SessionQuota, &a.TokensPerWeekCap, &a.ReputationScore, &a.QuotaAccountID,
			&a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: GetAgentsByParentUser scan: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// LookupIdentityForCache returns the minimal projection used by the edge-plane
// identity cache. It tries human_users first, then agent_identities.
func (r *identityReader) LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*store.IdentityCacheEntry, error) {
	const humanQ = `
		SELECT id, rate_bucket, quota_account_id FROM identity.human_users WHERE id = $1`
	entry := &store.IdentityCacheEntry{}
	err := r.q.QueryRow(ctx, humanQ, principalID).Scan(&entry.PrincipalID, &entry.RateBucket, &entry.QuotaID)
	if err == nil {
		entry.Kind = "human"
		return entry, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("postgres: LookupIdentityForCache (human): %w", err)
	}

	const agentQ = `
		SELECT id, rate_bucket, quota_account_id FROM identity.agent_identities WHERE id = $1`
	err = r.q.QueryRow(ctx, agentQ, principalID).Scan(&entry.PrincipalID, &entry.RateBucket, &entry.QuotaID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: LookupIdentityForCache (agent): %w", err)
	}
	entry.Kind = "agent"
	return entry, nil
}
