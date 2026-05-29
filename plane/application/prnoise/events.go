package prnoise

import (
	"time"

	"github.com/google/uuid"
)

// EventTypeNoiseDecisionRecorded is the event_type written to
// collaboration.collaboration_outbox when RecordDecision commits a
// decision row. The schema lives at
// plane/data/events/collaboration/pr.noise_decision_recorded.schema.json;
// lint-events validates fixtures against it on every CI run.
const EventTypeNoiseDecisionRecorded = "pr.noise_decision_recorded"

// noiseDecisionEnvelopeVersion is the payload-level version. Bump on
// any non-backwards-compatible change to the field set.
const noiseDecisionEnvelopeVersion = 1

// NoiseDecisionRecordedPayload is the JSON-encoded payload of a
// pr.noise_decision_recorded outbox row. Field names match the schema.
type NoiseDecisionRecordedPayload struct {
	PRID            uuid.UUID    `json:"pr_id"`
	RepoID          uuid.UUID    `json:"repo_id"`
	OrgID           uuid.UUID    `json:"org_id"`
	AgentID         *uuid.UUID   `json:"agent_id,omitempty"`
	DecisionCode    DecisionCode `json:"decision_code"`
	DedupScore      float64      `json:"dedup_score"`
	DuplicateOf     *uuid.UUID   `json:"duplicate_of,omitempty"`
	QualityScore    float64      `json:"quality_score"`
	ReputationScore float64      `json:"reputation_score"`
	CompositeScore  float64      `json:"composite_score"`
	Reason          string       `json:"reason"`
	DecidedAt       time.Time    `json:"decided_at"`
	EnvelopeVersion int          `json:"_envelope_version"`
}

// newNoiseDecisionRecordedPayload converts a Decision into its outbox
// payload. AgentID and DuplicateOf are normalised to nil pointers so
// the JSON-omitempty tags fire (humans / non-duplicates omit the fields).
func newNoiseDecisionRecordedPayload(d Decision) NoiseDecisionRecordedPayload {
	var agentPtr *uuid.UUID
	if d.AgentID != uuid.Nil {
		a := d.AgentID
		agentPtr = &a
	}
	return NoiseDecisionRecordedPayload{
		PRID:            d.PRID,
		RepoID:          d.RepoID,
		OrgID:           d.OrgID,
		AgentID:         agentPtr,
		DecisionCode:    d.Code,
		DedupScore:      d.DedupScore,
		DuplicateOf:     d.DuplicateOf,
		QualityScore:    d.QualityScore,
		ReputationScore: d.ReputationScore,
		CompositeScore:  d.CompositeScore,
		Reason:          d.Reason,
		DecidedAt:       d.DecidedAt,
		EnvelopeVersion: noiseDecisionEnvelopeVersion,
	}
}
