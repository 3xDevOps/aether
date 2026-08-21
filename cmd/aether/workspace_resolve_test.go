package main

import (
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
