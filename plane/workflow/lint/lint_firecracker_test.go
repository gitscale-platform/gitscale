package lint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLintFirecracker_passesOnGoodFixtures runs the lint script against a
// scan root containing only clean files and asserts a clean exit.
func TestLintFirecracker_passesOnGoodFixtures(t *testing.T) {
	script, scanRoot := firecrackerScriptAndFixture(t, "firecracker_good")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "FIRECRACKER_LINT_SCAN_ROOTS="+scanRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lint should pass on good fixture but failed:\nout=%s\nerr=%v", out, err)
	}
}

// TestLintFirecracker_failsOnBadFixtures runs the lint script against a
// scan root containing a forbidden import and asserts non-zero exit.
func TestLintFirecracker_failsOnBadFixtures(t *testing.T) {
	script, scanRoot := firecrackerScriptAndFixture(t, "firecracker_bad")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "FIRECRACKER_LINT_SCAN_ROOTS="+scanRoot)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("lint should fail on bad fixture but passed:\nout=%s", out)
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("lint did not exit with ExitError: %v", err)
	}
	if exit.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got %d", exit.ExitCode())
	}
}

// firecrackerScriptAndFixture rewrites the .go.txt fixtures to .go in a
// temp directory so the find -name '*.go' in the lint script picks them
// up. Pass kind = "firecracker_good" or "firecracker_bad".
func firecrackerScriptAndFixture(t *testing.T, kind string) (string, string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	script := filepath.Join(wd, "lint-firecracker.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("script not found at %s: %v", script, err)
	}
	srcDir := filepath.Join(wd, "testdata", kind)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", srcDir, err)
	}
	dst := t.TempDir()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		// Rename foo.go.txt -> foo.go in dst.
		base := e.Name()
		// strip trailing .txt; expect .go.txt
		newName := base[:len(base)-len(".txt")]
		data, err := os.ReadFile(filepath.Join(srcDir, base))
		if err != nil {
			t.Fatalf("read %s: %v", base, err)
		}
		if err := os.WriteFile(filepath.Join(dst, newName), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", newName, err)
		}
	}
	return script, dst
}
