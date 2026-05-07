package invalidator

import (
	"encoding/json"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/kafka"
	"github.com/google/uuid"
)

// EventTypes are the identity-domain event types the invalidator consumes.
// Adding a new event_type requires only an entry here; the affected-principals
// extraction shape is shared by every revocation event (#15 spec D6).
const (
	EventUserDisabled                = "user.disabled"
	EventAgentRevoked                = "agent.revoked"
	EventPrincipalPermissionsChanged = "principal.permissions_changed"
	EventOrgMemberRemoved            = "org.member_removed"
	// Reserved for future hard-delete flows; consumer accepts them today so
	// that the day they appear, no consumer redeploy is required.
	EventUserDeleted  = "user.deleted"
	EventAgentDeleted = "agent.deleted"
)

// affectedPrincipalsHandler decodes the affected_principal_ids[] field
// shared by every revocation event payload (#15 spec D6).
type affectedPrincipalsHandler struct{}

func (affectedPrincipalsHandler) Affected(env kafka.EventEnvelope) ([]uuid.UUID, error) {
	var p struct {
		AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, fmt.Errorf("decode affected_principal_ids: %w", err)
	}
	return p.AffectedPrincipalIDs, nil
}

// EventHandler decodes a Kafka envelope and returns the principal UUIDs
// whose cached identity should be invalidated.
type EventHandler interface {
	Affected(env kafka.EventEnvelope) ([]uuid.UUID, error)
}

// DefaultHandlers is the static dispatch table. Unknown event_types fall
// through to a nil lookup so the consumer can record + commit them without
// erroring (forwards-compat with future event types).
var DefaultHandlers = map[string]EventHandler{
	EventUserDisabled:                affectedPrincipalsHandler{},
	EventAgentRevoked:                affectedPrincipalsHandler{},
	EventPrincipalPermissionsChanged: affectedPrincipalsHandler{},
	EventOrgMemberRemoved:            affectedPrincipalsHandler{},
	EventUserDeleted:                 affectedPrincipalsHandler{},
	EventAgentDeleted:                affectedPrincipalsHandler{},
}
