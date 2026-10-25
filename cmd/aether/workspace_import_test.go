package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestWorkspaceImportRefusesBeforeDialing(t *testing.T) {
	testhome.Isolate(t)
	empty := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"workspace", "import"}, workspaceImportUsage},
		{[]string{"workspace", "import", "https://gitlab.com/acme/app.git"}, "is not a GitHub repository; from another Git host, run aether workspace add"},
		{[]string{"workspace", "import", filepath.Join(empty, "missing")}, filepath.Join(empty, "missing")},
		{[]string{"workspace", "import", empty}, "not a git repository"},
	} {
		err := dispatch(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestWorkspacesWithOriginMatchesTheStoredForm(t *testing.T) {
	list := []protocol.Workspace{
		{ID: "ws_app", Name: "app", Origin: "https://github.com/acme/app.git"},
		{ID: "ws_local", Name: "local"},
		{ID: "ws_other", Name: "other", Origin: "https://example.com/acme/app.git"},
	}
	for _, origin := range []string{"git@github.com:acme/app.git", "https://github.com/acme/app", "ssh://git@github.com/acme/app.git"} {
		if got := workspacesWithOrigin(list, origin); len(got) != 1 || got[0].ID != "ws_app" {
			t.Errorf("workspacesWithOrigin(%q) = %+v, want ws_app", origin, got)
		}
	}
	if got := workspacesWithOrigin(list, ""); got != nil {
		t.Errorf("a clone with no origin matched %+v", got)
	}
}

func TestImportAgainRefetchesAFailedGitHubImport(t *testing.T) {
	ws := protocol.Workspace{ID: "ws1", Name: "api"}
	replies := map[string]*fakeReply{
		protocol.MethodWorkspaceMirrorStatus: {result: protocol.WorkspaceMirrorResult{Enabled: true, Status: "offline", LastError: "dial tcp: timeout"}},
		protocol.MethodWorkspaceMirrorRefresh: {result: protocol.WorkspaceMirrorResult{
			Enabled: true, Status: "ready", Generation: 2, Branch: "main", ObservedCommit: "abc123"}},
	}
	var out bytes.Buffer
	if err := resumeGitHubImport(fakeControlMethods(t, replies), &out, []protocol.Workspace{ws}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "--generation 2 --commit abc123 --yes") {
		t.Fatalf("output = %q, want the adopt step", got)
	}

	replies[protocol.MethodWorkspaceMirrorRefresh].answer(protocol.WorkspaceMirrorResult{Enabled: true, Status: "offline", LastError: "dial tcp: timeout"})
	err := resumeGitHubImport(fakeControlMethods(t, replies), &out, []protocol.Workspace{ws})
	if err == nil || !strings.Contains(err.Error(), "dial tcp: timeout") || !strings.Contains(err.Error(), "aether workspace mirror refresh --workspace ws1") {
		t.Fatalf("err = %v, want the fetch error and the refresh command", err)
	}
}

func TestImportGitHubURLOfALocalCloneNeedsNoMirror(t *testing.T) {
	ws := protocol.Workspace{ID: "ws1", Name: "api"}
	replies := map[string]*fakeReply{
		protocol.MethodWorkspaceMirrorStatus: {result: protocol.WorkspaceMirrorResult{}},
	}
	var out bytes.Buffer
	if err := resumeGitHubImport(fakeControlMethods(t, replies), &out, []protocol.Workspace{ws}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "already added as workspace ws1 api") || !strings.Contains(got, "local clone") || !strings.Contains(got, "mirror configure --workspace ws1") {
		t.Fatalf("output = %q, want the local-clone note and the configure command", got)
	}
}

// A push URL set on the clone by hand would send the base to another
// repository, so the import stops before pushing.
func TestImportRefusesAPushURLThatGoesElsewhere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "aether", "ssh://alice@host:2222/ws-new.git"},
		{"config", "remote.aether.pushurl", "ssh://alice@host:2222/ws-old.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	ws := protocol.Workspace{ID: "ws-new", Name: "app", BaseBranch: "main"}
	err := pushBase(root, ws, "ssh://alice@host:2222/ws-new.git")
	if err == nil || !strings.Contains(err.Error(), "pushes to ssh://alice@host:2222/ws-old.git") || !strings.Contains(err.Error(), "--unset-all remote.aether.pushurl") {
		t.Fatalf("pushBase = %v, want the refusal naming the push URL", err)
	}
	for _, args := range [][]string{
		{"config", "remote.aether.pushurl", "ssh://alice@host:2222/ws-new.git"},
		{"config", "--add", "remote.aether.pushurl", "ssh://alice@host:2222/ws-other.git"},
	} {
		if out, gerr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gerr != nil {
			t.Fatalf("git %v: %v: %s", args, gerr, out)
		}
	}
	if err = pushBase(root, ws, "ssh://alice@host:2222/ws-new.git"); err == nil || !strings.Contains(err.Error(), "ws-other.git") {
		t.Fatalf("pushBase with a second push URL = %v, want the refusal naming it", err)
	}
}
