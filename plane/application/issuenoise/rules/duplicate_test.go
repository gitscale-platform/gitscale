package rules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type stubSearcher struct {
	cands []DuplicateCandidate
	err   error
}

func (s stubSearcher) FindCandidates(_ context.Context, _ uuid.UUID, _ string, _ int) ([]DuplicateCandidate, error) {
	return s.cands, s.err
}

func TestDuplicate(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("real bug body content ", 5)
	parent := uuid.New()

	t.Run("nil_searcher", func(t *testing.T) {
		r, err := Duplicate(nil)(ctx, Input{Body: body})
		if err != nil || r.Signal.Weight != 0 {
			t.Fatalf("expected no fire, no err; got %v %+v", err, r)
		}
	})

	t.Run("short_body_skipped", func(t *testing.T) {
		s := stubSearcher{cands: []DuplicateCandidate{{IssueID: parent, Score: 0.99}}}
		r, err := Duplicate(s)(ctx, Input{Body: "tiny"})
		if err != nil || r.Signal.Weight != 0 {
			t.Fatalf("short body must not fire")
		}
	})

	t.Run("no_candidates", func(t *testing.T) {
		s := stubSearcher{cands: nil}
		r, err := Duplicate(s)(ctx, Input{Body: body})
		if err != nil || r.Signal.Weight != 0 {
			t.Fatalf("expected no fire")
		}
	})

	t.Run("top_candidate_used", func(t *testing.T) {
		s := stubSearcher{cands: []DuplicateCandidate{{IssueID: parent, Score: 0.93}}}
		r, err := Duplicate(s)(ctx, Input{Body: body})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if r.Signal.Weight != 0.93 || r.Category != CategoryDuplicate {
			t.Fatalf("got %+v", r)
		}
		if r.DuplicateOf == nil || *r.DuplicateOf != parent {
			t.Fatalf("DuplicateOf=%v want %v", r.DuplicateOf, parent)
		}
	})

	t.Run("self_skipped", func(t *testing.T) {
		me := uuid.New()
		s := stubSearcher{cands: []DuplicateCandidate{{IssueID: me, Score: 0.99}, {IssueID: parent, Score: 0.91}}}
		r, err := Duplicate(s)(ctx, Input{IssueID: me, Body: body})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if r.DuplicateOf == nil || *r.DuplicateOf != parent {
			t.Fatalf("expected fallback to second candidate; got %+v", r)
		}
	})

	t.Run("self_only_no_fire", func(t *testing.T) {
		me := uuid.New()
		s := stubSearcher{cands: []DuplicateCandidate{{IssueID: me, Score: 0.99}}}
		r, err := Duplicate(s)(ctx, Input{IssueID: me, Body: body})
		if err != nil || r.Signal.Weight != 0 {
			t.Fatalf("expected no fire")
		}
	})

	t.Run("error_propagates", func(t *testing.T) {
		s := stubSearcher{err: errors.New("vespa down")}
		_, err := Duplicate(s)(ctx, Input{Body: body})
		if err == nil {
			t.Fatalf("expected error to propagate")
		}
	})
}
