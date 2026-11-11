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
		{ID: "01m0h6tym4y65102a721nq0jf3", Name: "myproject", OwnerMemberID: "mem-1", Role: "admin"},
		{ID: "01m0h6zzzzzzzzzzzzzzzzzzzz", Name: "other", OwnerMemberID: "mem-2", Role: "collaborator"},
	}, map[string]string{"mem-1": "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	want := "ID                          NAME       OWNER  ROLE\n" +
		"01m0h6tym4y65102a721nq0jf3  myproject  Ada    admin\n" +
		"01m0h6zzzzzzzzzzzzzzzzzzzz  other      mem-2  collaborator\n"
	if b.String() != want {
		t.Fatalf("output = %q, want %q", b.String(), want)
	}
}

func TestPrintWorkspacesEmpty(t *testing.T) {
	var b strings.Builder
	if err := printWorkspaces(&b, nil, nil); err != nil {
		t.Fatal(err)
	}
	if b.String() != "no workspaces\n" {
		t.Fatalf("output = %q, want a no-workspaces notice", b.String())
	}
}

func TestPrintWorkspaceAccess(t *testing.T) {
	var b strings.Builder
	err := printWorkspaceAccess(&b, protocol.WorkspaceAccessResult{
		OwnerMemberID: "mem-1",
		Grants:        []protocol.WorkspaceGrant{{MemberID: "mem-2", GrantedBy: "mem-1", CreatedAt: "2026-11-10T10:00:00Z"}},
	}, map[string]string{"mem-1": "Ada", "mem-2": "Bo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mem-1   Ada   owner", "mem-2   Bo    grant   Ada         2026-11-10T10:00:00Z"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
}
