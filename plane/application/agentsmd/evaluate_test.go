package agentsmd

import (
	"context"
	"errors"
	"testing"
)

// memFiles is an in-memory FileResolver for tests. Changed map is keyed
// by "old..new"; Size map by "oid:path"; ff map by "old..new".
type memFiles struct {
	changed map[string][]string
	sizes   map[string]int64
	ff      map[string]bool
	err     error
}

func (m *memFiles) Changed(_ context.Context, oldOID, newOID string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.changed[oldOID+".."+newOID], nil
}

func (m *memFiles) Size(_ context.Context, oid, path string) (int64, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.sizes[oid+":"+path], nil
}

func (m *memFiles) IsFastForward(_ context.Context, oldOID, newOID string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	return m.ff[oldOID+".."+newOID], nil
}

func TestEvaluate_NilOrEmptyPolicy(t *testing.T) {
	ctx := context.Background()
	v, err := Evaluate(ctx, nil, EvaluationInput{})
	if err != nil || v != nil {
		t.Fatalf("nil policy: %v %v", err, v)
	}
	v, err = Evaluate(ctx, &Policy{Empty: true}, EvaluationInput{})
	if err != nil || v != nil {
		t.Fatalf("empty policy: %v %v", err, v)
	}
}

func TestEvaluate_DeleteBranchMatches(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "abc123", NewOID: ZeroOID}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(v) != 1 || v[0].Predicate.Name != PredicateDeleteBranch {
		t.Fatalf("expected single delete violation, got %+v", v)
	}
}

func TestEvaluate_DeleteBranchSkipsNonDelete(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "abc", NewOID: "def"}},
	})
	if err != nil || len(v) != 0 {
		t.Fatalf("non-delete must not match: %v %+v", err, v)
	}
}

func TestEvaluate_PushToBranchGlob(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicatePushToBranch, Selector: PredicateSelector{BranchGlob: "release/*"}},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{
			{RefName: "refs/heads/release/v1", OldOID: "a", NewOID: "b"},
			{RefName: "refs/heads/feature/x", OldOID: "a", NewOID: "b"},
		},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(v) != 1 || v[0].RefName != "refs/heads/release/v1" {
		t.Fatalf("expected one match on release/v1, got %+v", v)
	}
}

func TestEvaluate_ForcePushDetection(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateForcePushToBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	files := &memFiles{ff: map[string]bool{
		"old..new":      false, // non-FF -> force push
		"old..ffnew":    true,  // FF -> not a force push
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{
			{RefName: "refs/heads/main", OldOID: "old", NewOID: "new"},
			{RefName: "refs/heads/main", OldOID: "old", NewOID: "ffnew"},
			{RefName: "refs/heads/main", OldOID: ZeroOID, NewOID: "new"}, // creation
		},
		Files: files,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(v) != 1 {
		t.Fatalf("expected 1 force-push violation, got %d: %+v", len(v), v)
	}
}

func TestEvaluate_ForcePushRequiresResolver(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateForcePushToBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	_, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "a", NewOID: "b"}},
	})
	if err == nil {
		t.Fatalf("expected error when FileResolver is nil")
	}
}

func TestEvaluate_ModifyPath(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateModifyPath, Selector: PredicateSelector{PathGlob: "infra/**"}},
	}}
	files := &memFiles{changed: map[string][]string{
		"a..b": {"src/main.go", "infra/terraform/main.tf"},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "a", NewOID: "b"}},
		Files:   files,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(v) != 1 {
		t.Fatalf("expected 1 modify_path violation, got %+v", v)
	}
}

func TestEvaluate_ModifyPath_NoMatch(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateModifyPath, Selector: PredicateSelector{PathGlob: "infra/**"}},
	}}
	files := &memFiles{changed: map[string][]string{
		"a..b": {"src/main.go"},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "a", NewOID: "b"}},
		Files:   files,
	})
	if err != nil || len(v) != 0 {
		t.Fatalf("expected no match: %v %+v", err, v)
	}
}

func TestEvaluate_PushBinaryOverSize(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicatePushBinaryOverSize, Selector: PredicateSelector{MaxBytes: 1024}},
	}}
	files := &memFiles{
		changed: map[string][]string{"a..b": {"big.bin"}},
		sizes:   map[string]int64{"b:big.bin": 5000},
	}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "a", NewOID: "b"}},
		Files:   files,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(v) != 1 {
		t.Fatalf("expected 1 size violation, got %+v", v)
	}
}

func TestEvaluate_FileResolverErrorPropagates(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateModifyPath, Selector: PredicateSelector{PathGlob: "**"}},
	}}
	files := &memFiles{err: errors.New("boom")}
	_, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "a", NewOID: "b"}},
		Files:   files,
	})
	if err == nil {
		t.Fatalf("expected error from resolver")
	}
}

func TestEvaluate_NonBranchRefSkipped(t *testing.T) {
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/tags/v1", OldOID: "abc", NewOID: ZeroOID}},
	})
	if err != nil || len(v) != 0 {
		t.Fatalf("tag delete must not match branch predicate: %v %+v", err, v)
	}
}

func TestEvaluate_LazyFileResolver(t *testing.T) {
	// A policy of only ref-level predicates must not call FileResolver,
	// even if it is nil.
	policy := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	v, err := Evaluate(context.Background(), policy, EvaluationInput{
		Updates: []RefUpdate{{RefName: "refs/heads/main", OldOID: "abc", NewOID: ZeroOID}},
		Files:   nil,
	})
	if err != nil {
		t.Fatalf("nil resolver should be fine for ref-level predicates: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("expected match, got %+v", v)
	}
}

func TestPathMatches_DoubleStar(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"infra/**", "infra/main.tf", true},
		{"infra/**", "infra/terraform/main.tf", true},
		{"infra/**", "src/main.go", false},
		{"infra/**", "infra", true},
		{"**", "anything", true},
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
	}
	for _, c := range cases {
		if got := pathMatches(c.glob, c.path); got != c.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}
