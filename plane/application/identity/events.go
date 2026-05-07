package identity

import (
	"time"

	"github.com/google/uuid"
)

// Event-type constants. These match the event_type column written to
// identity.identity_outbox and the topic gitscale.identity.events.
const (
	EventUserCreated              = "user.created"
	EventAgentCreated             = "agent.created"
	EventAgentReputationUpdated   = "agent.reputation_updated"
	EventUserDisabled             = "user.disabled"
	EventAgentRevoked             = "agent.revoked"
	EventPrincipalPermissionsChanged = "principal.permissions_changed"
	EventOrgMemberAdded           = "org.member_added"
	EventOrgMemberRemoved         = "org.member_removed"
)

// envelopeVersion is the payload-level version distinct from the
// EventEnvelope.version on the Kafka topic. Bumping this signals a
// non-backwards-compatible change to a payload's field set.
const envelopeVersion = 1

// UserCreatedPayload is the payload of a user.created event.
// Metering-ready: rate_bucket and quota_account_id are present from day one
// so the metering plane consumer does not need a schema retrofit (spec D5).
type UserCreatedPayload struct {
	UserID           uuid.UUID  `json:"user_id"`
	Email            string     `json:"email"`
	RateBucket       string     `json:"rate_bucket"`
	QuotaAccountID   *uuid.UUID `json:"quota_account_id,omitempty"`
	PrincipalClass   string     `json:"principal_class"` // always "user"
	CreatedAt        time.Time  `json:"created_at"`
	EnvelopeVersion  int        `json:"_envelope_version"`
}

// AgentCreatedPayload is the payload of an agent.created event.
type AgentCreatedPayload struct {
	AgentID          uuid.UUID  `json:"agent_id"`
	ParentUserID     uuid.UUID  `json:"parent_user_id"`
	DisplayName      string     `json:"display_name"`
	PermissionScope  []string   `json:"permission_scope"`
	RateBucket       string     `json:"rate_bucket"`
	SessionQuota     *int64     `json:"session_quota,omitempty"`
	TokensPerWeekCap *int64     `json:"tokens_per_week_cap,omitempty"`
	ReputationScore  float64    `json:"reputation_score"`
	QuotaAccountID   *uuid.UUID `json:"quota_account_id,omitempty"`
	PrincipalClass   string     `json:"principal_class"` // always "agent"
	CreatedAt        time.Time  `json:"created_at"`
	EnvelopeVersion  int        `json:"_envelope_version"`
}

// AgentReputationUpdatedPayload is the payload of an agent.reputation_updated
// event. delta = new_score - old_score, computed inside the same Tx that
// performs the write.
type AgentReputationUpdatedPayload struct {
	AgentID         uuid.UUID `json:"agent_id"`
	OldScore        float64   `json:"old_score"`
	NewScore        float64   `json:"new_score"`
	Delta           float64   `json:"delta"`
	ComputedAt      time.Time `json:"computed_at"`
	EnvelopeVersion int       `json:"_envelope_version"`
}

func newUserCreatedPayload(u HumanUser) UserCreatedPayload {
	return UserCreatedPayload{
		UserID:          u.ID,
		Email:           u.Email,
		RateBucket:      u.RateBucket,
		QuotaAccountID:  u.QuotaAccountID,
		PrincipalClass:  "user",
		CreatedAt:       time.Now().UTC(),
		EnvelopeVersion: envelopeVersion,
	}
}

func newAgentCreatedPayload(a AgentIdentity) AgentCreatedPayload {
	return AgentCreatedPayload{
		AgentID:          a.ID,
		ParentUserID:     a.ParentUserID,
		DisplayName:      a.DisplayName,
		PermissionScope:  a.PermissionScope,
		RateBucket:       a.RateBucket,
		SessionQuota:     a.SessionQuota,
		TokensPerWeekCap: a.TokensPerWeekCap,
		ReputationScore:  a.ReputationScore,
		QuotaAccountID:   a.QuotaAccountID,
		PrincipalClass:   "agent",
		CreatedAt:        time.Now().UTC(),
		EnvelopeVersion:  envelopeVersion,
	}
}

func newAgentReputationUpdatedPayload(agentID uuid.UUID, oldScore, newScore float64) AgentReputationUpdatedPayload {
	return AgentReputationUpdatedPayload{
		AgentID:         agentID,
		OldScore:        oldScore,
		NewScore:        newScore,
		Delta:           newScore - oldScore,
		ComputedAt:      time.Now().UTC(),
		EnvelopeVersion: envelopeVersion,
	}
}

// UserDisabledPayload is the payload of a user.disabled event.
// affected_principal_ids contains the disabled user's UUID; downstream
// consumers (cache invalidator #27, audit log) treat any principal in that
// list as no-longer-authoritative.
type UserDisabledPayload struct {
	UserID               uuid.UUID   `json:"user_id"`
	Reason               string      `json:"reason"`
	AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	DisabledAt           time.Time   `json:"disabled_at"`
	EnvelopeVersion      int         `json:"_envelope_version"`
}

// AgentRevokedPayload is the payload of an agent.revoked event.
type AgentRevokedPayload struct {
	AgentID              uuid.UUID   `json:"agent_id"`
	ParentUserID         uuid.UUID   `json:"parent_user_id"`
	Reason               string      `json:"reason"`
	AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	RevokedAt            time.Time   `json:"revoked_at"`
	EnvelopeVersion      int         `json:"_envelope_version"`
}

// PrincipalPermissionsChangedPayload is the payload of a
// principal.permissions_changed event. old_scope / new_scope let downstream
// consumers diff without a back-read.
type PrincipalPermissionsChangedPayload struct {
	AgentID              uuid.UUID   `json:"agent_id"`
	OldScope             []string    `json:"old_scope"`
	NewScope             []string    `json:"new_scope"`
	AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	ChangedAt            time.Time   `json:"changed_at"`
	EnvelopeVersion      int         `json:"_envelope_version"`
}

// OrgMemberAddedPayload is the payload of an org.member_added event.
type OrgMemberAddedPayload struct {
	OrgID                uuid.UUID   `json:"org_id"`
	UserID               uuid.UUID   `json:"user_id"`
	Role                 string      `json:"role"`
	AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	AddedAt              time.Time   `json:"added_at"`
	EnvelopeVersion      int         `json:"_envelope_version"`
}

// OrgMemberRemovedPayload is the payload of an org.member_removed event.
type OrgMemberRemovedPayload struct {
	OrgID                uuid.UUID   `json:"org_id"`
	UserID               uuid.UUID   `json:"user_id"`
	AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	RemovedAt            time.Time   `json:"removed_at"`
	EnvelopeVersion      int         `json:"_envelope_version"`
}

func newUserDisabledPayload(userID uuid.UUID, reason string, now time.Time) UserDisabledPayload {
	return UserDisabledPayload{
		UserID:               userID,
		Reason:               reason,
		AffectedPrincipalIDs: []uuid.UUID{userID},
		DisabledAt:           now,
		EnvelopeVersion:      envelopeVersion,
	}
}

func newAgentRevokedPayload(agentID, parentUserID uuid.UUID, reason string, now time.Time) AgentRevokedPayload {
	return AgentRevokedPayload{
		AgentID:              agentID,
		ParentUserID:         parentUserID,
		Reason:               reason,
		AffectedPrincipalIDs: []uuid.UUID{agentID},
		RevokedAt:            now,
		EnvelopeVersion:      envelopeVersion,
	}
}

func newPrincipalPermissionsChangedPayload(agentID uuid.UUID, oldScope, newScope []string, now time.Time) PrincipalPermissionsChangedPayload {
	if oldScope == nil {
		oldScope = []string{}
	}
	if newScope == nil {
		newScope = []string{}
	}
	return PrincipalPermissionsChangedPayload{
		AgentID:              agentID,
		OldScope:             append([]string{}, oldScope...),
		NewScope:             append([]string{}, newScope...),
		AffectedPrincipalIDs: []uuid.UUID{agentID},
		ChangedAt:            now,
		EnvelopeVersion:      envelopeVersion,
	}
}

func newOrgMemberAddedPayload(orgID, userID uuid.UUID, role string, now time.Time) OrgMemberAddedPayload {
	return OrgMemberAddedPayload{
		OrgID:                orgID,
		UserID:               userID,
		Role:                 role,
		AffectedPrincipalIDs: []uuid.UUID{userID, orgID},
		AddedAt:              now,
		EnvelopeVersion:      envelopeVersion,
	}
}

func newOrgMemberRemovedPayload(orgID, userID uuid.UUID, now time.Time) OrgMemberRemovedPayload {
	return OrgMemberRemovedPayload{
		OrgID:                orgID,
		UserID:               userID,
		AffectedPrincipalIDs: []uuid.UUID{userID, orgID},
		RemovedAt:            now,
		EnvelopeVersion:      envelopeVersion,
	}
}
