package ci

import (
	"sort"

	"github.com/google/uuid"

	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
)

// PrincipalKind enumerates the identity classes admitted to the CI
// pipeline. Closed sum type — a new value requires explicit code review
// per ADR-002 (hardware boundary against untrusted code).
type PrincipalKind int

// PrincipalKind values. The wire form ("human" / "agent" / "service") is
// produced by the String() method below; the integer form is what the
// workflow input carries to keep the deterministic contract narrow (no
// string-comparison dance at routing time).
const (
	PrincipalUnknown PrincipalKind = iota
	PrincipalHuman
	PrincipalAgent
	PrincipalService
)

// String returns the wire form. Used by the EmitUsageEvent activity to
// stamp the outbox payload's principal_kind field.
func (k PrincipalKind) String() string {
	switch k {
	case PrincipalHuman:
		return "human"
	case PrincipalAgent:
		return "agent"
	case PrincipalService:
		return "service"
	default:
		return "unknown"
	}
}

// IsValid returns true for any non-Unknown variant.
func (k PrincipalKind) IsValid() bool {
	return k == PrincipalHuman || k == PrincipalAgent || k == PrincipalService
}

// Tier enumerates the two CI execution surfaces. Closed sum type per
// spec §"Tier enum"; an `assignTier` switch must remain exhaustive.
type Tier int

// Tier values.
const (
	TierUnknown Tier = iota
	TierHot
	TierCold
)

// String returns the wire form ("hot" / "cold") used in the billing
// outbox payload.
func (t Tier) String() string {
	switch t {
	case TierHot:
		return "hot"
	case TierCold:
		return "cold"
	default:
		return "unknown"
	}
}

// CIJobInput is the input to CIJobWorkflow. Annotations is read by
// explicit key only — never iterated — to preserve the deterministic
// contract. If a future predicate needs to walk all keys, wrap with
// sortedKeys.
type CIJobInput struct {
	JobID         uuid.UUID
	PrincipalID   uuid.UUID
	PrincipalKind PrincipalKind
	OrgID         uuid.UUID
	RepoID        uuid.UUID
	Annotations   map[string]string
	Command       []string
	Env           map[string]string
	Resource      runner.ResourceShape
}

// CIJobOutput is the workflow output.
type CIJobOutput struct {
	Tier     Tier
	VMID     string
	ExitCode int
	Result   runner.JobResult
}

// AnnotationRequireHotPool is the annotation key an agent submits to
// override the cold-pool default. Value must be exactly "true" — any
// other value falls through to the default.
const AnnotationRequireHotPool = "require-hot-pool"

// assignTier is a pure deterministic function of the principal kind and
// the annotations map. Routing rule per spec §"Routing":
//
//	annotations[require-hot-pool] == "true"  → hot
//	principal kind == agent                   → cold
//	otherwise                                 → hot
//
// No I/O. No time. Map access by explicit key only. Replay-safe.
func assignTier(kind PrincipalKind, ann map[string]string) Tier {
	if ann[AnnotationRequireHotPool] == "true" {
		return TierHot
	}
	if kind == PrincipalAgent {
		return TierCold
	}
	return TierHot
}

// sortedKeys returns a sorted snapshot of m's keys. Use this to walk a
// map deterministically inside a workflow body — Go's native map
// iteration order is randomised and would corrupt Temporal replay.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
