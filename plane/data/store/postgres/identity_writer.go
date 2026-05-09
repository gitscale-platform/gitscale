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

// DisableUser sets disabled_at = now() and disable_reason = reason. The caller
// must verify the row exists in the same Tx; under SERIALIZABLE this guarantees
// the UPDATE finds it.
func (w *identityWriter) DisableUser(ctx context.Context, userID uuid.UUID, reason string) error {
	const q = `
		UPDATE identity.human_users
		SET disabled_at = now(), disable_reason = $2, updated_at = now()
		WHERE id = $1`
	if _, err := w.q.Exec(ctx, q, userID, reason); err != nil {
		return fmt.Errorf("postgres: DisableUser: %w", err)
	}
	return nil
}

func (w *identityWriter) RevokeAgent(ctx context.Context, agentID uuid.UUID, reason string) error {
	const q = `
		UPDATE identity.agent_identities
		SET revoked_at = now(), revoke_reason = $2, updated_at = now()
		WHERE id = $1`
	if _, err := w.q.Exec(ctx, q, agentID, reason); err != nil {
		return fmt.Errorf("postgres: RevokeAgent: %w", err)
	}
	return nil
}

func (w *identityWriter) UpdateAgentPermissions(ctx context.Context, agentID uuid.UUID, scope []string) error {
	const q = `
		UPDATE identity.agent_identities
		SET permission_scope = $2, updated_at = now()
		WHERE id = $1`
	if scope == nil {
		scope = []string{}
	}
	if _, err := w.q.Exec(ctx, q, agentID, scope); err != nil {
		return fmt.Errorf("postgres: UpdateAgentPermissions: %w", err)
	}
	return nil
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

// InsertCloneToken records a freshly-minted clone token. Lives in the
// identity domain (same Tx as the clone_token_minted outbox event;
// ADR-008). UNIQUE(token) is enforced by the migration so a duplicate
// surfaces as 23505.
func (w *identityWriter) InsertCloneToken(ctx context.Context, ct store.CloneToken) error {
	const q = `
		INSERT INTO identity.clone_tokens
		  (id, token, principal_id, repo_id, expires_at)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := w.q.Exec(ctx, q, ct.ID, ct.Token, ct.PrincipalID, ct.RepoID, ct.ExpiresAt)
	if err != nil {
		return fmt.Errorf("postgres: InsertCloneToken: %w", err)
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
