package reputation

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/google/uuid"
)

// fakeAgentReader is a tiny in-memory AgentReader. nil keys panic; the
// test suite never asks for an agent it didn't seed.
type fakeAgentReader struct {
	agents map[uuid.UUID]*identity.AgentIdentity
	err    error
}

func (f *fakeAgentReader) GetAgent(_ context.Context, id uuid.UUID) (*identity.AgentIdentity, error) {
	if f.err != nil {
		return nil, f.err
	}
	a, ok := f.agents[id]
	if !ok {
		return nil, nil
	}
	return a, nil
}

func TestRuleBasedReputationScorer_HumanAuthorScoresOne(t *testing.T) {
	t.Parallel()
	s := NewRuleBasedReputationScorer(&fakeAgentReader{})
	got, err := s.Score(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.0 {
		t.Errorf("human author: Score = %v, want 1.0", got)
	}
}

func TestRuleBasedReputationScorer_KnownAgentForwardsScore(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	f := &fakeAgentReader{
		agents: map[uuid.UUID]*identity.AgentIdentity{
			id: {ID: id, ReputationScore: 0.73},
		},
	}
	s := NewRuleBasedReputationScorer(f)
	got, err := s.Score(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0.73 {
		t.Errorf("Score = %v, want 0.73", got)
	}
}

func TestRuleBasedReputationScorer_UnknownAgentReturnsErrAgentNotFound(t *testing.T) {
	t.Parallel()
	s := NewRuleBasedReputationScorer(&fakeAgentReader{agents: map[uuid.UUID]*identity.AgentIdentity{}})
	_, err := s.Score(context.Background(), uuid.New())
	if !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("err = %v, want identity.ErrAgentNotFound", err)
	}
}

func TestRuleBasedReputationScorer_LookupErrorIsWrapped(t *testing.T) {
	t.Parallel()
	want := errors.New("identity offline")
	s := NewRuleBasedReputationScorer(&fakeAgentReader{err: want})
	_, err := s.Score(context.Background(), uuid.New())
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrapped %v", err, want)
	}
}
