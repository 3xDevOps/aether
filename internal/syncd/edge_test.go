package syncd

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
)

const testServerID = "wqc4lsjvzdzrwq3k5dabdtajwj"

func TestNewValidatesEdgeLink(t *testing.T) {
	for _, cfg := range []Config{
		{ServerID: testServerID, RepoPath: "."},
		{ServerID: "not-a-server-id", EdgeURL: "https://edge.example", RepoPath: "."},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
	if _, err := New(Config{ServerID: testServerID, EdgeURL: "https://edge.example", RepoPath: "."}); err != nil {
		t.Fatalf("New with an edge-only link: %v", err)
	}
}

func TestGitRunsEdgeSSHOnlyForEdgeLinks(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	want, err := cli.EdgeSSHCommand()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", "")
	show := []string{"-c", `alias.sshcmd=!printf %s "$GIT_SSH_COMMAND"`, "sshcmd"}

	edge, err := New(Config{ServerID: testServerID, EdgeURL: "https://edge.example", RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	got, err := edge.git(context.Background(), show...)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.HasSuffix(got, " edge-ssh") {
		t.Fatalf("GIT_SSH_COMMAND for an edge link = %q, want %q", got, want)
	}

	direct, err := New(Config{Server: "host:2222", RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	if got, err = direct.git(context.Background(), show...); err != nil || got != "" {
		t.Fatalf("GIT_SSH_COMMAND for a direct link = %q, %v; want it unset", got, err)
	}
}
