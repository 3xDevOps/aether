package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// The list output must carry the workspace ID: it is what git remote URLs
// need, and list exists so users can find it without scrollback.
func TestPrintWorkspaces(t *testing.T) {
	var b strings.Builder
	err := printWorkspaces(&b, []protocol.Workspace{
		{ID: "01m0h6tym4y65102a721nq0jf3", Name: "myproject"},
		{ID: "01m0h6zzzzzzzzzzzzzzzzzzzz", Name: "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "workspace 01m0h6tym4y65102a721nq0jf3 myproject\nworkspace 01m0h6zzzzzzzzzzzzzzzzzzzz other\n"
	if b.String() != want {
		t.Fatalf("output = %q, want %q", b.String(), want)
	}
}

func TestPrintWorkspacesEmpty(t *testing.T) {
	var b strings.Builder
	if err := printWorkspaces(&b, nil); err != nil {
		t.Fatal(err)
	}
	if b.String() != "no workspaces\n" {
		t.Fatalf("output = %q, want a no-workspaces notice", b.String())
	}
}
