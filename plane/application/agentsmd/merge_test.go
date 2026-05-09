package agentsmd

import "testing"

func TestMerge_NilInputs(t *testing.T) {
	got := Merge(nil, nil)
	if got == nil || !got.Empty || len(got.Never) != 0 {
		t.Fatalf("expected empty policy, got %+v", got)
	}
}

func TestMerge_EmptyInputs(t *testing.T) {
	got := Merge(&Policy{Empty: true}, &Policy{Empty: true})
	if !got.Empty {
		t.Fatalf("expected empty policy, got %+v", got)
	}
}

func TestMerge_OrgWinsOnDuplicate(t *testing.T) {
	org := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	repo := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
		{Name: PredicatePushToBranch, Selector: PredicateSelector{BranchGlob: "release/*"}},
	}}
	got := Merge(org, repo)
	if len(got.Never) != 2 {
		t.Fatalf("expected 2 predicates, got %d: %+v", len(got.Never), got.Never)
	}
	if got.Never[0].Name != PredicateDeleteBranch {
		t.Fatalf("org predicate must come first, got %v", got.Never[0])
	}
	if got.Never[1].Name != PredicatePushToBranch {
		t.Fatalf("non-conflicting repo predicate expected second, got %v", got.Never[1])
	}
}

func TestMerge_DifferentSelectorsCoexist(t *testing.T) {
	org := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "main"}},
	}}
	repo := &Policy{Never: []NeverPredicate{
		{Name: PredicateDeleteBranch, Selector: PredicateSelector{BranchGlob: "release/*"}},
	}}
	got := Merge(org, repo)
	if len(got.Never) != 2 {
		t.Fatalf("expected 2 (different selectors), got %d", len(got.Never))
	}
}
