package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestStatusNotLinkedShowsWhatThisComputerHoldsAndTheFirstStep(t *testing.T) {
	testhome.Isolate(t)
	r, err := gatherStatus()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printStatus(&out, r)
	for _, want := range []string{
		"config      ", filepath.Join("aether", "config.json") + "\n",
		"edge        not signed in\n",
		"link        not linked\n",
		"next        aether login, then aether link --claim <code>; or aether link <host:port>\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}

func TestStatusNextStep(t *testing.T) {
	admin := protocol.Member{ID: "mem-1", Role: "admin"}
	ws := []protocol.Workspace{{ID: "ws-1", Name: "app"}}
	found, missing := true, false
	ready := []protocol.AgentInfo{{Name: "mine", Installed: true}}
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name       string
		member     protocol.Member
		workspaces []protocol.Workspace
		agents     []protocol.AgentInfo
		mirrors    map[string]protocol.WorkspaceMirrorResult
		want       string
	}{
		{"no workspace", admin, nil, nil, nil, "aether workspace import <path|url>"},
		{"no workspace, not an admin", protocol.Member{Role: "member"}, nil, nil, nil, "an admin runs: aether workspace import <path|url>"},
		{"no agent installed", admin, ws, []protocol.AgentInfo{{Name: "claude"}}, nil, "aether agent add <name>"},
		{"no login file, device login", admin, ws, []protocol.AgentInfo{{Name: "claude"}, {Name: "codex", Installed: true, LoginFound: &missing, LoginCommand: "codex login --device-auth", LoginKind: "device"}}, nil, "aether agent login codex"},
		{"no login file, interactive login", admin, ws, []protocol.AgentInfo{{Name: "codex"}, {Name: "claude", Installed: true, LoginFound: &missing, LoginCommand: "claude auth login", LoginKind: "interactive"}}, nil, "a human runs in a terminal: aether agent login claude"},
		{"no login file, custom agent", admin, ws, []protocol.AgentInfo{{Name: "mine", Installed: true, LoginFound: &missing}}, nil, "a human runs in a terminal: aether terminal, then mine's own sign-in"},
		{"one agent logged in", admin, ws, []protocol.AgentInfo{{Name: "claude", Installed: true, LoginFound: &missing}, {Name: "codex", Installed: true, LoginFound: &found}}, nil, ""},
		{"no login path declared", admin, ws, []protocol.AgentInfo{{Name: "mine", Installed: true}}, nil, ""},
		{"mirror fetched, not adopted", admin, ws, ready, map[string]protocol.WorkspaceMirrorResult{"ws-1": {Enabled: true, Status: "ready", Generation: 3, ObservedCommit: sha}}, "a human runs: aether workspace mirror adopt --workspace ws-1 --generation 3 --commit " + sha + " --yes"},
		{"mirror never fetched", admin, ws, ready, map[string]protocol.WorkspaceMirrorResult{"ws-1": {Enabled: true, Status: "auth-failed", LastError: "denied"}}, "aether workspace mirror refresh --workspace ws-1"},
		{"mirror adopted", admin, ws, ready, map[string]protocol.WorkspaceMirrorResult{"ws-1": {Enabled: true, Status: "ready", ObservedCommit: sha, AcceptedCommit: sha}}, ""},
		{"mirror fetched, not an admin", protocol.Member{Role: "member"}, ws, ready, map[string]protocol.WorkspaceMirrorResult{"ws-1": {Enabled: true, Status: "ready", Generation: 3, ObservedCommit: sha}}, "an admin runs: aether workspace mirror adopt --workspace ws-1 --generation 3 --commit " + sha + " --yes"},
		{"mirror never fetched, not an admin", protocol.Member{Role: "member"}, ws, ready, map[string]protocol.WorkspaceMirrorResult{"ws-1": {Enabled: true, Status: "auth-failed"}}, "an admin runs: aether workspace mirror refresh --workspace ws-1"},
	} {
		if got := nextStep(tc.member, tc.workspaces, tc.agents, tc.mirrors, nil); got != tc.want {
			t.Errorf("%s: next = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatusNextStepSeedsAnUnpushedBase(t *testing.T) {
	ws := []protocol.Workspace{{ID: "ws-1", Name: "app", BaseBranch: "main"}}
	ready := []protocol.AgentInfo{{Name: "mine", Installed: true}}
	for _, tc := range []struct {
		name   string
		member protocol.Member
		clone  string
		want   string
	}{
		{"clone linked", protocol.Member{Role: "admin"}, "/src/my app", "git -C '/src/my app' push aether main"},
		{"no clone", protocol.Member{Role: "admin"}, "", "aether workspace import <path>"},
		{"no clone, not an admin", protocol.Member{Role: "member"}, "", "an admin runs: aether workspace import <path>"},
	} {
		got := nextStep(tc.member, ws, ready, nil, map[string]string{"ws-1": tc.clone})
		if got != tc.want {
			t.Errorf("%s: next = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatusListsLinkedClones(t *testing.T) {
	var out strings.Builder
	printStatus(&out, statusReport{
		Link:       &statusLink{Addr: "host:2222"},
		Workspaces: []protocol.Workspace{{ID: "ws_app", Name: "app"}},
		Repos: []statusRepo{
			{LinkedRepo: cli.LinkedRepo{WorkspaceID: "ws_app", Path: "/src/app"}},
			{LinkedRepo: cli.LinkedRepo{WorkspaceID: "ws_gone", Path: "/src/old"}},
			{LinkedRepo: cli.LinkedRepo{Path: "/src/moved"}, Error: "git remote: exit status 128"},
		},
	})
	for _, want := range []string{
		"repo        /src/app -> workspace app (ws_app)\n",
		"repo        /src/old -> workspace ws_gone, not on this server\n",
		"repo        /src/moved: git remote: exit status 128\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}
