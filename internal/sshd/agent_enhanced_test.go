package sshd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
// shared account. The adapter counts only in the home the CLI comes from.
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
	installInHome(t, homes, owner.ID, ".local/bin/claude", ".local/bin/claude-agent-acp", ".claude/.credentials.json",
		".local/bin/pi", ".local/bin/pi-acp")

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
		// The grantee's own claude runs, so the owner's adapter is not
		// on the launch's PATH.
		"claude": {false, true, "tui"},
		// The grantee has no pi, so the launch borrows the owner's pi and
		// its adapter together.
		"pi": {true, false, "tui"},
		// The grantee's own codex and adapter run, but the login is the
		// owner's, who has none.
		"codex": {true, false, "acp"},
	})
}

func TestAgentListLeavesHomesAloneAndSurvivesAnUnreadableLogin(t *testing.T) {
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
	if err = s.cfg.Store.ShareAccount(ctx, owner.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	list := func(account domain.MemberID) map[string]protocol.AgentInfo {
		t.Helper()
		raw, marshalErr := json.Marshal(protocol.AgentListParams{AccountMemberID: string(account)})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		result, perr := s.agentList(ctx, grantee.ID, raw)
		if perr != nil {
			t.Fatalf("agent.list on %s: %v", account, perr)
		}
		agents := map[string]protocol.AgentInfo{}
		for _, agent := range result.(protocol.AgentListResult).Agents {
			agents[agent.Name] = agent
		}
		return agents
	}

	list(owner.ID)
	for _, member := range []domain.MemberID{owner.ID, grantee.ID} {
		// homes.Path would create the home it names.
		if _, statErr := os.Lstat(filepath.Join(homes.Root(), string(member))); !os.IsNotExist(statErr) {
			t.Errorf("agent.list created %s's home: %v", member, statErr)
		}
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory")
	}
	installInHome(t, homes, grantee.ID, ".local/bin/codex", ".codex/auth.json", ".local/bin/claude", ".claude/.credentials.json")
	home, err := homes.Path(grantee.ID)
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(home, ".codex")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	agents := list(grantee.ID)
	if agents["codex"].LoginFound || !agents["codex"].Installed {
		t.Errorf("codex with an unreadable login = %+v, want installed with no login found", agents["codex"])
	}
	if !agents["claude"].LoginFound {
		t.Errorf("claude = %+v, want its readable login found", agents["claude"])
	}
}

func TestAgentInstall(t *testing.T) {
	t.Parallel()
	s, member := newAgentTestServer(t)
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Homes = homes
	runs := &fakeRuns{}
	s.cfg.Runs = runs
	installInHome(t, homes, member.ID, ".local/bin/claude")
	install := func(params protocol.AgentInstallParams) (protocol.AgentInstallResult, *protocol.Error) {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		result, perr := s.agentInstall(context.Background(), member.ID, raw)
		if perr != nil {
			return protocol.AgentInstallResult{}, perr
		}
		return result.(protocol.AgentInstallResult), nil
	}
	got, perr := install(protocol.AgentInstallParams{Name: "claude", Enhanced: true})
	if perr != nil {
		t.Fatal(perr)
	}
	want := protocol.AgentInstallResult{LogTail: "installed", Installed: true}
	if got != want {
		t.Fatalf("agent.install = %+v, want %+v", got, want)
	}
	claude, _ := harness.Lookup("claude")
	if calls := runs.Calls(); !slices.Equal(calls, []string{"agent-install:" + string(member.ID) + ":" + claude.InstallCommand(true)}) {
		t.Fatalf("RunController calls = %v", calls)
	}
	for _, refused := range []protocol.AgentInstallParams{{Name: "custom"}, {Name: "mybot"}, {Name: ""}} {
		if _, perr := install(refused); perr == nil || perr.Code != protocol.CodeInvalidParams {
			t.Errorf("agent.install %+v = %v, want invalid params", refused, perr)
		}
	}
}
