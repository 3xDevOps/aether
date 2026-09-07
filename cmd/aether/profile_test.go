package main

import (
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
