package policy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// AuditEventKind enumerates the categories of audit events. Strings are the
// canonical wire encoding and match the values stored in
// application.policy_audit.event_kind.
type AuditEventKind string

const (
	AuditEventSubmitted          AuditEventKind = "submitted"
	AuditEventApproved           AuditEventKind = "approved"
	AuditEventRejected           AuditEventKind = "rejected"
	AuditEventEscalated          AuditEventKind = "escalated"
	AuditEventExpired            AuditEventKind = "expired"
	AuditEventAutoApprovedNoRule AuditEventKind = "auto_approved_no_rule"
	AuditEventAutoDenied         AuditEventKind = "auto_denied"
)

// IsValid reports whether k is one of the closed enum values.
func (k AuditEventKind) IsValid() bool {
	switch k {
	case AuditEventSubmitted, AuditEventApproved, AuditEventRejected,
		AuditEventEscalated, AuditEventExpired,
		AuditEventAutoApprovedNoRule, AuditEventAutoDenied:
		return true
	}
	return false
}

// ActorKind enumerates the principal types that can author an audit event.
type ActorKind string

const (
	ActorKindHuman   ActorKind = "human"
	ActorKindAgent   ActorKind = "agent"
	ActorKindService ActorKind = "service"
	ActorKindSystem  ActorKind = "system" // for system-emitted events (no actor_id)
)

// IsValid reports whether k is one of the closed enum values.
func (k ActorKind) IsValid() bool {
	switch k {
	case ActorKindHuman, ActorKindAgent, ActorKindService, ActorKindSystem:
		return true
	}
	return false
}

// AuditRow is one row of the per-policy Merkle-chained audit log. Genesis
// row carries PrevHash = 32 zero bytes; subsequent rows carry the previous
// row's RowHash. Tampering with any payload field invalidates RowHash on
// re-computation, which the verification routine surfaces.
type AuditRow struct {
	ID         int64           // bigserial; assigned by the DB
	PolicyID   uuid.UUID
	PlanID     *uuid.UUID
	EventKind  AuditEventKind
	ActorID    *uuid.UUID      // nil for ActorKindSystem
	ActorKind  ActorKind
	Payload    json.RawMessage // canonicalised JSON payload
	PrevHash   [32]byte
	RowHash    [32]byte
	CreatedAt  time.Time
}

// GenesisHash is the conventional 32-byte zero PrevHash for the first row
// of a policy's audit chain. Stored explicitly rather than implicitly so
// off-line replays can detect a missing first row.
var GenesisHash = [32]byte{}

// CanonicalPayload re-emits payload as canonical JSON: top-level object
// keys sorted, no whitespace. Required for stable hashing across writers
// that may serialise maps in different orders. Returns the bytes plus an
// error if the payload is not a valid JSON object or array.
func CanonicalPayload(payload json.RawMessage) ([]byte, error) {
	if len(payload) == 0 {
		return []byte("null"), nil
	}
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return nil, fmt.Errorf("policy: audit payload not valid JSON: %w", err)
	}
	return canonicalize(v)
}

// canonicalize emits any JSON value with sorted object keys and no
// whitespace. Recursive on maps and slices.
func canonicalize(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf []byte
		buf = append(buf, '{')
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kj, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf = append(buf, kj...)
			buf = append(buf, ':')
			vj, err := canonicalize(t[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, vj...)
		}
		buf = append(buf, '}')
		return buf, nil
	case []any:
		var buf []byte
		buf = append(buf, '[')
		for i, e := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			ej, err := canonicalize(e)
			if err != nil {
				return nil, err
			}
			buf = append(buf, ej...)
		}
		buf = append(buf, ']')
		return buf, nil
	default:
		// Scalars (string, float64, bool, nil) — json.Marshal already
		// emits canonical form.
		return json.Marshal(v)
	}
}

// ComputeRowHash returns the Merkle hash of a row given its predecessor
// hash. The hash covers (prev_hash || canonical(payload) || actor_kind ||
// event_kind || actor_id_or_zero || created_at_unix_nano). Order is fixed;
// adding fields requires a chain version bump and migration.
func ComputeRowHash(prev [32]byte, r AuditRow) ([32]byte, error) {
	canon, err := CanonicalPayload(r.Payload)
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	_, _ = h.Write(prev[:])
	_, _ = h.Write(canon)
	_, _ = h.Write([]byte(r.ActorKind))
	_, _ = h.Write([]byte{0x1f}) // unit separator to avoid kind/event_kind collision
	_, _ = h.Write([]byte(r.EventKind))
	_, _ = h.Write([]byte{0x1f})
	if r.ActorID != nil {
		id := *r.ActorID
		_, _ = h.Write(id[:])
	} else {
		var zero [16]byte
		_, _ = h.Write(zero[:])
	}
	var ts [8]byte
	nano := r.CreatedAt.UTC().UnixNano()
	for i := 0; i < 8; i++ {
		ts[7-i] = byte(nano >> (8 * i))
	}
	_, _ = h.Write(ts[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// VerifyChain replays rows in slice order and reports the first row whose
// stored RowHash does not match the recomputed value, or whose PrevHash
// does not match the predecessor's RowHash. Returns (-1, nil) on success.
//
// rows must be ordered by ascending ID. The first row's PrevHash MUST equal
// GenesisHash; otherwise the genesis violation is surfaced as breakIndex=0.
func VerifyChain(rows []AuditRow) (int, error) {
	prev := GenesisHash
	for i, r := range rows {
		if r.PrevHash != prev {
			return i, fmt.Errorf("policy: row %d prev_hash mismatch", i)
		}
		got, err := ComputeRowHash(prev, r)
		if err != nil {
			return i, err
		}
		if got != r.RowHash {
			return i, fmt.Errorf("policy: row %d row_hash mismatch (tampering detected)", i)
		}
		prev = got
	}
	return -1, nil
}

// AppendRow stamps PrevHash and RowHash on r in place using the supplied
// predecessor hash. Callers persist r inside a Tx that has already taken
// the per-policy SELECT … FOR UPDATE on the latest row to serialise
// concurrent appends (avoids hash forks).
func AppendRow(prev [32]byte, r *AuditRow) error {
	r.PrevHash = prev
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	hash, err := ComputeRowHash(prev, *r)
	if err != nil {
		return err
	}
	r.RowHash = hash
	return nil
}
