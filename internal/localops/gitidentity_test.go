package localops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// isolateGitConfig points git at a scratch global config and no system
// config, and moves out of any repository, so the test reads what it
// wrote rather than the developer's own identity.
func isolateGitConfig(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte(contents), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Chdir(dir)
}

func TestGitIdentitySet(t *testing.T) {
	isolateGitConfig(t, "[user]\n\tname = Ada Lovelace\n\temail = ada@example.invalid\n")

	name, email, err := GitIdentity(context.Background())
	if err != nil {
		t.Fatalf("GitIdentity: %v", err)
	}
	if name != "Ada Lovelace" || email != "ada@example.invalid" {
		t.Errorf("GitIdentity = %q/%q, want Ada Lovelace/ada@example.invalid", name, email)
	}
}

func TestGitIdentityUnset(t *testing.T) {
	isolateGitConfig(t, "")

	name, email, err := GitIdentity(context.Background())
	if err != nil {
		t.Fatalf("GitIdentity: %v", err)
	}
	if name != "" || email != "" {
		t.Errorf("GitIdentity = %q/%q, want both empty", name, email)
	}
}
