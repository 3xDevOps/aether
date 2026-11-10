package sshd

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// An admin cannot edit, or learn anything about, a member they do not see:
// renaming, recoloring, setting the git identity of N, or revoking N from
// wsA, answers exactly as for a member that does not exist.
func TestAdminEditsOfAnInvisibleMemberReadAsMissing(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	const missing = "mem-doesnotexist"
	n := string(w.n.ID)
	cases := []struct {
		method  string
		hidden  any
		nothing any
	}{
		{protocol.MethodMemberRename, protocol.MemberRenameParams{MemberID: n, DisplayName: "Eve"},
			protocol.MemberRenameParams{MemberID: missing, DisplayName: "Eve"}},
		{protocol.MethodMemberColor, protocol.MemberColorParams{MemberID: n, Color: "#e6194b"},
			protocol.MemberColorParams{MemberID: missing, Color: "#e6194b"}},
		{protocol.MethodMemberGit, protocol.MemberGitParams{MemberID: n, Name: "Eve", Email: "eve@example.com"},
			protocol.MemberGitParams{MemberID: missing, Name: "Eve", Email: "eve@example.com"}},
		{protocol.MethodWorkspaceAccessRevoke, protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(w.wsA.ID), MemberID: n},
			protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(w.wsA.ID), MemberID: missing}},
	}
	echo := strings.NewReplacer(n, "<id>", missing, "<id>")
	for _, tc := range cases {
		got, want := w.call(t, "A", tc.method, tc.hidden, nil), w.call(t, "A", tc.method, tc.nothing, nil)
		if got == nil || want == nil {
			t.Fatalf("A %s: hidden = %v, missing = %v; want both refused", tc.method, got, want)
		}
		g, m := wireErrOf(t, got), wireErrOf(t, want)
		if g.Code != m.Code || echo.Replace(g.Message) != echo.Replace(m.Message) {
			t.Fatalf("A %s: hidden = %d %q, missing = %d %q; want them identical", tc.method, g.Code, g.Message, m.Code, m.Message)
		}
	}
	if err := w.call(t, "A", protocol.MethodMemberRename, protocol.MemberRenameParams{MemberID: string(w.u.ID), DisplayName: "Uma"}, nil); err != nil {
		t.Fatalf("A renames U, whom A sees: %v", err)
	}
}
