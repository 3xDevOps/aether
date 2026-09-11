//go:build integration

// Integration tests against a real git binary: a file:// remote stands in
// for the server's SSH transport, proving the fetch/push plumbing is
// refs-only and correct.
package syncd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepoPair returns a bare "server" repo with one commit on main and a
// local clone with the bare repo as remote "aether".
func newRepoPair(t *testing.T) (bare, local string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("integration test needs git: %v", err)
	}
	root := t.TempDir()
	bare = filepath.Join(root, "server.git")
	seed := filepath.Join(root, "seed")
	local = filepath.Join(root, "local")
	gitc(t, root, "init", "--bare", bare)
	gitc(t, root, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, seed, "add", ".")
	gitc(t, seed, "commit", "-m", "init")
	gitc(t, seed, "push", bare, "main:main")
	gitc(t, root, "clone", "-b", "main", bare, local)
	gitc(t, local, "remote", "rename", "origin", "aether")
	return bare, local
}

// serverCommit adds a commit to branch in the bare repo via a throwaway
// worktree push, mimicking the server publishing a run branch.
func serverCommit(t *testing.T, bare, branch, file string) string {
	t.Helper()
	work := t.TempDir()
	gitc(t, filepath.Dir(work), "clone", bare, work)
	if err := os.WriteFile(filepath.Join(work, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, work, "checkout", "-b", branch)
	gitc(t, work, "add", ".")
	gitc(t, work, "commit", "-m", "agent work")
	gitc(t, work, "push", bare, branch+":"+branch)
	return gitc(t, work, "rev-parse", "HEAD")
}

func TestIntegrationFetchRunsIsRefsOnly(t *testing.T) {
	bare, local := newRepoPair(t)
	sha := serverCommit(t, bare, "aether/run-r1-fix-auth", "agent.txt")

	d, err := New(Config{Server: "unused:1", RepoPath: local})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.fetchRuns(context.Background()); err != nil {
		t.Fatalf("fetchRuns: %v", err)
	}

	got := gitc(t, local, "rev-parse", "refs/remotes/aether/aether/run-r1-fix-auth")
	if got != sha {
		t.Errorf("remote-tracking ref = %s, want %s", got, sha)
	}
	// Refs only: no local branch, clean working tree, no agent file.
	if out := gitc(t, local, "status", "--porcelain"); out != "" {
		t.Errorf("working tree dirtied by fetch:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(local, "agent.txt")); !os.IsNotExist(err) {
		t.Error("fetch materialized an agent file in the working tree")
	}
	if out := gitc(t, local, "branch", "--list", "aether/run-*"); out != "" {
		t.Errorf("fetch created local branches:\n%s", out)
	}

	// A run branch appearing after the first fetch still syncs.
	sha2 := serverCommit(t, bare, "aether/run-r1-fix-auth-v2", "more.txt")
	if err := d.fetchRuns(context.Background()); err != nil {
		t.Fatalf("second fetchRuns: %v", err)
	}
	if got := gitc(t, local, "rev-parse", "refs/remotes/aether/aether/run-r1-fix-auth-v2"); got != sha2 {
		t.Errorf("second ref = %s, want %s", got, sha2)
	}
}

func TestIntegrationPushBase(t *testing.T) {
	bare, local := newRepoPair(t)
	d, err := New(Config{Server: "unused:1", RepoPath: local})
	if err != nil {
		t.Fatal(err)
	}

	// Nothing new: push succeeds (up to date) and records the tip.
	if err := d.pushBase(context.Background()); err != nil {
		t.Fatalf("pushBase (up to date): %v", err)
	}

	if err := os.WriteFile(filepath.Join(local, "f.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, local, "add", ".")
	gitc(t, local, "commit", "-m", "local base update")
	tip := gitc(t, local, "rev-parse", "HEAD")

	if err := d.pushBase(context.Background()); err != nil {
		t.Fatalf("pushBase: %v", err)
	}
	if got := gitc(t, bare, "rev-parse", "refs/heads/main"); got != tip {
		t.Errorf("server main = %s, want %s", got, tip)
	}
	// Tip unchanged: the next poll is a no-op (lastPushed short-circuit).
	if err := d.pushBase(context.Background()); err != nil {
		t.Fatalf("pushBase (no-op): %v", err)
	}
	if d.lastPushed != tip {
		t.Errorf("lastPushed = %s, want %s", d.lastPushed, tip)
	}
}
