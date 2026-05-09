// Package metering carries the per-RPC metering Counter contract used by the
// proxy and the value type written to the git outbox.
//
// The two-tier counter (TwoTierCounter) implements ADR-015: a best-effort
// Redis enforcement counter and a load-bearing outbox row. The outbox event
// schema lives in plane/data/events/git/git.metering.schema.json — keep this
// type and that schema in lock-step.
package metering

import "time"

// Operation enumerates the Git RPCs that emit metering events. The string
// values are stable and serialised to the outbox payload; do not rename
// without bumping the event schema version.
const (
	OpInfoRefs    = "info_refs"
	OpUploadPack  = "upload_pack"
	OpReceivePack = "receive_pack"
)

// EventType is the value written to git.git_outbox.event_type. It must match
// the file name of the JSON schema under plane/data/events/git/.
const EventType = "git.metering"

// AggregateType is the value written to git.git_outbox.aggregate_type. The
// aggregate is the repository — events for the same repo share an
// aggregate_id so the Kafka partition key (ADR-004) routes them in order.
const AggregateType = "repository"

// MeteringEvent is the JSON payload written to git.git_outbox.payload and
// drained to TopicGitMeteringEvents by the existing outbox consumer.
//
// Field tags match plane/data/events/git/git.metering.schema.json. The
// _envelope_version field is stamped at the kafka envelope layer and is not
// repeated here; the schema validates it on the way out.
//
// AgentID is the empty string for human (non-agent) operations; downstream
// reconciliation distinguishes agent vs human traffic on this field.
type MeteringEvent struct {
	EventID          string    `json:"event_id"`
	AgentID          string    `json:"agent_id"`
	RepoID           string    `json:"repo_id"`
	Operation        string    `json:"operation"`
	BytesTransferred int64     `json:"bytes_transferred"`
	PackObjects      int64     `json:"pack_objects"`
	RefUpdates       int       `json:"ref_updates"`
	OccurredAt       time.Time `json:"occurred_at"`
	EnvelopeVersion  int       `json:"_envelope_version"`
}
