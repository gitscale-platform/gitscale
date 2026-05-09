package repositories

import (
	"time"

	"github.com/google/uuid"
)

// Event-type constants written to repositories.repositories_outbox and
// published on gitscale.repositories.events.
const (
	EventRepositoryCreated = "repositories.repository_created"
)

// envelopeVersion bumps when a payload's field set is no longer
// backwards-compatible.
const envelopeVersion = 1

// RepositoryCreatedPayload is the payload of a repositories.repository_created
// event. Search-indexer (#16-search) and audit-log consumers depend on
// org_id + slug + visibility being present from day one.
type RepositoryCreatedPayload struct {
	RepositoryID    uuid.UUID `json:"repository_id"`
	OrgID           uuid.UUID `json:"org_id"`
	OwnerID         uuid.UUID `json:"owner_id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	DefaultBranch   string    `json:"default_branch"`
	Visibility      string    `json:"visibility"`
	CreatedAt       time.Time `json:"created_at"`
	EnvelopeVersion int       `json:"_envelope_version"`
}

func newRepositoryCreatedPayload(r Repository) RepositoryCreatedPayload {
	return RepositoryCreatedPayload{
		RepositoryID:    r.ID,
		OrgID:           r.OrgID,
		OwnerID:         r.OwnerID,
		Slug:            r.Slug,
		Name:            r.Name,
		DefaultBranch:   r.DefaultBranch,
		Visibility:      r.Visibility,
		CreatedAt:       r.CreatedAt,
		EnvelopeVersion: envelopeVersion,
	}
}
