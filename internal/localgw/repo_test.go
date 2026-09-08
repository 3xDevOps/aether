package localgw

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// sshShim makes ssh:// git URLs resolve to local paths, so a test push
// really moves objects without dialing anything. Same trick as the pull
// test: the shim runs the wrapped git-receive-pack itself.
func sshShim(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test ssh shim is a POSIX shell script")
	}
	shim := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nfor last; do :; done\neval \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", shim)
	// An unknown GIT_SSH_COMMAND defaults to the "simple" variant, which
	// refuses the URL's port; declare the OpenSSH argv convention.
	t.Setenv("GIT_SSH_VARIANT", "ssh")
}

// pushGateway wires a gateway whose linked repo holds one commit on
// branch and an `aether` remote pointing, through the ssh shim, at a
// bare repo standing in for the workspace. The server reports that one
// workspace with that base branch. It returns the gateway, the bare
// remote's path, and the workspace ID the remote URL carries.
func pushGateway(t *testing.T, branch string) (*Gateway, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	sshShim(t)

	// The bare directory carries a .git suffix so the ssh URL's path
	// resolves to it verbatim.
	remote := filepath.Join(t.TempDir(), "wsp_1.git")
	localGit(t, t.TempDir(), "init", "--bare", "-b", branch, remote)
	wsID := strings.TrimSuffix(strings.TrimPrefix(remote, "/"), ".git")

	local := t.TempDir()
	localGit(t, local, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	localGit(t, local, "add", "README.md")
	localGit(t, local, "commit", "-m", "seed")
	localGit(t, local, "remote", "add", "aether", cli.GitURL("alice", "host:2222", wsID))

	list, err := json.Marshal(protocol.WorkspaceListResult{
		Workspaces: []protocol.Workspace{{ID: wsID, Name: "myproject", BaseBranch: branch}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodWorkspaceList: list},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: local})
	return g, remote, wsID
}

// pushBody is one repo.push request naming a workspace.
func pushBody(t *testing.T, wsID string) string {
	t.Helper()
	body, err := json.Marshal(struct {
		WorkspaceID string `json:"workspace_id"`
	}{wsID})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// syncGateway extends the push fixture with an origin remote whose branch is
// ahead of the linked checkout.
func syncGateway(t *testing.T, branch string) (*Gateway, string, string) {
	t.Helper()
	g, aether, wsID := pushGateway(t, branch)
	local := g.local.snapshot().Repo
	origin := filepath.Join(t.TempDir(), "origin.git")
	localGit(t, t.TempDir(), "init", "--bare", "-b", branch, origin)
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	localGit(t, local, "remote", "add", "origin", origin)
	localGit(t, local, "push", "origin", refspec)

	clone := filepath.Join(t.TempDir(), "origin-clone")
	localGit(t, t.TempDir(), "clone", "--branch", branch, origin, clone)
	if err := os.WriteFile(filepath.Join(clone, "origin.txt"), []byte("from origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	localGit(t, clone, "add", "origin.txt")
	localGit(t, clone, "commit", "-m", "origin update")
	localGit(t, clone, "push", "origin", refspec)
	return g, aether, wsID
}

// The seeding push runs the user's base branch, not a hardcoded main.
func TestLocalRepoPush(t *testing.T) {
	g, remote, wsID := pushGateway(t, "trunk")

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.push = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Branch          string `json:"branch"`
		Remote          string `json:"remote"`
		State           string `json:"state"`
		WorkspaceCommit string `json:"workspace_commit"`
		Ahead           int    `json:"ahead"`
		Behind          int    `json:"behind"`
		Output          string `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// A workspace whose repository has no such branch yet is the fresh
	// case the comparison lets straight through to the push. There was
	// nothing to count against, so both counts stay zero.
	if got.Branch != "trunk" || got.Remote != "aether" || got.State != "pushed" {
		t.Fatalf("repo.push = %+v", got)
	}
	if got.WorkspaceCommit != "" || got.Ahead != 0 || got.Behind != 0 {
		t.Fatalf("repo.push = %+v, want no workspace tip and no counts", got)
	}
	if !strings.Contains(got.Output, "trunk") {
		t.Fatalf("output does not mention the branch: %q", got.Output)
	}
	if localGit(t, remote, "rev-parse", "trunk") == "" {
		t.Fatal("remote has no trunk")
	}
}

func TestLocalRepoSync(t *testing.T) {
	g, remote, wsID := syncGateway(t, "trunk")
	local := g.local.snapshot().Repo
	beforeHead := localGit(t, local, "rev-parse", "HEAD")

	rec := do(g, http.MethodPost, "/local/v1/repo.sync", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.sync = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Branch string `json:"branch"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Branch != "trunk" {
		t.Fatalf("repo.sync = %+v", got)
	}
	if !strings.Contains(got.Output, "trunk") {
		t.Fatalf("output does not mention the branch: %q", got.Output)
	}
	if got := localGit(t, remote, "rev-parse", "trunk"); got == beforeHead {
		t.Fatal("repo.sync did not advance the aether branch")
	}
	if got := localGit(t, local, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("repo.sync changed local HEAD from %s to %s", beforeHead, got)
	}
}

func TestLocalRepoSyncRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/repo.sync", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no linked repo") {
		t.Fatalf("error = %+v", perr)
	}
}

// A rejected push is the server's word, not the gateway's; the handler
// answers with git's own text so branch protection reads as itself.
func TestLocalRepoPushSurfacesGitRefusal(t *testing.T) {
	g, remote, wsID := pushGateway(t, "main")
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'main is protected' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); !strings.Contains(perr.Message, "main is protected") {
		t.Fatalf("message = %q", perr.Message)
	}
}

// A repository the user has not committed in yet is theirs to fix, so it
// answers invalid state with the next step rather than a git failure.
func TestLocalRepoPushRefusesAnEmptyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	local := t.TempDir()
	localGit(t, local, "init", "-b", "main")
	list, err := json.Marshal(protocol.WorkspaceListResult{
		Workspaces: []protocol.Workspace{{ID: "wsp_1", Name: "myproject", BaseBranch: "main"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodWorkspaceList: list},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: local})

	rec := do(g, http.MethodPost, "/local/v1/repo.push", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no commits yet") {
		t.Fatalf("error = %+v", perr)
	}
}

func TestLocalRepoPushRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/repo.push", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no linked repo") {
		t.Fatalf("error = %+v", perr)
	}
}

// The base branch comes from the named workspace, the push lands wherever
// the `aether` remote points. When those are two different workspaces the
// verb refuses instead of reporting a seed it did not perform.
func TestLocalRepoPushRefusesAWorkspaceTheRemoteDoesNotServe(t *testing.T) {
	g, remote, wsID := pushGateway(t, "main")
	// link.repo has since re-pointed the remote at another workspace.
	other := cli.GitURL("alice", "host:2222", "wsp_other")
	localGit(t, g.local.snapshot().Repo, "remote", "set-url", "aether", other)

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, other) {
		t.Fatalf("error = %+v", perr)
	}
	if refs := localGit(t, remote, "for-each-ref"); refs != "" {
		t.Fatalf("the refused push still wrote refs: %s", refs)
	}
}

// The workspace check reads the repository before the push does, so a
// linked folder the user has since moved must still answer with the
// preflight's own words, not a bare git exit status from the check.
func TestLocalRepoPushRefusesALinkedFolderThatIsNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gone := t.TempDir()
	list, err := json.Marshal(protocol.WorkspaceListResult{
		Workspaces: []protocol.Workspace{{ID: "wsp_1", Name: "myproject", BaseBranch: "main"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodWorkspaceList: list},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: gone})

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, "wsp_1"), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, gone) || !strings.Contains(perr.Message, "not a git repository") {
		t.Fatalf("message = %q", perr.Message)
	}
}

// behindGateway is the second member's situation: a workspace another
// member already seeded and then pushed a further commit to, and a clone
// still sitting on the commit it started from. It returns the gateway,
// the bare workspace repo, and the workspace ID.
func behindGateway(t *testing.T) (*Gateway, string, string) {
	t.Helper()
	g, remote, wsID := pushGateway(t, "main")
	refspec := "refs/heads/main:refs/heads/main"
	localGit(t, g.local.snapshot().Repo, "push", "aether", refspec)

	// A second clone stands in for the member who seeded the workspace
	// and kept working in it.
	work := filepath.Join(t.TempDir(), "work")
	localGit(t, t.TempDir(), "clone", "--branch", "main", remote, work)
	if err := os.WriteFile(filepath.Join(work, "server.txt"), []byte("from the workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	localGit(t, work, "add", "server.txt")
	localGit(t, work, "commit", "-m", "workspace update")
	localGit(t, work, "push", "origin", refspec)
	return g, remote, wsID
}

// pushState is the repo.push answer: the comparison it made, and what it
// did about it.
type pushState struct {
	State           string `json:"state"`
	LocalCommit     string `json:"local_commit"`
	WorkspaceCommit string `json:"workspace_commit"`
	Ahead           int    `json:"ahead"`
	Behind          int    `json:"behind"`
	Output          string `json:"output"`
}

func decodePushState(t *testing.T, body []byte) pushState {
	t.Helper()
	var got pushState
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// Pressing Push now on a workspace a teammate already seeded used to
// answer nothing but git's "fetch first" rejection.
// It now reports how far behind the clone is and leaves the workspace
// branch alone, so the caller can offer a fast-forward.
func TestLocalRepoPushReportsAWorkspaceAheadOfTheClone(t *testing.T) {
	g, remote, wsID := behindGateway(t)
	before := localGit(t, remote, "rev-parse", "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.push = %d: %s", rec.Code, rec.Body)
	}
	got := decodePushState(t, rec.Body.Bytes())
	if got.State != "behind" || got.Ahead != 0 || got.Behind != 1 {
		t.Fatalf("repo.push = %+v, want behind by one", got)
	}
	if got.WorkspaceCommit != before || got.LocalCommit == before {
		t.Fatalf("commits = %+v, want the workspace at %s and the clone elsewhere", got, before)
	}
	if after := localGit(t, remote, "rev-parse", "main"); after != before {
		t.Fatalf("the workspace branch moved: %s -> %s", before, after)
	}
}

func TestLocalRepoPushReportsAnAlreadySeededWorkspace(t *testing.T) {
	g, remote, wsID := pushGateway(t, "main")
	localGit(t, g.local.snapshot().Repo, "push", "aether", "refs/heads/main:refs/heads/main")
	tip := localGit(t, remote, "rev-parse", "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.push = %d: %s", rec.Code, rec.Body)
	}
	got := decodePushState(t, rec.Body.Bytes())
	if got.State != "up-to-date" || got.LocalCommit != tip || got.WorkspaceCommit != tip {
		t.Fatalf("repo.push = %+v, want up-to-date at %s", got, tip)
	}
	// The tips match, so the counts were never measured.
	if got.Ahead != 0 || got.Behind != 0 {
		t.Fatalf("repo.push = %+v, want no counts", got)
	}
}

// Both sides moved on. Nothing is pushed and nothing is forced: the
// member resolves it in their own repository.
func TestLocalRepoPushReportsDivergence(t *testing.T) {
	g, remote, wsID := behindGateway(t)
	localGit(t, g.local.snapshot().Repo, "commit", "--allow-empty", "-m", "my work")
	before := localGit(t, remote, "rev-parse", "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.push = %d: %s", rec.Code, rec.Body)
	}
	got := decodePushState(t, rec.Body.Bytes())
	if got.State != "diverged" || got.Ahead != 1 || got.Behind != 1 {
		t.Fatalf("repo.push = %+v, want diverged one for one", got)
	}
	if after := localGit(t, remote, "rev-parse", "main"); after != before {
		t.Fatalf("the workspace branch moved: %s -> %s", before, after)
	}
}

func TestLocalRepoFastForward(t *testing.T) {
	g, remote, wsID := behindGateway(t)
	local := g.local.snapshot().Repo
	tip := localGit(t, remote, "rev-parse", "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.fast-forward", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.fast-forward = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Branch  string `json:"branch"`
		Commit  string `json:"commit"`
		Current bool   `json:"current"`
		Dirty   bool   `json:"dirty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" || got.Commit != tip || !got.Current || got.Dirty {
		t.Fatalf("repo.fast-forward = %+v, want main at %s, current and clean", got, tip)
	}
	if head := localGit(t, local, "rev-parse", "HEAD"); head != tip {
		t.Fatalf("HEAD = %s, want %s", head, tip)
	}
	if merges := localGit(t, local, "rev-list", "--merges", "--count", "HEAD"); merges != "0" {
		t.Fatalf("the fast-forward merged: %s merge commits", merges)
	}
}

func TestLocalRepoFastForwardRefusesDivergence(t *testing.T) {
	g, _, wsID := behindGateway(t)
	local := g.local.snapshot().Repo
	localGit(t, local, "commit", "--allow-empty", "-m", "my work")
	mine := localGit(t, local, "rev-parse", "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.fast-forward", pushBody(t, wsID), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "aether/main") {
		t.Fatalf("error = %+v", perr)
	}
	if after := localGit(t, local, "rev-parse", "main"); after != mine {
		t.Fatalf("the refused fast-forward moved main to %s, want %s", after, mine)
	}
}

func TestLocalRepoFastForwardRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/repo.fast-forward", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no linked repo") {
		t.Fatalf("error = %+v", perr)
	}
}

// An uncommitted change the fast-forward would overwrite is the member's
// to resolve in their own repository, so it answers the locally-fixable
// refusal rather than an internal error - carrying git's own words.
func TestLocalRepoFastForwardRefusesAnEditItWouldOverwrite(t *testing.T) {
	g, _, wsID := behindGateway(t)
	local := g.local.snapshot().Repo
	if err := os.WriteFile(filepath.Join(local, "server.txt"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := localGit(t, local, "rev-parse", "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.fast-forward", pushBody(t, wsID), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "would be overwritten by merge") {
		t.Fatalf("message drops git's own words: %q", perr.Message)
	}
	if after := localGit(t, local, "rev-parse", "main"); after != before {
		t.Fatalf("the refused fast-forward moved main to %s, want %s", after, before)
	}
}

// The base branch checked out in a second worktree is a local state the
// member resolves in their own repository, so it answers the same
// invalid-state refusal as a dirty checkout rather than an internal error.
func TestLocalRepoFastForwardRefusesABranchHeldByAnotherWorktree(t *testing.T) {
	g, _, wsID := behindGateway(t)
	local := g.local.snapshot().Repo
	before := localGit(t, local, "rev-parse", "main")
	localGit(t, local, "switch", "-c", "feature")
	held := filepath.Join(t.TempDir(), "held")
	localGit(t, local, "worktree", "add", held, "main")

	rec := do(g, http.MethodPost, "/local/v1/repo.fast-forward", pushBody(t, wsID), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, held) || !strings.Contains(perr.Message, "merge --ff-only") {
		t.Fatalf("message does not name the worktree and the fix: %q", perr.Message)
	}
	if after := localGit(t, local, "rev-parse", "main"); after != before {
		t.Fatalf("the refused fast-forward moved main to %s, want %s", after, before)
	}
}
