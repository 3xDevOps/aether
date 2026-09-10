package sshd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func TestGitHubConnectIsMemberScoped(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var res protocol.GitHubConnectResult
	if err := c.Call(protocol.MethodGitHubConnect, struct{}{}, &res); err != nil {
		t.Fatalf("github.connect: %v", err)
	}
	if res.Login != "octocat" || !strings.HasSuffix(res.SigningKey, "aether "+string(e.member.ID)) || !strings.HasPrefix(res.Fingerprint, "SHA256:") {
		t.Fatalf("result = %+v, want the caller's own connection", res)
	}
	calls := e.runs.Calls()
	if len(calls) != 1 || calls[0] != "github-connect:"+string(e.member.ID) {
		t.Fatalf("RunController calls = %v, want one github-connect for the caller", calls)
	}
}

// github.probe answers for the caller's own environment terminal and
// nobody else's, the same way the connect does.
func TestGitHubProbeIsMemberScoped(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var res protocol.GitHubProbeResult
	if err := c.Call(protocol.MethodGitHubProbe, struct{}{}, &res); err != nil {
		t.Fatalf("github.probe: %v", err)
	}
	want := protocol.GitHubProbeResult{
		Status:      string(domain.GitHubCLIOutdated),
		Version:     "2.45.0",
		Minimum:     "2.81.0",
		Detail:      "gh version 2.45.0",
		Image:       "ghcr.io/3xdevops/aether-standard:latest",
		SavedImage:  "aether/member-" + string(e.member.ID) + ":1",
		Path:        "/root/.local/bin/gh",
		Remedy:      "aether env reset",
		AdminRemedy: "docker pull ghcr.io/3xdevops/aether-standard:latest",
	}
	if res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	calls := e.runs.Calls()
	if len(calls) != 1 || calls[0] != "github-probe:"+string(e.member.ID) {
		t.Fatalf("RunController calls = %v, want one github-probe for the caller", calls)
	}
}

// Every way a connect can be refused for the state of the member's gh or
// their login reaches the client as CodeInvalidState, with the remedy in
// the message.
func TestGitHubLoginProblemsMapToInvalidState(t *testing.T) {
	for _, seam := range []error{
		scheduler.ErrGitHubNotLoggedIn, scheduler.ErrGitHubScopeMissing,
		scheduler.ErrGitHubCLIMissing, scheduler.ErrGitHubCLIBroken,
		scheduler.ErrGitHubCLIOutdated,
	} {
		t.Run(seam.Error(), func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.runs.setErr(seam)
			c := controlClient(t, e)

			var pe *protocol.Error
			err := c.Call(protocol.MethodGitHubConnect, struct{}{}, nil)
			if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
				t.Fatalf("github.connect error = %v, want CodeInvalidState", err)
			}
			if pe.Message != seam.Error() {
				t.Errorf("message = %q, want %q", pe.Message, seam.Error())
			}
		})
	}
}

// A member who has connected GitHub keeps the identity in their home's
// .gitconfig - what commits made inside their containers are authored as -
// in step with the one member.git records.
func TestMemberGitRefreshesTheHomeGitConfig(t *testing.T) {
	homesRoot := filepath.Join(t.TempDir(), "homes")
	homes, err := memberhome.New(homesRoot)
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	e := newTestEnv(t, func(c *Config) { c.Homes = homes })
	if _, keyErr := homes.EnsureSigningKey(e.member.ID); keyErr != nil {
		t.Fatalf("EnsureSigningKey: %v", keyErr)
	}
	c := controlClient(t, e)

	if callErr := c.Call(protocol.MethodMemberGit, protocol.MemberGitParams{
		Name: "Ada Lovelace", Email: "ada@example.com",
	}, nil); callErr != nil {
		t.Fatalf("member.git: %v", callErr)
	}
	config, err := os.ReadFile(filepath.Join(homesRoot, string(e.member.ID), ".gitconfig"))
	if err != nil {
		t.Fatalf("read .gitconfig: %v", err)
	}
	for _, want := range []string{"name = Ada Lovelace", "email = ada@example.com", "signingkey = ~/.ssh/aether_signing"} {
		if !strings.Contains(string(config), want) {
			t.Errorf(".gitconfig is missing %q:\n%s", want, config)
		}
	}
}

// Without a signing key there is nothing to keep in step, so member.git
// writes no .gitconfig at all.
func TestMemberGitLeavesAHomeWithoutASigningKeyAlone(t *testing.T) {
	homesRoot := filepath.Join(t.TempDir(), "homes")
	homes, err := memberhome.New(homesRoot)
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	e := newTestEnv(t, func(c *Config) { c.Homes = homes })
	c := controlClient(t, e)

	if err := c.Call(protocol.MethodMemberGit, protocol.MemberGitParams{
		Name: "Ada Lovelace", Email: "ada@example.com",
	}, nil); err != nil {
		t.Fatalf("member.git: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homesRoot, string(e.member.ID), ".gitconfig")); !os.IsNotExist(err) {
		t.Fatalf(".gitconfig stat = %v, want not exists", err)
	}
}
