package localops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syncBaseRepos(t *testing.T, branch string) (local, origin, aether string) {
	t.Helper()
	origin = filepath.Join(t.TempDir(), "origin.git")
	aether = filepath.Join(t.TempDir(), "aether.git")
	git(t, t.TempDir(), "init", "--bare", "-b", branch, origin)
	git(t, t.TempDir(), "init", "--bare", "-b", branch, aether)

	local = t.TempDir()
	git(t, local, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, local, "add", "README.md")
	git(t, local, "commit", "-m", "seed")
	git(t, local, "remote", "add", "origin", origin)
	git(t, local, "remote", "add", "aether", aether)
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	git(t, local, "push", "origin", refspec)
	git(t, local, "push", "aether", refspec)
	return local, origin, aether
}

func advanceSyncBaseRemote(t *testing.T, remote, branch, name, contents, message string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, t.TempDir(), "clone", "--branch", branch, remote, clone)
	if err := os.WriteFile(filepath.Join(clone, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "add", name)
	git(t, clone, "commit", "-m", message)
	git(t, clone, "push", "origin", "refs/heads/"+branch+":refs/heads/"+branch)
	return git(t, remote, "rev-parse", branch)
}

func TestSyncBaseFastForwardsAetherFromOriginWithoutTouchingLocal(t *testing.T) {
	requireGit(t)
	local, origin, aether := syncBaseRepos(t, "main")
	beforeHead := git(t, local, "rev-parse", "HEAD")
	beforeBranch := git(t, local, "branch", "--show-current")
	beforeStatus := git(t, local, "status", "--porcelain")
	originTip := advanceSyncBaseRemote(t, origin, "main", "origin.txt", "from origin\n", "origin update")

	output, err := SyncBase(local, "main")
	if err != nil {
		t.Fatalf("SyncBase: %v", err)
	}
	if !strings.Contains(output, "main") {
		t.Fatalf("output does not mention the branch: %q", output)
	}
	if got := git(t, aether, "rev-parse", "main"); got != originTip {
		t.Fatalf("aether main = %s, want origin tip %s", got, originTip)
	}
	if got := git(t, local, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("local HEAD changed from %s to %s", beforeHead, got)
	}
	if got := git(t, local, "branch", "--show-current"); got != beforeBranch {
		t.Fatalf("local branch changed from %q to %q", beforeBranch, got)
	}
	if got := git(t, local, "status", "--porcelain"); got != beforeStatus {
		t.Fatalf("local working tree changed from %q to %q", beforeStatus, got)
	}
}

func TestSyncBaseRefusesWithoutOriginRemote(t *testing.T) {
	requireGit(t)
	local, _ := seedRepos(t, "main")

	_, err := SyncBase(local, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Fatalf("message = %q, want it to name origin", err)
	}
}

func TestSyncBaseReturnsNonFastForwardWithoutForcingAether(t *testing.T) {
	requireGit(t)
	local, origin, aether := syncBaseRepos(t, "main")
	advanceSyncBaseRemote(t, origin, "main", "origin.txt", "from origin\n", "origin update")
	serverTip := advanceSyncBaseRemote(t, aether, "main", "server.txt", "from server\n", "server update")

	_, err := SyncBase(local, "main")
	if err == nil {
		t.Fatal("SyncBase succeeded over a diverged aether branch")
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "rejected") && !strings.Contains(lower, "non-fast-forward") {
		t.Fatalf("error = %q, want git's non-fast-forward rejection text", err)
	}
	if got := git(t, aether, "rev-parse", "main"); got != serverTip {
		t.Fatalf("aether main changed from rejected push: got %s, want %s", got, serverTip)
	}
}
