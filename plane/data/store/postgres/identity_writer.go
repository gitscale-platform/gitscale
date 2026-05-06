package postgres

import (
	"context"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// identityWriter embeds identityReader (providing read methods) and adds
// write operations. It must only be used within a transaction.
type identityWriter struct {
	identityReader
}

func (w *identityWriter) InsertHumanUser(ctx context.Context, u store.HumanUser) error {
	const q = `
		INSERT INTO identity.human_users
		  (id, email, credential_hash, rate_bucket, quota_account_id)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := w.q.Exec(ctx, q,
		u.ID, u.Email, u.CredentialHash, u.RateBucket, u.QuotaAccountID,
	)
	if err != nil {
		return fmt.Errorf("postgres: InsertHumanUser: %w", err)
	}
	return nil
}

func (w *identityWriter) InsertAgentIdentity(ctx context.Context, a store.AgentIdentity) error {
	const q = `
		INSERT INTO identity.agent_identities
		  (id, display_name, parent_user_id, permission_scope, rate_bucket,
		   session_quota, tokens_per_week_cap, reputation_score, quota_account_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	_, err := w.q.Exec(ctx, q,
		a.ID, a.DisplayName, a.ParentUserID, a.PermissionScope, a.RateBucket,
		a.SessionQuota, a.TokensPerWeekCap, a.ReputationScore, a.QuotaAccountID,
	)
	if err != nil {
		return fmt.Errorf("postgres: InsertAgentIdentity: %w", err)
	}
	return nil
}

func (w *identityWriter) SetAgentReputationScore(ctx context.Context, agentID uuid.UUID, score float64) error {
	const q = `
		UPDATE identity.agent_identities SET reputation_score = $2, updated_at = now()
		WHERE id = $1`
	_, err := w.q.Exec(ctx, q, agentID, score)
	if err != nil {
		return fmt.Errorf("postgres: SetAgentReputationScore: %w", err)
	}
	return nil
}

func (w *identityWriter) DisableUser(_ context.Context, _ uuid.UUID) error {
	return errNotImplemented
}

func (w *identityWriter) RevokeAgent(_ context.Context, _ uuid.UUID) error {
	return errNotImplemented
}

func (w *identityWriter) UpdateAgentPermissions(_ context.Context, _ uuid.UUID, _ []string) error {
	return errNotImplemented
}

func (w *identityWriter) AddOrgMember(ctx context.Context, m store.OrgMembership) error {
	const q = `
		INSERT INTO identity.org_memberships (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role`
	_, err := w.q.Exec(ctx, q, m.OrgID, m.UserID, m.Role)
	if err != nil {
		return fmt.Errorf("postgres: AddOrgMember: %w", err)
	}
	return nil
}

func (w *identityWriter) RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `DELETE FROM identity.org_memberships WHERE org_id = $1 AND user_id = $2`
	_, err := w.q.Exec(ctx, q, orgID, userID)
	if err != nil {
		return fmt.Errorf("postgres: RemoveOrgMember: %w", err)
	}
	return nil
}
