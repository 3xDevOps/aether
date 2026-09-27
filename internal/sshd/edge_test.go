package sshd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// relayedConn stands in for a WebSocket-backed conn: its RemoteAddr is not
// host:port, and nothing on the relayed path may read it.
type relayedConn struct{ net.Conn }

type relayAddr struct{}

func (relayedConn) RemoteAddr() net.Addr { return relayAddr{} }
func (relayAddr) Network() string        { return "websocket" }
func (relayAddr) String() string         { return "edge relay" }

var octo = edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo", Name: "Octo Cat"}

func grantFor(account edgeproto.Account, device ssh.Signer, label string) edgeproto.Grant {
	now := time.Now().UTC()
	return edgeproto.Grant{
		ServerID: "wqc4lsjvzdzrwq3k5dabdtajwj", ConnID: edgeproto.NewConnID(), Kind: edgeproto.KindSSH,
		Account: account, DeviceID: "edge-" + label, DeviceKey: edgeproto.DeviceKeyLine(device.PublicKey()),
		DeviceLabel: label, IssuedAt: now, ExpiresAt: now.Add(edgeproto.GrantTTL),
	}
}

// tcpPair returns both ends of a loopback TCP connection. net.Pipe would
// deadlock: both SSH ends write their version line before reading.
func tcpPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server = <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	return server, client
}

// dialEdge relays one SSH connection through ServeEdgeConn, as the edge
// agent does, and returns the client with the banner it was shown.
func (e *testEnv) dialEdge(t *testing.T, grant edgeproto.Grant, signer ssh.Signer, user string) (*ssh.Client, string, error) {
	t.Helper()
	server, client := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.ServeEdgeConn(context.Background(), relayedConn{server}, grant)
	}()
	var banner strings.Builder
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		BannerCallback:  func(m string) error { banner.WriteString(m); return nil },
		Timeout:         5 * time.Second,
	}
	conn, chans, reqs, err := ssh.NewClientConn(client, "edge", cfg)
	if err != nil {
		_ = client.Close()
		<-done
		return nil, banner.String(), err
	}
	c := ssh.NewClient(conn, chans, reqs)
	t.Cleanup(func() {
		_ = c.Close()
		<-done
	})
	return c, banner.String(), nil
}

func (e *testEnv) mustDialEdge(t *testing.T, account edgeproto.Account, signer ssh.Signer, label string) *ssh.Client {
	t.Helper()
	c, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, "aether")
	if err != nil {
		t.Fatalf("edge dial: %v (banner %q)", err, banner)
	}
	return c
}

func (e *testEnv) mustRefuseEdge(t *testing.T, account edgeproto.Account, signer ssh.Signer, label, wantBanner string) {
	t.Helper()
	_, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, "aether")
	if err == nil {
		t.Fatalf("edge dial succeeded, want refusal %q", wantBanner)
	}
	if !strings.Contains(banner, wantBanner) {
		t.Fatalf("edge refusal banner = %q, want it to contain %q", banner, wantBanner)
	}
}

func identities(t *testing.T, e *testEnv) store.IdentityStore {
	t.Helper()
	ids, ok := e.store.(store.IdentityStore)
	if !ok {
		t.Fatal("test store does not hold identities")
	}
	return ids
}

func inviteAccount(t *testing.T, e *testEnv, inv domain.Invitation) *domain.Invitation {
	t.Helper()
	inv.CreatedBy = e.member.ID
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = time.Now().Add(edgeproto.InvitationTTL)
	}
	if err := identities(t, e).CreateInvitation(context.Background(), &inv); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	return &inv
}

// waitClosed waits for the server to close a relayed client connection.
func waitClosed(t *testing.T, c *ssh.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection still open after revocation")
	}
}

func TestEdgeKeyMustBeTheGrantsDeviceKey(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	granted, offered := newSigner(t), newSigner(t)
	_, banner, err := e.dialEdge(t, grantFor(octo, granted, "laptop"), offered, "aether")
	if err == nil {
		t.Fatal("a key other than the grant's device key authenticated")
	}
	if !strings.Contains(banner, "not the device key") {
		t.Fatalf("banner = %q", banner)
	}
	if _, err := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused key consumed the invitation: %v", err)
	}
}

func TestEdgeNoBootstrapOnEmptyStore(t *testing.T) {
	t.Parallel()
	e := newFreshTestEnv(t, nil)
	e.mustRefuseEdge(t, octo, newSigner(t), "laptop", "github account octo is not a member of this server")
	members, err := e.store.ListMembers(context.Background())
	if err != nil || len(members) != 0 {
		t.Fatalf("members after relayed contact with an empty store = %d, %v; want none", len(members), err)
	}
}

func TestEdgeIgnoresInviteCodeUserAndTailnet(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "invites")
	whois := &fakeWhoIs{}
	whois.set(WhoIsIdentity{Login: "ada@example.com", NodeID: "n1"}, nil)
	e := newFreshTestEnv(t, func(c *Config) {
		c.InvitesDir = dir
		c.WhoIs = whois
	})
	code, _, err := mintInvite(dir, time.Hour)
	if err != nil {
		t.Fatalf("mint invite: %v", err)
	}
	signer := newSigner(t)
	_, banner, err := e.dialEdge(t, grantFor(octo, signer, "laptop"), signer, "invite:"+code+":eve")
	if err == nil || !strings.Contains(banner, "not a member") {
		t.Fatalf("relayed invite-code user = %v, banner %q; want not a member", err, banner)
	}
	if _, err := os.Stat(invitePath(dir, code)); err != nil {
		t.Fatalf("relayed dial burned the invite code: %v", err)
	}
	if members, _ := e.store.ListMembers(context.Background()); len(members) != 0 {
		t.Fatalf("relayed dial registered %d members through tailnet or invite", len(members))
	}
}

func TestEdgeInvitationCreatesMemberOnce(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inv := inviteAccount(t, e, domain.Invitation{Email: "octo@example.com", Role: domain.RoleViewer})
	account := octo
	account.Email = "octo@example.com"
	signer := newSigner(t)

	var wg sync.WaitGroup
	clients := make([]*ssh.Client, 2)
	for i := range clients {
		wg.Go(func() { clients[i], _, _ = e.dialEdge(t, grantFor(account, signer, "laptop"), signer, "aether") })
	}
	wg.Wait()
	for i, c := range clients {
		if c == nil {
			t.Fatalf("concurrent accept %d refused", i)
		}
	}
	got := serverInfoMember(t, controlClientOn(t, clients[0]))
	if got.Role != string(domain.RoleViewer) || got.Pending {
		t.Fatalf("invited member = %+v, want an approved viewer", got)
	}
	if again := serverInfoMember(t, controlClientOn(t, clients[1])); again.ID != got.ID {
		t.Fatalf("concurrent accepts created members %s and %s", got.ID, again.ID)
	}
	members, _ := e.store.ListMembers(context.Background())
	if len(members) != 2 {
		t.Fatalf("members = %d, want the admin and one invitee", len(members))
	}
	if left, _ := identities(t, e).ListInvitations(context.Background()); len(left) != 0 {
		t.Fatalf("invitation %s not consumed: %+v", inv.ID, left)
	}
	// The same verified email from another provider is another account,
	// and the consumed invitation admits nobody else.
	google := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-7", Email: "octo@example.com"}
	e.mustRefuseEdge(t, google, newSigner(t), "phone", "google account octo@example.com is not a member of this server")
}

func TestEdgeExpiredInvitationRefused(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "OCTO", Role: domain.RoleAdmin,
		ExpiresAt: time.Now().Add(-time.Minute)})
	e.mustRefuseEdge(t, octo, newSigner(t), "laptop", "not a member of this server")
}

func TestEdgeSecondDevicePendingUntilApproved(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop, phone := newSigner(t), newSigner(t)
	first := controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))

	_, banner, err := e.dialEdge(t, grantFor(octo, phone, "phone"), phone, "aether")
	if err == nil {
		t.Fatal("second device connected without approval")
	}
	var list protocol.MemberDeviceListResult
	if err = first.Call(protocol.MethodMemberDeviceList, struct{}{}, &list); err != nil {
		t.Fatalf("device list: %v", err)
	}
	var code string
	for _, d := range list.Devices {
		if d.Label == "phone" && d.Status == string(domain.DevicePending) {
			code = d.ApprovalCode
		}
	}
	if code == "" || !strings.Contains(banner, "aether device approve "+code) ||
		!strings.Contains(banner, "sudo aether-server device approve "+code) {
		t.Fatalf("pending banner = %q, devices %+v", banner, list.Devices)
	}

	// Another member may not approve it; its own member may.
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	var pe *protocol.Error
	err = controlAs(t, e, bob).Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("other member approving = %v, want CodeDenied", err)
	}
	var approved protocol.MemberDeviceResult
	if err := first.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: strings.ToLower(code)}, &approved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Device.Status != string(domain.DeviceApproved) {
		t.Fatalf("approved device = %+v", approved.Device)
	}
	e.mustDialEdge(t, octo, phone, "phone")
}

func TestEdgeDeviceRevokeRefusesAndCloses(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop := newSigner(t)
	live := e.mustDialEdge(t, octo, laptop, "laptop")
	serverInfoMember(t, controlClientOn(t, live))

	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberDeviceRevoke,
		protocol.MemberDeviceRevokeParams{DeviceID: string(dev.ID)}, nil); err != nil {
		t.Fatalf("admin revoke: %v", err)
	}
	waitClosed(t, live)
	e.mustRefuseEdge(t, octo, laptop, "laptop", `device "laptop" was revoked`)
}

func TestEdgeMemberRemovalClosesLiveConnections(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	live := e.mustDialEdge(t, octo, newSigner(t), "laptop")
	m := serverInfoMember(t, controlClientOn(t, live))
	changed := e.srv.EdgeDirectoryChanged()
	for len(changed) > 0 {
		<-changed
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: m.ID}, nil); err != nil {
		t.Fatalf("member.remove: %v", err)
	}
	waitClosed(t, live)
	select {
	case <-changed:
	default:
		t.Fatal("member removal did not signal a directory change")
	}
	if _, err := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("identity survived member removal: %v", err)
	}
}

func TestCloseEdgeDeviceClosesItsConnections(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.EdgeDeviceAutoApprove = true })
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop, phone := newSigner(t), newSigner(t)
	revoked := e.mustDialEdge(t, octo, laptop, "laptop")
	kept := e.mustDialEdge(t, octo, phone, "phone")
	e.srv.CloseEdgeDevice(string(ssh.MarshalAuthorizedKey(laptop.PublicKey())))
	waitClosed(t, revoked)
	serverInfoMember(t, controlClientOn(t, kept))
}

func TestEdgeHandshakeFloodKeepsDirectPathOpen(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.maxHandshakes = 2 })
	grant := grantFor(octo, newSigner(t), "flood")
	for range 2 {
		server, client := tcpPair(t)
		t.Cleanup(func() { _ = client.Close() })
		go e.srv.ServeEdgeConn(context.Background(), relayedConn{server}, grant)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.srv.edgeHandshakes) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("stalled relayed handshakes did not fill the relayed budget")
		}
		time.Sleep(5 * time.Millisecond)
	}
	server, client := tcpPair(t)
	defer func() { _ = client.Close() }()
	shed := make(chan struct{})
	go func() {
		e.srv.ServeEdgeConn(context.Background(), relayedConn{server}, grant)
		close(shed)
	}()
	select {
	case <-shed:
	case <-time.After(5 * time.Second):
		t.Fatal("relayed connection over budget was not shed")
	}
	serverInfoMember(t, controlClient(t, e))
}

func TestClaimByEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newFreshTestEnv(t, nil)
	owner, err := e.srv.ClaimByEdge(ctx, octo)
	if err != nil || owner.Role != domain.RoleAdmin || owner.DisplayName != "Octo Cat" {
		t.Fatalf("claim of an empty server = %+v, %v", owner, err)
	}
	if again, err := e.srv.ClaimByEdge(ctx, octo); err != nil || again.ID != owner.ID {
		t.Fatalf("repeated claim = %+v, %v; want the same admin", again, err)
	}
	other := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2002", Login: "mallory"}
	if _, err := e.srv.ClaimByEdge(ctx, other); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("claim of a claimed server = %v, want %v", err, edgeproto.RefusalClaimed)
	}
	e.mustDialEdge(t, octo, newSigner(t), "laptop")
}

func TestClaimByEdgeLinksAnExistingAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newTestEnv(t, nil)
	ada := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "3003", Login: "ada"}
	bobKey, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	var link protocol.MemberInvitationResult
	if err := controlAs(t, e, bobKey).Call(protocol.MethodMemberIdentityLink,
		protocol.MemberIdentityLinkParams{Provider: "github", Login: "bob"}, &link); err != nil {
		t.Fatalf("collaborator link: %v", err)
	}
	if link.Invitation.MemberID != string(bob.ID) {
		t.Fatalf("link = %+v, want bound to the caller", link.Invitation)
	}
	bobAccount := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "4004", Login: "bob"}
	if _, err := e.srv.ClaimByEdge(ctx, bobAccount); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("claim through a collaborator's link = %v, want refused", err)
	}
	if _, err := e.srv.ClaimByEdge(ctx, ada); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("claim before the admin linked = %v, want refused", err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberIdentityLink,
		protocol.MemberIdentityLinkParams{Provider: "github", Login: "ada"}, nil); err != nil {
		t.Fatalf("admin link: %v", err)
	}
	m, err := e.srv.ClaimByEdge(ctx, ada)
	if err != nil || m.ID != e.member.ID {
		t.Fatalf("claim through the admin's link = %+v, %v; want %s", m, err, e.member.ID)
	}
}

func TestEdgeInvitationRPCs(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	admin := controlClient(t, e)
	bobKey, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bob := controlAs(t, e, bobKey)
	params := protocol.MemberInvitationCreateParams{Provider: "github", Login: "octo", Role: "collaborator"}

	var pe *protocol.Error
	if err := bob.Call(protocol.MethodMemberInvitationCreate, params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator invitation.create = %v, want CodeDenied", err)
	}
	bad := protocol.MemberInvitationCreateParams{Provider: "google", Login: "octo", Role: "viewer"}
	if err := admin.Call(protocol.MethodMemberInvitationCreate, bad, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("google login invitation = %v, want CodeInvalidParams", err)
	}
	changed := e.srv.EdgeDirectoryChanged()
	for len(changed) > 0 {
		<-changed
	}
	var created protocol.MemberInvitationResult
	if err := admin.Call(protocol.MethodMemberInvitationCreate, params, &created); err != nil {
		t.Fatalf("invitation.create: %v", err)
	}
	<-changed
	entries, err := e.srv.EdgeDirectory(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Kind != edgeproto.EntryInvitation ||
		entries[0].Login != "octo" || entries[0].Role != "collaborator" {
		t.Fatalf("directory = %+v, %v", entries, err)
	}
	if err := bob.Call(protocol.MethodMemberInvitationRevoke,
		protocol.MemberInvitationRevokeParams{InvitationID: created.Invitation.ID}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator revoking an admin's invitation = %v, want CodeDenied", err)
	}
	var list protocol.MemberInvitationListResult
	if err := bob.Call(protocol.MethodMemberInvitationList, struct{}{}, &list); err != nil || len(list.Invitations) != 0 {
		t.Fatalf("collaborator invitation.list = %+v, %v; want only their own", list, err)
	}
	if err := admin.Call(protocol.MethodMemberInvitationRevoke,
		protocol.MemberInvitationRevokeParams{InvitationID: created.Invitation.ID}, nil); err != nil {
		t.Fatalf("invitation.revoke: %v", err)
	}
	if entries, _ := e.srv.EdgeDirectory(context.Background()); len(entries) != 0 {
		t.Fatalf("directory after revoke = %+v", entries)
	}
	e.mustRefuseEdge(t, octo, newSigner(t), "laptop", "not a member")
}

// A device key the server registered through an edge authenticates its
// member on the direct path too, under the same approval rules.
func TestEdgeDeviceKeyOnTheDirectPath(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop, phone := newSigner(t), newSigner(t)
	if _, err := e.dialWith(laptop, nil); err == nil {
		t.Fatal("a device key authenticated directly before the server registered it")
	}
	e.mustDialEdge(t, octo, laptop, "laptop")
	direct, err := e.dialWith(laptop, nil)
	if err != nil {
		t.Fatalf("direct dial with an approved device key: %v", err)
	}
	if m := serverInfoMember(t, controlClientOn(t, direct)); m.DisplayName != octo.Name {
		t.Fatalf("direct dial authenticated as %+v, want octo's member", m)
	}

	e.mustRefuseEdge(t, octo, phone, "phone", "waiting for approval")
	var banner strings.Builder
	if _, err = e.dialWith(phone, &banner); err == nil || !strings.Contains(banner.String(), "aether device approve") {
		t.Fatalf("direct dial with a pending device key: %v, banner %q", err, banner.String())
	}

	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberDeviceRevoke,
		protocol.MemberDeviceRevokeParams{DeviceID: string(dev.ID)}, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	waitClosed(t, direct)
	banner.Reset()
	if _, err := e.dialWith(laptop, &banner); err == nil || !strings.Contains(banner.String(), "was revoked") {
		t.Fatalf("direct dial with a revoked device key: %v, banner %q", err, banner.String())
	}
}
