package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// The git remote URL and every wire call carry workspace IDs; a user-supplied
// name must resolve to the ID or pushes fail with "store: not found" on the
// server (the sshd pack path resolves by ID only).
func TestWorkspaceIDIn(t *testing.T) {
	list := []protocol.Workspace{
		{ID: "01m0h6tym4y65102a721nq0jf3", Name: "myproject"},
		{ID: "01m0h6zzzzzzzzzzzzzzzzzzzz", Name: "other"},
	}

	for _, in := range []string{"myproject", "01m0h6tym4y65102a721nq0jf3"} {
		got, err := workspaceIDIn(list, in)
		if err != nil {
			t.Fatalf("workspaceIDIn(%q) error: %v", in, err)
		}
		if got != "01m0h6tym4y65102a721nq0jf3" {
			t.Fatalf("workspaceIDIn(%q) = %q, want the workspace ID", in, got)
		}
	}

	if _, err := workspaceIDIn(list, "missing"); err == nil {
		t.Fatal("workspaceIDIn(missing) succeeded, want an error")
	}
}

// Names are not unique. Resolving one that two workspaces share must
// refuse rather than pick whichever came back first, because the pick
// decides where a run lands.
func TestWorkspaceIDInRefusesAnAmbiguousName(t *testing.T) {
	list := []protocol.Workspace{
		{ID: "01m0aaaaaaaaaaaaaaaaaaaaaa", Name: "shared"},
		{ID: "01m0bbbbbbbbbbbbbbbbbbbbbb", Name: "shared"},
	}
	_, err := workspaceIDIn(list, "shared")
	if err == nil {
		t.Fatal("ambiguous name resolved, want an error naming both candidates")
	}
	for _, want := range []string{"01m0aaaaaaaaaaaaaaaaaaaaaa", "01m0bbbbbbbbbbbbbbbbbbbbbb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name candidate %s", err, want)
		}
	}
}

// An ID is exact, so it wins even when some other workspace took that
// string as its name.
func TestWorkspaceIDInPrefersAnExactID(t *testing.T) {
	list := []protocol.Workspace{
		{ID: "01m0bbbbbbbbbbbbbbbbbbbbbb", Name: "01m0aaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "01m0aaaaaaaaaaaaaaaaaaaaaa", Name: "real"},
	}
	got, err := workspaceIDIn(list, "01m0aaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("workspaceIDIn: %v", err)
	}
	if got != "01m0aaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("resolved to %q, want the workspace whose ID it is", got)
	}
}
