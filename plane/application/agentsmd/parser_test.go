package agentsmd

import (
	"strings"
	"testing"
)

func TestParse_EmptyInput(t *testing.T) {
	cases := [][]byte{nil, {}, []byte("   \n  \t\n")}
	for _, c := range cases {
		p, diags, err := Parse(c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !p.Empty {
			t.Fatalf("expected empty policy, got %+v", p)
		}
		if len(diags) != 0 {
			t.Fatalf("expected no diagnostics, got %+v", diags)
		}
	}
}

func TestParse_MissingFrontMatter(t *testing.T) {
	doc := []byte("# Hello\n\n## Never\n\n- delete_branch: main\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !p.Empty {
		t.Fatalf("expected empty policy when front-matter missing, got %+v", p)
	}
	if !diagContains(diags, CodeMalformedFrontMatter, SeverityError) {
		t.Fatalf("expected CodeMalformedFrontMatter, got %+v", diags)
	}
}

func TestParse_UnsupportedSchema(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v2\n---\n## Never\n- delete_branch: main\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !p.Empty {
		t.Fatalf("expected empty policy on unsupported schema")
	}
	if !diagContains(diags, CodeUnsupportedSchemaVersion, SeverityError) {
		t.Fatalf("expected CodeUnsupportedSchemaVersion, got %+v", diags)
	}
}

func TestParse_NeverDeleteBranch(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n- push_to_branch: \"release/*\"\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if p.Empty || len(p.Never) != 2 {
		t.Fatalf("expected 2 predicates, got %+v", p)
	}
	if p.Never[0].Name != PredicateDeleteBranch || p.Never[0].Selector.BranchGlob != "main" {
		t.Fatalf("predicate[0] mismatch: %+v", p.Never[0])
	}
	if p.Never[1].Name != PredicatePushToBranch || p.Never[1].Selector.BranchGlob != "release/*" {
		t.Fatalf("predicate[1] mismatch: %+v", p.Never[1])
	}
}

func TestParse_PushBinaryOverSize(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- push_binary_over_size: 5M\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if got := p.Never[0].Selector.MaxBytes; got != 5*1024*1024 {
		t.Fatalf("expected 5MiB, got %d", got)
	}
}

func TestParse_UnknownPredicate(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- frobulate_branch: main\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(p.Never) != 0 {
		t.Fatalf("unknown predicate must be dropped, got %+v", p.Never)
	}
	if !diagContains(diags, CodeUnknownPredicate, SeverityError) {
		t.Fatalf("expected CodeUnknownPredicate, got %+v", diags)
	}
}

func TestParse_DuplicatePredicate(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n- delete_branch: main\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(p.Never) != 1 {
		t.Fatalf("duplicate must be dropped, got %d", len(p.Never))
	}
	if !diagContains(diags, CodeDuplicatePredicate, SeverityWarning) {
		t.Fatalf("expected CodeDuplicatePredicate, got %+v", diags)
	}
}

func TestParse_EmptyNeverBlock(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n\n## Always\n- something: x\n")
	_, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !diagContains(diags, CodeEmptyNeverBlock, SeverityWarning) {
		t.Fatalf("expected CodeEmptyNeverBlock, got %+v", diags)
	}
}

func TestParse_UnknownSection(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Cromulent\n- whatever\n## Never\n- delete_branch: main\n")
	_, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !diagContains(diags, CodeUnknownSection, SeverityWarning) {
		t.Fatalf("expected CodeUnknownSection, got %+v", diags)
	}
}

func TestParse_MalformedPredicate(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch\n")
	_, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !diagContains(diags, CodeMalformedPredicate, SeverityError) {
		t.Fatalf("expected CodeMalformedPredicate, got %+v", diags)
	}
}

func TestParse_MalformedFrontMatterYAML(t *testing.T) {
	doc := []byte("---\nschema: : :\n  not yaml\n---\n## Never\n- delete_branch: main\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !p.Empty {
		t.Fatalf("expected empty policy on malformed front-matter")
	}
	if !diagContains(diags, CodeMalformedFrontMatter, SeverityError) {
		t.Fatalf("expected CodeMalformedFrontMatter, got %+v", diags)
	}
}

func TestParse_FrontMatterMissingSchema(t *testing.T) {
	doc := []byte("---\nversion: 1\n---\n## Never\n- delete_branch: main\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !p.Empty {
		t.Fatalf("expected empty policy without schema")
	}
	if !diagContains(diags, CodeMalformedFrontMatter, SeverityError) {
		t.Fatalf("expected CodeMalformedFrontMatter, got %+v", diags)
	}
}

func TestLint_DelegatesToParse(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch\n")
	got := Lint(doc)
	if !diagContains(got, CodeMalformedPredicate, SeverityError) {
		t.Fatalf("expected lint to surface diagnostics, got %+v", got)
	}
}

func TestParse_QuotedSelector(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: \"main\"\n")
	p, diags, err := Parse(doc)
	if err != nil || len(diags) != 0 {
		t.Fatalf("parse: %v %+v", err, diags)
	}
	if p.Never[0].Selector.BranchGlob != "main" {
		t.Fatalf("quotes not stripped: %q", p.Never[0].Selector.BranchGlob)
	}
}

func diagContains(diags []Diagnostic, code string, sev Severity) bool {
	for _, d := range diags {
		if d.Code == code && d.Severity == sev {
			return true
		}
	}
	return false
}

func TestParse_AllowsBlankLinesAroundNever(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n\n# Repo notes\n\nSome prose.\n\n## Never\n\n- delete_branch: main\n\n")
	p, diags, err := Parse(doc)
	if err != nil {
		t.Fatalf("err=%v diags=%+v", err, diags)
	}
	if len(p.Never) != 1 {
		t.Fatalf("expected 1 predicate, got %d (diags=%+v)", len(p.Never), diags)
	}
	// "# Repo notes" is an unknown section heading; that's a warning, not blocking.
	for _, d := range diags {
		if d.Severity == SeverityError {
			t.Fatalf("unexpected error diag: %+v", d)
		}
	}
	_ = strings.TrimSpace
}
