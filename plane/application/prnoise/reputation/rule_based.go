package reputation

import (
	"context"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/google/uuid"
)

// AgentReader is the minimum subset of identity.Service that
// RuleBasedReputationScorer needs. Keeping the dependency surface this
// small lets unit tests inject a tiny fake without dragging the full
// identity service into the test binary.
type AgentReader interface {
	GetAgent(ctx context.Context, id uuid.UUID) (*identity.AgentIdentity, error)
}

// RuleBasedReputationScorer implements ReputationScorer by reading
// AgentIdentity.ReputationScore directly from the identity service.
//
// Human authors (uuid.Nil AgentID) score 1.0 — humans are trusted by
// default at this stage; abuse mitigation lives at the edge plane
// (rate-limit, identity revocation), per the spec design decisions.
type RuleBasedReputationScorer struct {
	Identity AgentReader
}

// NewRuleBasedReputationScorer returns a scorer that delegates to id.
func NewRuleBasedReputationScorer(id AgentReader) *RuleBasedReputationScorer {
	return &RuleBasedReputationScorer{Identity: id}
}

// Score returns the agent's stored reputation, or 1.0 for humans.
// identity.ErrAgentNotFound (and other identity errors) propagate
// unchanged — the pipeline must not fall through to a default score
// when the identity lookup fails.
func (s *RuleBasedReputationScorer) Score(ctx context.Context, agentID uuid.UUID) (float64, error) {
	if agentID == uuid.Nil {
		return 1.0, nil
	}
	a, err := s.Identity.GetAgent(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("reputation: get agent: %w", err)
	}
	if a == nil {
		return 0, identity.ErrAgentNotFound
	}
	return a.ReputationScore, nil
}
