package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
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
	if err := approveByCode(controlClient(t, e), code, nil); err != nil {
		t.Fatalf("admin approve %s: %v", code, err)
	}
}

// approveByCode approves the device code names as clients do: it looks
// the code up, then approves the device the lookup named.
func approveByCode(c *protocol.Client, code string, out any) error {
	var found protocol.MemberDeviceLookupResult
	if err := c.Call(protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: code}, &found); err != nil {
		return err
	}
	return c.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code, DeviceID: found.Device.ID}, out)
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
	e.invite(t, laptopRPC, protocol.MethodMemberInvitationCreate, protocol.MemberInvitationCreateParams{Login: "someone", Role: "viewer"})
	code := store.ApprovalCode(edgeproto.DeviceKeyLine(phone.PublicKey()))
	var pe *protocol.Error
	err := approveByCode(laptopRPC, code, nil)
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

// Every relayed connection with a new key records a device, so the
// devices waiting for approval are bounded per invitation and per
// account: an edge relaying fresh keys cannot grow the store without
// limit. Approving one makes room again.
func TestWaitingDevicesAreBounded(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	codes := make([]string, store.MaxWaitingDevices)
	for i := range codes {
		codes[i] = e.refusedForApproval(t, octo, newSigner(t), fmt.Sprintf("key-%d", i))
	}
	e.mustRefuseEdge(t, octo, newSigner(t), "one-more", "devices of github account octo are waiting for approval on this server already")
	approveAsAdmin(t, e, codes[0])
	for i := range store.MaxWaitingDevices {
		e.refusedForApproval(t, octo, newSigner(t), fmt.Sprintf("member-key-%d", i))
	}
	e.mustRefuseEdge(t, octo, newSigner(t), "one-more", "devices of github account octo are waiting for approval on this server already")
	devs, err := identities(t, e).ListDevices(context.Background(), "")
	if err != nil || len(devs) != store.MaxWaitingDevices+1 {
		t.Fatalf("devices = %d, %v; want the approved one and %d pending", len(devs), err, store.MaxWaitingDevices)
	}
	approveAsAdmin(t, e, store.ApprovalCode(devs[1].Credential))
	e.refusedForApproval(t, octo, newSigner(t), "after-approval")
}

// An invite code admits an SSH key on the direct path whatever edge-access
// says, so signing in alone never mints one: under account access a
// registered admin device is refused, and the admin's SSH key is not.
func TestSignInAloneMintsNoInviteCode(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "invites")
	e := newTestEnv(t, func(c *Config) {
		c.EdgeAccess = edgeproto.PolicyAccount
		c.InvitesDir = dir
	})
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	laptop := newSigner(t)
	var pe *protocol.Error
	err := controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop")).Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied || !strings.Contains(pe.Message, "needs an approved device") {
		t.Fatalf("invite code from a registered device = %v, want CodeDenied", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("a refused call left %d invite codes", len(left))
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, nil); err != nil {
		t.Fatalf("invite code from the admin's SSH key: %v", err)
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
	_, banner, err := strict.dialEdge(t, grantFor(octo, laptop, "laptop"), laptop, strict.edgeUser(t, octo))
	if err == nil || !strings.Contains(banner, "admits approved devices only") {
		t.Fatalf("registered device under approved-devices = %v, banner %q", err, banner)
	}
	if got := deviceStatus(t, e, laptop); got != domain.DeviceRegistered {
		t.Fatalf("switching policy changed the device to %s", got)
	}
	// The admin's SSH key is independent of the edge and keeps working.
	approveAsAdmin(t, e, strict.approvalCode(t, laptop, banner))
	strict.mustDialEdge(t, octo, laptop, "laptop")
}

// The account a device signed in as decides the member approving it
// admits it as, and a compromised edge chooses that account for a key it
// relays. The banner names the account, member.device.lookup shows the
// approver the member and role before anything is committed, and
// member.device.approve approves only the device the lookup named.
func TestApprovalShowsWhomTheCodeAdmits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	approveAsAdmin(t, e, e.refusedForApproval(t, octo, newSigner(t), "laptop"))
	octoMember, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject)
	if err != nil {
		t.Fatal(err)
	}
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: mallory().Login, Role: domain.RoleCollaborator})
	admin := controlClient(t, e)

	// A newcomer's key relayed under the admin's account.
	stray := newSigner(t)
	_, banner, err := e.dialEdge(t, grantFor(octo, stray, "mallory-laptop"), stray, e.edgeUser(t, octo))
	if err == nil || !strings.Contains(banner, `device "mallory-laptop", signed in as github account octo, is waiting for approval`) {
		t.Fatalf("stray key under the admin's account: %v, banner %q", err, banner)
	}
	code := e.approvalCode(t, stray, banner)
	var found protocol.MemberDeviceLookupResult
	if err := admin.Call(protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: code}, &found); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if found.MemberID != string(octoMember.ID) || found.DisplayName != octoMember.DisplayName || found.Role != string(domain.RoleAdmin) ||
		found.Device.Label != "mallory-laptop" || found.Device.Provider != "github" || found.Device.Account != "octo" {
		t.Fatalf("lookup = %+v, want the admin octo's member and role", found)
	}

	// mallory's own device, waiting on her invitation, admits a new
	// collaborator.
	invitee := newSigner(t)
	invitedCode := e.refusedForApproval(t, mallory(), invitee, "mallory-laptop")
	var invited protocol.MemberDeviceLookupResult
	if err := admin.Call(protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: invitedCode}, &invited); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if invited.MemberID != "" || invited.Role != string(domain.RoleCollaborator) || invited.Device.InvitationID == "" || invited.Device.Account != "mallory" {
		t.Fatalf("lookup of the invitation's device = %+v, want a new collaborator", invited)
	}

	// An approval naming another device than the code's commits nothing.
	var pe *protocol.Error
	if err := admin.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code, DeviceID: invited.Device.ID}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeConflict || !strings.Contains(pe.Message, "nothing was approved") {
		t.Fatalf("approve %s naming device %q = %v, want a conflict", code, invited.Device.ID, err)
	}
	if deviceStatus(t, e, stray) != domain.DevicePending || deviceStatus(t, e, invitee) != domain.DevicePending {
		t.Fatal("a refused approval approved a device")
	}
	var approved protocol.MemberDeviceResult
	if err := admin.Call(protocol.MethodMemberDeviceApprove,
		protocol.MemberDeviceApproveParams{Code: invitedCode, DeviceID: invited.Device.ID}, &approved); err != nil {
		t.Fatalf("approve the looked-up device: %v", err)
	}
	if m, err := identities(t, e).GetMemberByIdentity(ctx, mallory().Provider, mallory().Subject); err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("mallory after approval = %+v, %v; want a collaborator", m, err)
	}
}

// A refusal carries the approval code only where nobody can approve the
// device in the app: the device admits as the only admin, who has no
// approved device. A device waiting on an invitation adding a new member,
// or one admitting as an admin beside another, names only its id.
func TestApprovalCodeOnlyWithoutAnInAppApprover(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	refused := func(account edgeproto.Account, label string) *edgeproto.Refused {
		t.Helper()
		signer := newSigner(t)
		_, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, e.edgeUser(t, account))
		r, ok := edgeproto.ParseRefusedBanner(banner)
		if err == nil || !ok || r.Kind != edgeproto.RefusedDevicePending || r.DeviceID == "" {
			t.Fatalf("%s: %v, banner %q; want a device waiting for approval", label, err, banner)
		}
		return r
	}
	ada := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "3003", Login: "ada", Name: "Ada", IdentityAt: time.Now()}
	e.invite(t, controlClient(t, e), protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Login: "ada"})
	if r := refused(ada, "laptop"); r.ApprovalCode == "" {
		t.Fatalf("the only admin's own device = %+v, want its code", r)
	}
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: mallory().Login, Role: domain.RoleAdmin})
	if r := refused(mallory(), "mallory-laptop"); r.ApprovalCode != "" {
		t.Fatalf("a device adding a new member = %+v, want no code", r)
	}
	addMember(t, e, "Bob", domain.RoleAdmin, false)
	if r := refused(ada, "phone"); r.ApprovalCode != "" {
		t.Fatalf("an admin's device beside another admin = %+v, want no code", r)
	}
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

	second := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "5005", Login: "octo-alt", Email: "octo@example.com", IdentityAt: time.Now()}
	m, err := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject)
	if err != nil {
		t.Fatal(err)
	}
	inviteAccount(t, e, domain.Invitation{Provider: "github", Email: "octo@example.com", Member: m.ID})
	e.mustRefuseEdge(t, second, laptop, "laptop", "registered to another account")
}

// Linking an identity to an existing member binds nothing until the
// device that signs in with it is approved: the member approves it from
// their key or tailnet connection (rule 6).
func TestLinkedIdentityCreatesNoDevice(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ada := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "3003", Login: "ada", Name: "Ada", IdentityAt: time.Now()}
	admin := controlClient(t, e)
	e.invite(t, admin, protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Login: "ada"})
	laptop := newSigner(t)
	code := e.refusedForApproval(t, ada, laptop, "laptop")
	if m, err := identities(t, e).GetMemberByIdentity(context.Background(), ada.Provider, ada.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a waiting device bound the link to %+v, %v; want nothing bound", m, err)
	}
	if err := approveByCode(admin, code, nil); err != nil {
		t.Fatalf("approve from the key connection: %v", err)
	}
	if m, err := identities(t, e).GetMemberByIdentity(context.Background(), ada.Provider, ada.Subject); err != nil || m.ID != e.member.ID {
		t.Fatalf("link bound %+v, %v; want the admin", m, err)
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
	approveAsAdmin(t, e, e.refusedForApproval(t, octo, newSigner(t), "phone"))
	laptop := newSigner(t)
	e.refusedForApproval(t, octo, laptop, "laptop")
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	calls := map[string]any{
		protocol.MethodMemberDeviceLookup:     protocol.MemberDeviceLookupParams{Code: dev.ApprovalCode},
		protocol.MethodMemberDeviceApprove:    protocol.MemberDeviceApproveParams{Code: dev.ApprovalCode, DeviceID: string(dev.ID)},
		protocol.MethodMemberInvitationCreate: protocol.MemberInvitationCreateParams{Login: "x", Role: "admin"},
		protocol.MethodMemberIdentityLink:     protocol.MemberIdentityLinkParams{Login: "x"},
		protocol.MethodMemberRole:             protocol.MemberRoleParams{MemberID: string(e.member.ID), Role: "viewer"},
		protocol.MethodMemberApprove:          protocol.MemberApproveParams{MemberID: string(e.member.ID)},
		protocol.MethodMemberInvite:           protocol.MemberInviteParams{},
		protocol.MethodServerOwnerTransfer:    protocol.ServerOwnerTransferParams{MemberID: string(e.member.ID)},
		protocol.MethodMemberMergeStart:       struct{}{},
		protocol.MethodMemberMergePreview:     protocol.MemberMergePreviewParams{Code: "AAAA-BBBB-CCCC-DDDD"},
		protocol.MethodMemberMergeConfirm:     protocol.MemberMergeConfirmParams{Code: "AAAA-BBBB-CCCC-DDDD"},
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
	if err := e.srv.EdgeAccountDeleted(context.Background(), edgeproto.ProviderGitHub, "999999"); err != nil {
		t.Fatalf("notice for an unknown account: %v", err)
	}
	if members, _ := e.store.ListMembers(context.Background()); len(members) != 2 {
		t.Fatalf("members after the notices = %d, want 2", len(members))
	}
	// The member kept no credential, and an admin still changes its role.
	if err := controlClient(t, e).Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: m.ID, Role: "viewer"}, nil); err != nil {
		t.Fatalf("demote the member left without a credential: %v", err)
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
	// owner is what Owner returns; nil is a server without one.
	owner *edgeproto.Account
	// state is what EdgeState returns; empty is an edge that has not
	// answered.
	state string
	// issuedFor is the admin of the last IssueClaimCode.
	issuedFor domain.MemberID
}

func (f *fakeEdgeOwner) IssueClaimCode(admin domain.MemberID) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issuedFor = admin
	return testClaimCode, time.Now().Add(edgeproto.ClaimCodeTTL), nil
}

func (f *fakeEdgeOwner) Owner() (*edgeproto.Account, error) { return f.owner, nil }
func (f *fakeEdgeOwner) Origin() string                     { return "https://edge.example" }
func (f *fakeEdgeOwner) Connected() bool                    { return true }
func (f *fakeEdgeOwner) EdgeState() string                  { return f.state }

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
	// With two GitHub identities, the admin removes the one ownership
	// should not go to.
	owner.err = nil
	if err := identities(t, e).BindIdentity(context.Background(), &domain.Identity{Member: domain.MemberID(m.ID),
		Provider: edgeproto.ProviderGitHub, Subject: "8008", Login: "octo-alt"}); err != nil {
		t.Fatal(err)
	}
	if err := transfer(m.ID); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams ||
		!strings.Contains(pe.Message, "has the GitHub identities github:"+octo.Subject+", github:8008 and ownership goes to one") {
		t.Fatalf("transfer to a member with two GitHub identities = %v, want refused", err)
	}
}

// lockedEdgeOwner is an edge agent whose state lock a claim holds: the
// transfer waits for claim, which needs registerMu, to finish.
type lockedEdgeOwner struct {
	*fakeEdgeOwner
	claim func() error
}

func (o lockedEdgeOwner) TransferOwner(edgeproto.Account) error {
	done := make(chan error, 1)
	go func() { done <- o.claim() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("the claim holding the edge state lock is still waiting")
	}
}

// A transfer and a claim through the edge at the same moment both finish.
func TestOwnerTransferDuringAClaim(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, newSigner(t), "laptop")))
	key := newSigner(t)
	e.srv.SetEdgeOwner(lockedEdgeOwner{fakeEdgeOwner: &fakeEdgeOwner{}, claim: func() error {
		_, err := e.srv.claimServer(context.Background(), claimGrantFor(octo, key), key.PublicKey(), "")
		return err
	}})
	if err := controlClient(t, e).Call(protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: m.ID}, nil); err != nil {
		t.Fatalf("transfer while a claim waits: %v", err)
	}
}
