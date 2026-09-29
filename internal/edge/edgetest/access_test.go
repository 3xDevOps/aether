package edgetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// controlOver opens the control channel on a raw SSH connection.
func controlOver(t *testing.T, sc *ssh.Client) *protocol.Client {
	t.Helper()
	ch, reqs, err := sc.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Subsystem string }{protocol.SubsystemControl}))
	if err != nil || !ok {
		t.Fatalf("control subsystem: %v %v", ok, err)
	}
	return protocol.NewClient(ch)
}

// deniedCall asserts that method is refused on ctl with a message
// containing want.
func deniedCall(t *testing.T, ctl *protocol.Client, method string, params any, want string) {
	t.Helper()
	err := ctl.Call(method, params, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, want) {
		t.Fatalf("%s: %v, want a refusal saying %q", method, err, want)
	}
}

// Under account access an invited person signs in and works: no
// approval, and the device is recorded as registered, not approved. The
// servers list says which policy the server announced.
func TestAccountAccess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyAccount)
	cs := h.login(alice, bob, bob)
	al, bo, bo2 := cs[0], cs[1], cs[2]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	inviteLogin(t, ctl, bo, a.id, "collaborator")

	info := call[protocol.ServerInfoResult](t, h.control(bo, a), protocol.MethodServerInfo, struct{}{})
	if info.Member.Role != "collaborator" {
		t.Fatalf("bob joined as %+v", info.Member)
	}
	h.mustDial(bo2, a)
	for _, c := range []*client{bo, bo2} {
		if got := a.deviceStatus(t, c); got != domain.DeviceRegistered {
			t.Fatalf("%s's device is %q, want registered", c.user.Login, got)
		}
	}
	// The claim approved the owner's device: the code came from the
	// machine's console.
	if got := a.deviceStatus(t, al); got != domain.DeviceApproved {
		t.Fatalf("the claiming device is %q, want approved", got)
	}
	servers, err := bo.edge.Servers(context.Background())
	if err != nil || len(servers) != 1 || servers[0].AccessPolicy != edgeproto.PolicyAccount || servers[0].Kind != edgeproto.ServerSelfHosted {
		t.Fatalf("bob's servers = %+v %v", servers, err)
	}

	// A registered device approves nothing, not even under account
	// access: an approval by sign-in alone would carry over to
	// approved-devices.
	deniedCall(t, h.control(bo, a), protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: "ABCD-EFGH"},
		"needs an approved device")
}

// Under approved devices a new device waits until someone approves it:
// the same member from an approved device, an admin from an approved
// device, or the machine's administrator on the console. A waiting
// device completes no handshake, so it approves nothing and calls no
// method.
func TestApprovedDevices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, alice, bob, bob, carol, alice)
	al, al2, bo, bo2, ca, al3 := cs[0], cs[1], cs[2], cs[3], cs[4], cs[5]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	if got := a.deviceStatus(t, al); got != domain.DeviceApproved {
		t.Fatalf("the claiming device is %q, want approved", got)
	}

	// Alice's second device, approved from her first.
	_, err := h.dial(al2, h.link(a))
	code := waitingCode(t, "alice's second device", err)
	approved := approve(t, ctl, code)
	if approved.Device.Status != "approved" || approved.Device.ApprovedBy != string(a.memberOf(t, alice).ID) {
		t.Fatalf("approve: %+v", approved.Device)
	}
	h.mustDial(al2, a)

	// No first-device exception: bob's first device waits, and the
	// admin approves it from an approved device.
	inviteLogin(t, ctl, bo, a.id, "collaborator")
	_, err = h.dial(bo, h.link(a))
	code = waitingCode(t, "bob's first device", err)
	if _, err = h.tryControl(bo, a); err == nil {
		t.Fatal("bob's waiting device opened a control channel")
	}
	approve(t, ctl, code)
	h.mustDial(bo, a)

	// Bob's second device, approved on the machine.
	_, err = h.dial(bo2, h.link(a))
	a.consoleApprove(t, waitingCode(t, "bob's second device", err))
	h.mustDial(bo2, a)

	// Carol's first device waits; nothing it presents lets it approve
	// itself.
	inviteLogin(t, ctl, ca, a.id, "admin")
	_, err = h.dial(ca, h.link(a))
	carolCode := waitingCode(t, "carol's first device", err)
	for range 3 {
		if _, cerr := h.tryControl(ca, a); cerr == nil || !strings.Contains(cerr.Error(), carolCode) {
			t.Fatalf("carol's waiting device: %v, want its refusal", cerr)
		}
	}
	if got := a.deviceStatus(t, ca); got != domain.DevicePending {
		t.Fatalf("carol's device is %q after trying, want pending", got)
	}

	// An admin's new device is refused before any method runs, so an
	// unapproved credential of an admin cannot invite, change a role,
	// link an identity or transfer ownership.
	before := a.snapshot(t)
	_, err = h.dial(al3, h.link(a))
	waitingCode(t, "alice's third device", err)
	if _, err = h.tryControl(al3, a); err == nil {
		t.Fatal("alice's waiting device opened a control channel")
	}
	after := a.snapshot(t)
	if len(after.members) != len(before.members) || len(after.admins()) != len(before.admins()) {
		t.Fatalf("members changed while a waiting device tried: %+v -> %+v", before.members, after.members)
	}
}
