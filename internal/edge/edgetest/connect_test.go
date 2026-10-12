package edgetest

import (
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// A server whose first admin joined with an SSH key has members and no
// owner, and the edge has not recorded it, so it refuses invitations.
// The admin issues themself a claim code over their key connection and
// claims with it through the edge: the account is bound to their member,
// the device approved and the server owned, with no member added. A
// collaborator gets no code, a pending device opens no control channel,
// and an owned server issues none.
func TestAdminConnectsTheirAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	admin, adminKey := keyMember(t, a, "Admin", domain.RoleAdmin)
	_, collabKey := keyMember(t, a, "Collab", domain.RoleCollaborator)
	sc, err := dialKey(a, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close() //nolint:errcheck // test client
	ctl := controlOver(t, sc)
	inv := protocol.MemberInvitationCreateParams{Login: bob.Login, Role: "collaborator"}
	var pe *protocol.Error
	if err = ctl.Call(protocol.MethodMemberInvitationCreate, inv, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState ||
		!strings.Contains(pe.Message, "aether edge connect") {
		t.Fatalf("invitation on a server the edge has not recorded = %v, want CodeInvalidState", err)
	}
	csc, err := dialKey(a, collabKey)
	if err != nil {
		t.Fatal(err)
	}
	defer csc.Close() //nolint:errcheck // test client
	if err = controlOver(t, csc).Call(protocol.MethodServerEdgeClaimCode, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator's claim code = %v, want CodeDenied", err)
	}
	before := a.snapshot(t)

	cs := h.login(alice, alice)
	al, al2 := cs[0], cs[1]
	code := call[protocol.ServerEdgeClaimCodeResult](t, ctl, protocol.MethodServerEdgeClaimCode, struct{}{}).Code
	res, err := h.claim(al, code)
	if err != nil || res.Info.Member.ID != string(admin.ID) || res.Info.Member.Role != string(domain.RoleAdmin) {
		t.Fatalf("claim with the admin's code: %+v %v; want the admin %s", res.Info.Member, err, admin.ID)
	}
	h.waitEdgeOwner(t, a, &alice)
	if a.deviceStatus(t, al) != domain.DeviceApproved {
		t.Fatal("the claiming device is not approved")
	}
	if after := a.snapshot(t); len(after.members) != len(before.members) || len(after.admins()) != 1 {
		t.Fatalf("members %v -> %v; want the same members and roles", before.members, after.members)
	}
	invite(t, ctl, inv)

	_, err = h.dial(al2, h.link(a))
	waitingCode(t, "alice's second device", err)
	if err = ctl.Call(protocol.MethodServerEdgeClaimCode, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState ||
		!strings.Contains(pe.Message, "already owned at its edge by github alice") {
		t.Fatalf("claim code on an owned server = %v, want CodeInvalidState", err)
	}
}
