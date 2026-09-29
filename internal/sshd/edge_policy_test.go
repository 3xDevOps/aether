package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func mallory() edgeproto.Account {
	return edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2002", Login: "mallory", IdentityAt: time.Now()}
}

// approveAsAdmin approves code over the admin's SSH key connection.
func approveAsAdmin(t *testing.T, e *testEnv, code string) {
	t.Helper()
	if err := controlClient(t, e).Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code}, nil); err != nil {
		t.Fatalf("admin approve %s: %v", code, err)
	}
}

func deviceStatus(t *testing.T, e *testEnv, key ssh.Signer) domain.DeviceStatus {
	t.Helper()
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(key.PublicKey()))
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	return dev.Status
}

// restartWith returns a second server over e's store with policy, as the
// same host restarted after `aether-server config set edge-access`.
func (e *testEnv) restartWith(t *testing.T, policy edgeproto.AccessPolicy) *testEnv {
	t.Helper()
	srv, err := New(Config{
		Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(t.TempDir(), "host_key"),
		Store: e.store, Bus: e.bus, Git: e.git, PTY: e.pty, Runs: e.runs, EdgeAccess: policy,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	restarted := *e
	restarted.srv = srv
	return &restarted
}

// Under account access signing in admits a new device, recorded as
// registered: no person approved it.
func TestAccountAccessRegistersDevices(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	laptop, phone := newSigner(t), newSigner(t)
	laptopRPC := controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))
	e.mustDialEdge(t, octo, phone, "phone")
	for _, key := range []ssh.Signer{laptop, phone} {
		if got := deviceStatus(t, e, key); got != domain.DeviceRegistered {
			t.Fatalf("device admitted by sign-in = %s, want registered", got)
		}
	}
	// As an admin under account access, the member administers the server
	// from a registered device, but approves no device from one (rule 4):
	// switching to approved-devices must inherit nothing.
	if err := laptopRPC.Call(protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Provider: "github", Login: "someone", Role: "viewer"}, nil); err != nil {
		t.Fatalf("invitation from a registered admin device under account access: %v", err)
	}
	code := store.ApprovalCode(edgeproto.DeviceKeyLine(phone.PublicKey()))
	var pe *protocol.Error
	err := laptopRPC.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied || !strings.Contains(pe.Message, "needs an approved device") {
		t.Fatalf("approval from a registered device = %v, want CodeDenied", err)
	}
	if got := deviceStatus(t, e, phone); got != domain.DeviceRegistered {
		t.Fatalf("phone = %s after a refused approval", got)
	}
	approveAsAdmin(t, e, code)
	if got := deviceStatus(t, e, phone); got != domain.DeviceApproved {
		t.Fatalf("phone = %s after the admin's approval, want approved", got)
	}
}

// Switching to approved-devices changes no device's status, and a
// registered device is refused like a pending one until someone approves
// it (rule 13).
func TestTighteningInheritsNothing(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	laptop := newSigner(t)
	e.mustDialEdge(t, octo, laptop, "laptop")

	strict := e.restartWith(t, edgeproto.PolicyApprovedDevices)
	_, banner, err := strict.dialEdge(t, grantFor(octo, laptop, "laptop"), laptop, "aether")
	if err == nil || !strings.Contains(banner, "admits approved devices only") {
		t.Fatalf("registered device under approved-devices = %v, banner %q", err, banner)
	}
	if got := deviceStatus(t, e, laptop); got != domain.DeviceRegistered {
		t.Fatalf("switching policy changed the device to %s", got)
	}
	// The admin's SSH key is independent of the edge and keeps working.
	approveAsAdmin(t, e, approvalCode(t, banner))
	strict.mustDialEdge(t, octo, laptop, "laptop")
}

// An approved key names its member's identity: a grant naming another
// account with that key is refused (rule 2), even another identity of
// the same member.
func TestApprovedKeyNamesItsIdentity(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "mallory", Role: domain.RoleAdmin})
	laptop := newSigner(t)
	approveAsAdmin(t, e, e.refusedForApproval(t, octo, laptop, "laptop"))
	e.mustDialEdge(t, octo, laptop, "laptop")
	e.mustRefuseEdge(t, mallory(), laptop, "laptop", "registered to another account")

	google := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-1", Email: "octo@example.com", IdentityAt: time.Now()}
	m, err := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject)
	if err != nil {
		t.Fatal(err)
	}
	inviteAccount(t, e, domain.Invitation{Provider: "google", Email: "octo@example.com", Member: m.ID})
	e.mustRefuseEdge(t, google, laptop, "laptop", "registered to another account")
}

// Linking an identity to an existing member creates no approved device:
// the member approves the device from their key or tailnet connection
// (rule 6).
func TestLinkedIdentityCreatesNoDevice(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ada := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "3003", Login: "ada", Name: "Ada", IdentityAt: time.Now()}
	admin := controlClient(t, e)
	if err := admin.Call(protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Provider: "github", Login: "ada"}, nil); err != nil {
		t.Fatalf("link: %v", err)
	}
	laptop := newSigner(t)
	code := e.refusedForApproval(t, ada, laptop, "laptop")
	if m, err := identities(t, e).GetMemberByIdentity(context.Background(), ada.Provider, ada.Subject); err != nil || m.ID != e.member.ID {
		t.Fatalf("link bound %+v, %v; want the admin", m, err)
	}
	if err := admin.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code}, nil); err != nil {
		t.Fatalf("approve from the key connection: %v", err)
	}
	if m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, ada, laptop, "laptop"))); m.ID != string(e.member.ID) {
		t.Fatalf("linked device connects as %s", m.ID)
	}
}

// withDevice is the context of a connection that signed in with dev.
func withDevice(member domain.MemberID, dev *domain.Device) context.Context {
	return context.WithValue(context.Background(), connIdentityKey{}, connIdentity{member: member, device: dev.ID, deviceKey: dev.Credential})
}

// Under approved-devices a connection whose only credential is a device
// awaiting approval authorizes nothing that raises privilege or admits a
// credential. The handshake already refuses such a device; this is the
// check each method makes itself.
func TestPrivilegedMethodsRefuseAnUnapprovedDevice(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	laptop := newSigner(t)
	e.refusedForApproval(t, octo, laptop, "laptop")
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	calls := map[string]any{
		protocol.MethodMemberDeviceApprove:    protocol.MemberDeviceApproveParams{Code: dev.ApprovalCode},
		protocol.MethodMemberInvitationCreate: protocol.MemberInvitationCreateParams{Provider: "github", Login: "x", Role: "admin"},
		protocol.MethodMemberIdentityLink:     protocol.MemberIdentityLinkParams{Provider: "github", Login: "x"},
		protocol.MethodMemberRole:             protocol.MemberRoleParams{MemberID: string(e.member.ID), Role: "viewer"},
		protocol.MethodMemberApprove:          protocol.MemberApproveParams{MemberID: string(e.member.ID)},
		protocol.MethodMemberInvite:           protocol.MemberInviteParams{},
		protocol.MethodServerOwnerTransfer:    protocol.ServerOwnerTransferParams{MemberID: string(e.member.ID)},
	}
	for method, params := range calls {
		raw, _ := json.Marshal(params)
		_, perr := e.srv.dispatch(withDevice(dev.Member, dev), dev.Member, method, raw)
		if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "needs an approved device") {
			t.Errorf("%s from a pending device = %v, want CodeDenied", method, perr)
		}
	}
	if got := deviceStatus(t, e, laptop); got != domain.DevicePending {
		t.Fatalf("the pending device approved itself: %s", got)
	}
}

// No control method reaches the access policy: every registered method is
// called as an admin with parameters naming the setting, and a new device
// still waits for approval afterwards.
func TestNoControlMethodChangesEdgeAccess(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	params := json.RawMessage(`{"key":"edge-access","value":"account","edge_access":"account","access_policy":"account",` +
		`"policy":"account","edge-device-approval":false,"member_id":"` + string(e.member.ID) + `"}`)
	for _, method := range slices.Sorted(maps.Keys(methodHandlers)) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = e.srv.dispatch(ctx, e.member.ID, method, params)
		cancel()
	}
	if e.srv.cfg.EdgeAccess != edgeproto.PolicyApprovedDevices {
		t.Fatalf("policy = %q after every control method", e.srv.cfg.EdgeAccess)
	}
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleViewer})
	e.refusedForApproval(t, octo, newSigner(t), "laptop")
}

// The edge's account-deleted notice only takes access away: the identity
// and its devices go, their connections close, and the member, its role
// and its SSH key stay.
func TestEdgeAccountDeletedOnlyRemovesAccess(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	laptop := newSigner(t)
	live := e.mustDialEdge(t, octo, laptop, "laptop")
	m := serverInfoMember(t, controlClientOn(t, live))

	if err := e.srv.EdgeAccountDeleted(context.Background(), octo.Provider, octo.Subject); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, live)
	after, err := e.store.GetMember(context.Background(), domain.MemberID(m.ID))
	if err != nil || after.Role != domain.RoleAdmin {
		t.Fatalf("member after the notice = %+v, %v; want kept as admin", after, err)
	}
	if devs, _ := identities(t, e).ListDevices(context.Background(), after.ID); len(devs) != 0 {
		t.Fatalf("devices after the notice = %+v", devs)
	}
	e.mustRefuseEdge(t, octo, laptop, "laptop", "not a member of this server")
	serverInfoMember(t, controlClient(t, e))
	if err := e.srv.EdgeAccountDeleted(context.Background(), edgeproto.ProviderGoogle, "nobody"); err != nil {
		t.Fatalf("notice for an unknown account: %v", err)
	}
	if members, _ := e.store.ListMembers(context.Background()); len(members) != 2 {
		t.Fatalf("members after the notices = %d, want 2", len(members))
	}
}

// A device revoked by another process, such as `aether-server device
// review`, loses its live connection at the next revalidation.
func TestDeviceRevokedElsewhereClosesItsConnections(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) {
		c.EdgeAccess = edgeproto.PolicyAccount
		c.revalidateInterval = 20 * time.Millisecond
	})
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop := newSigner(t)
	live := e.mustDialEdge(t, octo, laptop, "laptop")
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	if err := identities(t, e).RevokeDevice(context.Background(), dev.ID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, live)
}

type fakeEdgeOwner struct {
	mu     sync.Mutex
	owners []edgeproto.Account
	err    error
}

func (f *fakeEdgeOwner) TransferOwner(owner edgeproto.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.owners = append(f.owners, owner)
	return nil
}

func TestServerOwnerTransfer(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	admin := controlClient(t, e)
	transfer := func(member string) error {
		return admin.Call(protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: member}, nil)
	}
	var pe *protocol.Error
	if err := transfer(string(e.member.ID)); !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("transfer without an edge = %v, want CodeUnavailable", err)
	}
	owner := &fakeEdgeOwner{}
	e.srv.SetEdgeOwner(owner)

	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, newSigner(t), "laptop")))
	if err := transfer(m.ID); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("transfer to a collaborator = %v, want CodeInvalidState", err)
	}
	if got, _ := e.store.GetMember(context.Background(), domain.MemberID(m.ID)); got.Role != domain.RoleCollaborator {
		t.Fatalf("a refused transfer changed the role to %s", got.Role)
	}
	if err := transfer(string(e.member.ID)); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("transfer to an admin without an edge identity = %v, want CodeInvalidState", err)
	}
	if err := admin.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: m.ID, Role: "admin"}, nil); err != nil {
		t.Fatal(err)
	}
	var res protocol.ServerOwnerTransferResult
	if err := admin.Call(protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: m.ID}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Subject != octo.Subject || len(owner.owners) != 1 || owner.owners[0].Subject != octo.Subject {
		t.Fatalf("transfer = %+v, edge told %+v", res, owner.owners)
	}
	owner.err = errors.New("not connected")
	if err := transfer(m.ID); !errors.As(err, &pe) || !strings.Contains(pe.Message, "not connected") {
		t.Fatalf("transfer the edge refused = %v, want its error", err)
	}
}
