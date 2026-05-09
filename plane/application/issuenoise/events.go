package issuenoise

import (
	"time"

	"github.com/google/uuid"
)

// Outbox event_type constants. Both events are written to the
// collaboration domain outbox table (the issue is a collaboration
// aggregate). They must have schema files at
// plane/data/events/collaboration/<event_type>.schema.json so
// `make lint-events` finds them.
const (
	// EventTypeRoutingDecided is emitted on every Router.Route
	// decision, regardless of verdict. Consumers that need to act on
	// "an issue was routed" subscribe to this.
	EventTypeRoutingDecided = "issue_noise.routing_decided"

	// EventTypeReleased is emitted when a maintainer manually
	// releases a held issue via Router.Release.
	EventTypeReleased = "issue_noise.released"

	// AggregateTypeIssue is the aggregate_type value on outbox rows
	// for both events. Stable.
	AggregateTypeIssue = "issue"
)

// RoutingDecidedPayload is the structured payload of an
// issue_noise.routing_decided outbox row. JSON tags are stable; adding
// a new optional field is non-breaking, removing or renaming any of
// the listed fields is breaking and requires a schema_version bump
// on the envelope.
type RoutingDecidedPayload struct {
	IssueID       uuid.UUID  `json:"issue_id"`
	RepoID        uuid.UUID  `json:"repo_id"`
	ReporterID    uuid.UUID  `json:"reporter_id"`
	Verdict       string     `json:"verdict"`
	ScorerVersion string     `json:"scorer_version"`
	ScoreSpam     float64    `json:"score_spam"`
	ScoreLQ       float64    `json:"score_low_quality"`
	ScoreDup      float64    `json:"score_duplicate"`
	DuplicateOf   *uuid.UUID `json:"duplicate_of,omitempty"`
	Signals       []Signal   `json:"signals"`
	DecidedAt     time.Time  `json:"decided_at"`
	DecidedBy     string     `json:"decided_by"` // "auto" | "maintainer:<id>"
	Enforced      bool       `json:"enforced"`   // false during dark-launch
	EnvelopeV     int        `json:"_envelope_version"`
}

// ReleasedPayload is the structured payload of an
// issue_noise.released outbox row. Recorded when a maintainer
// manually releases a held issue.
type ReleasedPayload struct {
	IssueID      uuid.UUID `json:"issue_id"`
	RepoID       uuid.UUID `json:"repo_id"`
	MaintainerID uuid.UUID `json:"maintainer_id"`
	ReleasedAt   time.Time `json:"released_at"`
	EnvelopeV    int       `json:"_envelope_version"`
}

// EnvelopeVersion is the payload-side schema version. Bump on a
// breaking payload change; consumers gate on this.
const EnvelopeVersion = 1
