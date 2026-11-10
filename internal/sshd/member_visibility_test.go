package sshd

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const noMember = "mem-doesnotexist"

func (w *scopeWorld) visibleNames(t *testing.T, who string, names map[string]string) []string {
	t.Helper()
	var res protocol.MemberListResult
	if err := w.call(t, who, protocol.MethodMemberList, struct{}{}, &res); err != nil {
		t.Fatalf("%s member.list: %v", who, err)
	}
	var out []string
	for _, m := range res.Members {
		out = append(out, names[m.ID])
	}
	slices.Sort(out)
	return out
}

func TestMemberVisibilityFollowsWorkspaceAccess(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	e := w.e
	ctx := context.Background()
	_, p := addMember(t, e, "P", domain.RoleCollaborator, true)
	members := map[string]*domain.Member{"S": e.member, "A": w.a, "B": w.b, "U": w.u, "N": w.n, "P": p}
	names := map[string]string{}
	for name, m := range members {
		names[string(m.ID)] = name
	}

	for who, want := range map[string][]string{
		"S": {"A", "B", "N", "P", "S", "U"},
		"A": {"A", "P", "S", "U"},
		// A's run reaches into wsB, and U launched there before losing it.
		"B": {"A", "B", "P", "S", "U"},
		"U": {"A", "S", "U"},
		"N": {"N", "S"},
	} {
		if got := w.visibleNames(t, who, names); !slices.Equal(got, want) {
			t.Errorf("%s member.list = %v, want %v", who, got, want)
		}
	}

	ids := identities(t, e)
	for name, m := range members {
		identity := &domain.Identity{Member: m.ID, Provider: "github", Subject: "sub-" + name, Login: "login-" + name}
		if err := ids.BindIdentity(ctx, identity); err != nil {
			t.Fatal(err)
		}
		dev := &domain.Device{Member: m.ID, Provider: "github", Subject: identity.Subject, Login: identity.Login,
			Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()), Label: name, Status: domain.DeviceApproved}
		if err := ids.RegisterDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
	}
	for label, creator := range map[string]*domain.Member{"invA": w.a, "invB": w.b} {
		inv := &domain.Invitation{Provider: "github", Login: "login-" + label, Role: domain.RoleCollaborator, CreatedBy: creator.ID,
			ExpiresAt: time.Now().Add(time.Hour), SecretHash: edgeproto.HashToken(edgeproto.NewJoinSecret())}
		if err := ids.CreateInvitation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		dev := &domain.Device{Invitation: inv.ID, Provider: "github", Subject: "sub-" + label, Login: inv.Login,
			Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()), Label: label, Status: domain.DevicePending}
		if err := ids.RegisterInvitationDevice(ctx, dev, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for who, want := range map[string][]string{
		"S": {"A", "B", "N", "P", "S", "U", "invA", "invB"},
		"A": {"A", "P", "U", "invA"},
		"B": {"B", "P", "U", "invB"},
		"U": {"U"},
		"N": {"N"},
	} {
		var res protocol.MemberDeviceListResult
		if err := w.call(t, who, protocol.MethodMemberDeviceList, struct{}{}, &res); err != nil {
			t.Fatalf("%s member.device.list: %v", who, err)
		}
		var got []string
		for _, d := range res.Devices {
			got = append(got, d.Label)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s member.device.list = %v, want %v", who, got, want)
		}
	}

	identity := func(id string) map[string]any {
		return map[string]any{
			protocol.MethodMemberIdentityList:   protocol.MemberIdentityListParams{MemberID: id},
			protocol.MethodMemberIdentityRemove: protocol.MemberIdentityRemoveParams{MemberID: id, Provider: "github", Subject: "sub"},
		}
	}
	for who, hidden := range map[string][]*domain.Member{"A": {w.b, w.n}, "U": {w.b, w.n, p}, "N": {w.a, w.u}} {
		for _, m := range hidden {
			for method, params := range identity(string(m.ID)) {
				w.sameAsMissing(t, who, method, params, identity(noMember)[method])
			}
			w.sameAsMissing(t, who, protocol.MethodAccountShare,
				protocol.AccountMemberParams{MemberID: string(m.ID)}, protocol.AccountMemberParams{MemberID: noMember})
		}
	}
	grant := func(id string) protocol.WorkspaceAccessGrantParams {
		return protocol.WorkspaceAccessGrantParams{WorkspaceID: string(w.wsA.ID), MemberIDs: []string{id}}
	}
	w.sameAsMissing(t, "A", protocol.MethodWorkspaceAccessGrant, grant(string(w.n.ID)), grant(noMember))
	w.sameAsMissing(t, "A", protocol.MethodWorkspaceAccessGrant, grant(string(w.b.ID)), grant(noMember))
	if err := w.call(t, "U", protocol.MethodAccountShare, protocol.AccountMemberParams{MemberID: string(w.a.ID)}, nil); err != nil {
		t.Fatalf("U shares with A, whom U sees: %v", err)
	}
	if err := w.call(t, "S", protocol.MethodWorkspaceAccessGrant, grant(string(w.n.ID)), nil); err != nil {
		t.Fatalf("S grants N, whom only S sees: %v", err)
	}
	if got := w.visibleNames(t, "A", names); !slices.Contains(got, "N") {
		t.Fatalf("A member.list after N's grant on wsA = %v, want N", got)
	}
}

// A member who launched a session in a workspace and then lost it stays
// visible there: the workspace's history names them.
func TestMemberVisibilityKeepsRevokedRunAuthor(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	e := w.e
	ctx := context.Background()
	signer, v := addMember(t, e, "V", domain.RoleCollaborator, false)
	w.clients["V"] = controlAs(t, e, signer)
	for _, m := range []domain.MemberID{v.ID, w.n.ID} {
		if err := e.db.GrantWorkspace(ctx, w.wsB.ID, m, w.b.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.CreateRun(ctx, &domain.Run{WorkspaceID: w.wsB.ID, MemberID: w.n.ID, Task: "n's task", Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := w.call(t, "B", protocol.MethodWorkspaceAccessRevoke,
		protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(w.wsB.ID), MemberID: string(w.n.ID)}, nil); err != nil {
		t.Fatalf("B revokes N: %v", err)
	}
	names := map[string]string{string(w.n.ID): "N", string(w.a.ID): "A"}
	for who, want := range map[string]bool{"B": true, "V": true, "A": false} {
		got := w.visibleNames(t, who, names)
		if slices.Contains(got, "N") != want {
			t.Errorf("%s member.list names %v; want N listed = %v", who, got, want)
		}
	}
	if got := w.visibleNames(t, "N", names); slices.Contains(got, "A") {
		t.Errorf("N, who reaches nothing, member.list names %v; want no A", got)
	}
}
