package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A push refuses while a scanner finding in a file the member wrote is
// unacknowledged, and the refusal carries both ways out: leave the file
// behind, or send it with an audit trail. Naming it with --skip-secret
// gets past the refusal, and the flagged file is not among what would be
// uploaded.
func TestProfilePushRefusesUntilAFindingIsAnswered(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("..", "..", "internal", "cli", "profile", "testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	readme := filepath.Join(home, ".claude", "skills", "deploy", "README.md")
	if err = os.MkdirAll(filepath.Dir(readme), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(readme, secret, 0o644); err != nil {
		t.Fatal(err)
	}

	err = profilePush([]string{"--agent", "claude"})
	if err == nil {
		t.Fatal("a finding in the member's own file must be answered before a push")
	}
	for _, want := range []string{
		"the secret scanner flagged a file you wrote",
		"skills/deploy/README.md",
		"secret detected",
		"aether profile push --agent claude --skip-secret skills/deploy/README.md",
		"aether profile push --agent claude --allow-secret skills/deploy/README.md --workspace <workspace>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not carry %q", err.Error(), want)
		}
	}

	// --skip-secret answers it. There is no linked server here, so the
	// push fails at the control connection instead - past the refusal.
	err = profilePush([]string{"--agent", "claude", "--skip-secret", "skills/deploy/README.md"})
	if err != nil && strings.Contains(err.Error(), "--skip-secret") {
		t.Fatalf("--skip-secret did not answer the finding: %v", err)
	}
}

// The refusal comes after the skipped list, not instead of it: a member
// answering a finding of their own still learns which other files the
// walk dropped, and still gets the command that carries a plugin's own
// file, which docs/harnesses.md promises next to each one.
func TestProfilePushPrintsSkippedFilesBeforeRefusing(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("..", "..", "internal", "cli", "profile", "testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	vendored := filepath.Join("plugins", "cache", "official", "notes", "6.3.0", "tests", "ws.test.js")
	for _, rel := range []string{filepath.Join("skills", "deploy", "README.md"), vendored} {
		path := filepath.Join(home, ".claude", rel)
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, secret, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error { return profilePush([]string{"--agent", "claude"}) })
	if err == nil {
		t.Fatal("the finding in the member's own file must still refuse the push")
	}
	for _, want := range []string{
		"skipped plugins/cache/official/notes/6.3.0/tests/ws.test.js",
		"to send it anyway: aether profile push --agent claude --allow-secret " +
			"plugins/cache/official/notes/6.3.0/tests/ws.test.js --workspace <workspace>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not carry %q", out, want)
		}
	}
}

// captureStdout collects what run prints, so a test can assert on output
// the command writes rather than returns.
func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := run()
	os.Stdout = saved
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}
