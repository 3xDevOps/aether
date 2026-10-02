package localops

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
	"github.com/3xDevOps/Aether/internal/syncd"
	"github.com/3xDevOps/Aether/internal/testhome"
)

const testServerID = "wqc4lsjvzdzrwq3k5dabdtajwj"

func TestLinkRepoPointsEdgeLinkAtLogicalHostAndSetsSSHCommand(t *testing.T) {
	requireGit(t)
	useTempConfigDir(t)
	repo := t.TempDir()
	git(t, repo, "init")
	cfg := cli.Config{User: "aether", EdgeURL: "https://edge.example", ServerID: testServerID}
	_, url, err := LinkRepo(cfg, repo, "ws_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ssh://aether@" + cli.EdgeHost(testServerID) + "/ws_1.git"; url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
	want, err := cli.EdgeSSHCommand()
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "config", "--local", "--get", "core.sshCommand"); got != want {
		t.Fatalf("core.sshCommand = %q, want %q", got, want)
	}
}

func TestGitRemoteNeverOverwritesSSHCommand(t *testing.T) {
	requireGit(t)
	testhome.Isolate(t)
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "core.sshCommand", "ssh -i ~/.ssh/work")
	var out bytes.Buffer
	if err := GitRemote(repo, cli.GitURL("aether", cli.EdgeHost(testServerID), "ws_1"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "config", "--get", "core.sshCommand"); got != "ssh -i ~/.ssh/work" {
		t.Fatalf("core.sshCommand = %q, want the user's value kept", got)
	}
	want, err := cli.EdgeSSHCommand()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "config core.sshCommand "+shellquote.Quote(want)+"\n") {
		t.Fatalf("output %q lacks the command to set it", out.String())
	}
}

func TestGitRemoteReplacesAnEarlierAetherSSHCommand(t *testing.T) {
	requireGit(t)
	testhome.Isolate(t)
	want, err := cli.EdgeSSHCommand()
	if err != nil {
		t.Fatal(err)
	}
	for _, earlier := range []string{
		"/home/linuxbrew/.linuxbrew/Cellar/aether/1.4.0/bin/aether edge-ssh",
		"'/opt/old builds/aether' edge-ssh",
	} {
		repo := t.TempDir()
		git(t, repo, "init")
		git(t, repo, "config", "core.sshCommand", earlier)
		if err := GitRemote(repo, cli.GitURL("aether", cli.EdgeHost(testServerID), "ws_1"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if got := git(t, repo, "config", "--get", "core.sshCommand"); got != want {
			t.Fatalf("core.sshCommand = %q after relinking over %q, want %q", got, earlier, want)
		}
	}
	for _, own := range []string{
		"/usr/bin/aether edge-ssh -v",
		"/usr/bin/aether edge-ssh; ssh",
		"/usr/bin/aetherx edge-ssh",
		"'/usr/bin/aether' edge-ssh",
	} {
		repo := t.TempDir()
		git(t, repo, "init")
		git(t, repo, "config", "core.sshCommand", own)
		if err := GitRemote(repo, cli.GitURL("aether", cli.EdgeHost(testServerID), "ws_1"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if got := git(t, repo, "config", "--get", "core.sshCommand"); got != own {
			t.Fatalf("core.sshCommand = %q, want the user's %q kept", got, own)
		}
	}
}

func TestGitRemoteLeavesSSHCommandAloneForDirectLinks(t *testing.T) {
	requireGit(t)
	testhome.Isolate(t)
	repo := t.TempDir()
	git(t, repo, "init")
	if err := GitRemote(repo, cli.GitURL("aether", "host:2222", "ws_1"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "config", "--get", "core.sshCommand").Output(); err == nil {
		t.Fatalf("a direct link set core.sshCommand to %q", out)
	}
}

func TestPullCommandRunsEdgeSSHOnlyForEdgeHosts(t *testing.T) {
	coords := protocol.RunPullResult{WorkspaceID: "ws_1", Branch: "aether/run-1"}
	_, edge, err := PullCommand(t.TempDir(), "aether", cli.EdgeHost(testServerID), coords)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(edge.Env, func(e string) bool { return strings.HasPrefix(e, "GIT_SSH_COMMAND=") }) {
		t.Fatal("an edge fetch runs without GIT_SSH_COMMAND")
	}
	_, direct, err := PullCommand(t.TempDir(), "aether", "host:2222", coords)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Env != nil {
		t.Fatalf("a direct fetch changed the environment: %q", direct.Env)
	}
}

func TestDaemonUnitCarriesEdgeLink(t *testing.T) {
	testhome.Isolate(t)
	path, _, err := InstallDaemonUnit(syncd.Config{
		EdgeURL: "https://edge.example", ServerID: testServerID, RepoPath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--server-id", testServerID, "--edge-url", "https://edge.example"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("unit lacks %q: %s", want, body)
		}
	}
	if strings.Contains(string(body), "--server ") {
		t.Fatalf("unit names a server address the link does not have: %s", body)
	}
}
