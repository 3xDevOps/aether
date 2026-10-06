package sshd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func installInHome(t *testing.T, homes *memberhome.Manager, member domain.MemberID, files ...string) {
	t.Helper()
	home, err := homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		full := filepath.Join(home, file)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// agent.list reports enhanced mode from the adapter or native CLI a launch
// would run and the login from the home it signs in with: the owner's on a
// shared account, whose adapter it borrows like the CLI.
func TestAgentListReportsEnhancedModeAndLogin(t *testing.T) {
	t.Parallel()
	s, owner := newAgentTestServer(t)
	ctx := context.Background()
	grantee := &domain.Member{DisplayName: "grantee", TailnetLogin: "grantee@example.com", Role: domain.RoleCollaborator}
	if err := s.cfg.Store.CreateMember(ctx, grantee); err != nil {
		t.Fatal(err)
	}
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Homes = homes
	s.cfg.Runs = &fakeRuns{}
	installInHome(t, homes, grantee.ID, ".local/bin/codex", ".local/bin/codex-acp", ".codex/auth.json",
		".local/bin/omp", ".local/bin/claude")
	installInHome(t, homes, owner.ID, ".local/bin/claude", ".local/bin/claude-agent-acp", ".claude/.credentials.json")

	list := func(account domain.MemberID) map[string]protocol.AgentInfo {
		t.Helper()
		raw, err := json.Marshal(protocol.AgentListParams{AccountMemberID: string(account)})
		if err != nil {
			t.Fatal(err)
		}
		result, perr := s.agentList(ctx, grantee.ID, raw)
		if perr != nil {
			t.Fatal(perr)
		}
		agents := map[string]protocol.AgentInfo{}
		for _, agent := range result.(protocol.AgentListResult).Agents {
			agents[agent.Name] = agent
		}
		return agents
	}
	type state struct {
		enhancedInstalled, loginFound bool
		defaultMode                   string
	}
	check := func(agents map[string]protocol.AgentInfo, want map[string]state) {
		t.Helper()
		for name, w := range want {
			got := agents[name]
			if got.EnhancedInstalled != w.enhancedInstalled || got.LoginFound != w.loginFound || got.DefaultMode != w.defaultMode {
				t.Errorf("%s = enhanced_installed %v, login_found %v, default_mode %q; want %+v",
					name, got.EnhancedInstalled, got.LoginFound, got.DefaultMode, w)
			}
		}
	}
	own := list(grantee.ID)
	check(own, map[string]state{
		"codex": {true, true, "acp"},
		"omp":   {true, false, "acp"},
		// Claude stays on its terminal by default, and its adapter is the
		// owner's, not the grantee's.
		"claude":   {false, false, "tui"},
		"opencode": {false, false, "tui"},
	})
	codex, _ := harness.Lookup("codex")
	if got := own["codex"].EnhancedInstallScript; got != codex.InstallCommand(true) {
		t.Fatalf("codex enhanced install script = %q", got)
	}
	if got := own["omp"].EnhancedInstallScript; got != "" {
		t.Fatalf("native omp enhanced install script = %q, want none", got)
	}

	if err := s.cfg.Store.ShareAccount(ctx, owner.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	check(list(owner.ID), map[string]state{
		"claude": {true, true, "tui"},
		// The grantee's own codex and adapter run, but the login is the
		// owner's, who has none.
		"codex": {true, false, "acp"},
	})
}
