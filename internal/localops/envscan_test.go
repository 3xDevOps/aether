package localops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/harness"
)

// writeStub writes an executable shell script and returns the argv
// override that runs it with the rendered prompt as its first argument.
// The stubs are POSIX shell scripts, so every test that runs one skips
// on Windows; the engine itself execs harness argv directly and stays
// covered there by the portable fake-harness and validation tests.
func writeStub(t *testing.T, body string) []string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("stub harnesses are POSIX shell scripts")
	}
	script := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", script, harness.TaskPlaceholder}
}

// scanRecorder collects progress events; RunScan serializes callbacks so
// no locking is needed here.
type scanRecorder struct {
	statuses []string
	lines    []string
}

func (r *scanRecorder) record(event ScanEvent) {
	if event.Status != "" {
		r.statuses = append(r.statuses, event.Status)
	}
	if event.Line != "" {
		r.lines = append(r.lines, event.Line)
	}
}

// assertGone fails unless every recorded scratch directory was removed.
func assertGone(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if dir == "" {
			t.Fatal("no scratch directory was recorded")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("scratch directory %s still exists (stat err: %v)", dir, err)
		}
	}
}

// readScratch reads the scratch paths a stub recorded, one per attempt.
func readScratch(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read scratch log: %v", err)
	}
	return strings.Fields(string(data))
}

// initGitRepo creates a real git repository for repo-mode tests.
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// resolvePath follows symlinks so a stub's pwd output compares equal to
// the path the test created (temp dirs are symlinked on some systems).
func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func TestDetectHarnesses(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"claude", "pi"} {
		// Windows PATH lookup only finds files with an executable
		// extension, so the stub carries one there.
		if goruntime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	got := DetectHarnesses()
	want := []HarnessStatus{
		{Name: "claude", Installed: true},
		{Name: "codex", Installed: false},
		{Name: "pi", Installed: true},
		{Name: "amp", Installed: false},
	}
	if len(got) != len(want) {
		t.Fatalf("DetectHarnesses returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
