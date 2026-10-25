package main

import (
	"strings"
	"testing"

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
		"config      ", "/aether/config.json\n",
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
	for _, tc := range []struct {
		name       string
		member     protocol.Member
		workspaces []protocol.Workspace
		agents     []protocol.AgentInfo
		want       string
	}{
		{"no workspace", admin, nil, nil, "aether workspace import <path|url>"},
		{"no workspace, not an admin", protocol.Member{Role: "member"}, nil, nil, "an admin runs: aether workspace import <path|url>"},
		{"no agent installed", admin, ws, []protocol.AgentInfo{{Name: "claude"}}, "aether agent add <name>"},
		{"no login file", admin, ws, []protocol.AgentInfo{{Name: "codex"}, {Name: "claude", Installed: true, LoginFound: &missing}}, "aether agent login claude"},
		{"one agent logged in", admin, ws, []protocol.AgentInfo{{Name: "claude", Installed: true, LoginFound: &missing}, {Name: "codex", Installed: true, LoginFound: &found}}, ""},
		{"no login path declared", admin, ws, []protocol.AgentInfo{{Name: "mine", Installed: true}}, ""},
	} {
		if got := nextStep(tc.member, tc.workspaces, tc.agents); got != tc.want {
			t.Errorf("%s: next = %q, want %q", tc.name, got, tc.want)
		}
	}
}
