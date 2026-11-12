package sshd

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// refusedAlike asserts that two calls failed with the same wire error once
// the ids each echoes are blanked.
func refusedAlike(t *testing.T, what string, got, want error, echoed ...string) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("%s: got %v, want %v; want both refused", what, got, want)
	}
	pairs := make([]string, 0, 2*len(echoed))
	for _, id := range echoed {
		pairs = append(pairs, id, "<id>")
	}
	echo := strings.NewReplacer(pairs...)
	g, m := wireErrOf(t, got), wireErrOf(t, want)
	if g.Code != m.Code || echo.Replace(g.Message) != echo.Replace(m.Message) {
		t.Fatalf("%s: got %d %q, want %d %q; want them identical", what, g.Code, g.Message, m.Code, m.Message)
	}
}

// An admin cannot edit, or learn anything about, a member they do not see:
// renaming, recoloring, setting the git identity of N, removing N's
// sign-in, or revoking N from wsA, answers exactly as for a member that
// does not exist.
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
		{protocol.MethodMemberIdentityRemove, protocol.MemberIdentityRemoveParams{MemberID: n, Provider: "github", Subject: "1"},
			protocol.MemberIdentityRemoveParams{MemberID: missing, Provider: "github", Subject: "1"}},
		{protocol.MethodWorkspaceAccessRevoke, protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(w.wsA.ID), MemberID: n},
			protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(w.wsA.ID), MemberID: missing}},
	}
	for _, tc := range cases {
		refusedAlike(t, "A "+tc.method, w.call(t, "A", tc.method, tc.hidden, nil), w.call(t, "A", tc.method, tc.nothing, nil), n, missing)
	}
}

// Changing or disabling another member is for that member and super
// admins: an admin who sees U may still approve and read U's sign-ins, but
// not rename, recolor, set the git identity of, or remove a sign-in of U.
func TestEditingAnotherMemberIsForThemselvesOrASuperAdmin(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	u := string(w.u.ID)
	edits := []struct {
		method string
		params any
	}{
		{protocol.MethodMemberRename, protocol.MemberRenameParams{MemberID: u, DisplayName: "Uma"}},
		{protocol.MethodMemberColor, protocol.MemberColorParams{MemberID: u, Color: "#e6194b"}},
		{protocol.MethodMemberGit, protocol.MemberGitParams{MemberID: u, Name: "Uma", Email: "uma@example.com"}},
		{protocol.MethodMemberIdentityRemove, protocol.MemberIdentityRemoveParams{MemberID: u, Provider: "github", Subject: "1"}},
	}
	for _, c := range edits {
		err := w.call(t, "A", c.method, c.params, nil)
		if pe := wireErrOf(t, err); pe.Code != protocol.CodeDenied || pe.Message != c.method+": only the member themself or a super admin may do this" {
			t.Errorf("A %s of U = %v, want denied", c.method, err)
		}
	}
	for _, who := range []string{"S", "U"} {
		for _, c := range edits[:3] {
			if err := w.call(t, who, c.method, c.params, nil); err != nil {
				t.Errorf("%s %s of U: %v", who, c.method, err)
			}
		}
	}
	if err := w.call(t, "A", protocol.MethodMemberIdentityList, protocol.MemberIdentityListParams{MemberID: u}, nil); err != nil {
		t.Errorf("A reads U's sign-ins: %v", err)
	}
}

// A connector that is not the caller's, unless the caller is a super
// admin, answers connector.revoke exactly as an unknown id.
func TestRevokingAnotherMembersConnectorReadsAsUnknown(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	connectors := map[string]string{}
	for name, m := range map[string]*domain.Member{"U": w.u, "N": w.n, "B": w.b} {
		now := time.Now()
		c := &store.Connector{Member: m.ID, ClientID: "client", ClientName: "Claude", RedirectHost: "claude.ai", Resource: "https://aether.test/mcp",
			Scopes: []string{ConnectorScopeRead}, CreatedAt: now}
		if err := w.e.store.(store.ConnectorStore).CreateConnector(t.Context(), c, store.ConnectorTokens{Access: "a" + name, AccessExpiresAt: now.Add(time.Hour),
			Refresh: "r" + name, RefreshExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		connectors[name] = c.ID
	}
	const missing = "con-doesnotexist"
	revoke := func(who, id string) error {
		return w.call(t, who, protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: id}, nil)
	}
	for _, owner := range []string{"U", "N", "B"} {
		id := connectors[owner]
		refusedAlike(t, "A revokes "+owner+"'s connector", revoke("A", id), revoke("A", missing), id, missing)
	}
	if err := revoke("U", connectors["U"]); err != nil {
		t.Errorf("U revokes their own connector: %v", err)
	}
	if err := revoke("S", connectors["N"]); err != nil {
		t.Errorf("S revokes N's connector: %v", err)
	}
}

// An approval code for a device the caller may not act on answers lookup
// and approval exactly as a code no device waits with. Denying a device
// still waiting is approval; revoking an approved one is not.
func TestDeviceCodesTheCallerCannotActOnReadAsUnknown(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	ids := identities(t, w.e)
	device := func(m domain.MemberID, status domain.DeviceStatus) (*domain.Device, string) {
		t.Helper()
		subject := "subject-" + string(m)
		if _, err := ids.GetMemberByIdentity(ctx, "github", subject); err != nil {
			if err := ids.BindIdentity(ctx, &domain.Identity{Member: m, Provider: "github", Subject: subject, Login: string(m), CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		dev := &domain.Device{Member: m, Provider: "github", Subject: subject, Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()),
			Label: "laptop", Status: status}
		if status == domain.DeviceApproved {
			dev.ApprovedBy = m
		}
		if err := ids.RegisterDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		return dev, store.ApprovalCode(dev.Credential)
	}
	unknown := store.ApprovalCode(edgeproto.DeviceKeyLine(newSigner(t).PublicKey()))
	lookup := func(code string) error {
		return w.call(t, "A", protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: code}, nil)
	}
	approve := func(code string, dev domain.DeviceID) error {
		return w.call(t, "A", protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code, DeviceID: string(dev)}, nil)
	}
	for _, owner := range []*domain.Member{w.n, w.b} {
		dev, code := device(owner.ID, domain.DevicePending)
		refusedAlike(t, "A looks up "+owner.DisplayName+"'s code", lookup(code), lookup(unknown), code, unknown)
		refusedAlike(t, "A approves "+owner.DisplayName+"'s code", approve(code, dev.ID), approve(unknown, dev.ID), code, unknown)
	}
	waiting, code := device(w.u.ID, domain.DevicePending)
	if err := lookup(code); err != nil {
		t.Fatalf("A looks up U's code: %v", err)
	}
	if err := w.call(t, "A", protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: string(waiting.ID)}, nil); err != nil {
		t.Errorf("A denies U's waiting device: %v", err)
	}
	approved, _ := device(w.u.ID, domain.DeviceApproved)
	err := w.call(t, "A", protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: string(approved.ID)}, nil)
	if pe := wireErrOf(t, err); pe.Code != protocol.CodeDenied {
		t.Errorf("A revokes U's approved device = %v, want denied", err)
	}
	if err := w.call(t, "S", protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: string(approved.ID)}, nil); err != nil {
		t.Errorf("S revokes U's approved device: %v", err)
	}
}
