package lint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLintDeterminism_passesOnGoodFixtures runs the lint script against
// testdata/good/ and asserts a clean exit. Proves the rule set isn't so
// strict that legitimate workflow code can't pass.
func TestLintDeterminism_passesOnGoodFixtures(t *testing.T) {
	script, scanRoot := scriptAndFixture(t, "good")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "WORKFLOW_LINT_SCAN_ROOT="+scanRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lint should have passed on good fixture but failed:\nout=%s\nerr=%v", out, err)
	}
}

// TestLintDeterminism_failsOnBadFixtures runs the lint script against
// testdata/bad/ and asserts non-zero exit. Proves the lint isn't silently
// passing — the canonical sanity check on any rule-driven enforcement.
func TestLintDeterminism_failsOnBadFixtures(t *testing.T) {
	script, scanRoot := scriptAndFixture(t, "bad")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "WORKFLOW_LINT_SCAN_ROOT="+scanRoot)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("lint should have failed on bad fixture but passed:\nout=%s", out)
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("lint did not exit with ExitError: %v", err)
	}
	if exit.ExitCode() != 1 {
		t.Fatalf("expected exit 1 (rule violation), got %d", exit.ExitCode())
	}
}

func scriptAndFixture(t *testing.T, kind string) (string, string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// wd = …/plane/workflow/lint when go test runs from this package.
	script := filepath.Join(wd, "lint-determinism.sh")
	scanRoot := filepath.Join(wd, "testdata", kind)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("script not found at %s: %v", script, err)
	}
	if _, err := os.Stat(scanRoot); err != nil {
		t.Fatalf("scan root not found at %s: %v", scanRoot, err)
	}
	return script, scanRoot
}
