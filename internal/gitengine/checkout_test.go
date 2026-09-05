package gitengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func runCheckoutGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestCommitAllIgnoresCheckoutHooksAndFilters(t *testing.T) {
	e := newUnitEngine(t)
	checkout := filepath.Join(e.cfg.CheckoutsDir, "run-hostile")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, checkout, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(checkout, "payload"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, checkout, "add", "-A")
	runCheckoutGit(t, checkout, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")

	markerDir := t.TempDir()
	hookMarker := filepath.Join(markerDir, "hook-ran")
	filterMarker := filepath.Join(markerDir, "filter-ran")
	hooks := filepath.Join(markerDir, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf hook > "+hookMarker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := filepath.Join(markerDir, "clean-filter")
	if err := os.WriteFile(clean, []byte("#!/bin/sh\nprintf filter > "+filterMarker+"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, checkout, "config", "core.hooksPath", hooks)
	runCheckoutGit(t, checkout, "config", "filter.hostile.clean", clean)
	runCheckoutGit(t, checkout, "config", "filter.hostile.required", "true")
	if err := os.WriteFile(filepath.Join(checkout, ".gitattributes"), []byte("payload filter=hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// GIT_ATTR_SOURCE does not cover .git/info/attributes; CommitAll must
	// drop it rather than let it select the filter.
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git", "info", "attributes"), []byte("payload filter=hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "payload"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	commit, err := e.CommitAll(t.Context(), domain.RunID("run-hostile"), "server commit")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if commit == "" {
		t.Fatal("CommitAll returned an empty commit")
	}
	if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("pre-commit hook ran, marker stat = %v", err)
	}
	if _, err := os.Stat(filterMarker); !os.IsNotExist(err) {
		t.Fatalf("clean filter ran, marker stat = %v", err)
	}
	if got := runCheckoutGit(t, checkout, "show", "--format=", "--no-ext-diff", "HEAD:payload"); got != "after" {
		t.Fatalf("committed payload = %q, want after", got)
	}
	if got := runCheckoutGit(t, checkout, "show", "--format=", "--no-ext-diff", "HEAD:.gitattributes"); got != "payload filter=hostile" {
		t.Fatalf("committed attributes = %q, want hostile attribute", got)
	}
}
