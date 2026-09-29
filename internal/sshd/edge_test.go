package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// relayedConn stands in for a WebSocket-backed conn: its RemoteAddr is not
// host:port, and nothing on the relayed path may read it.
type relayedConn struct{ net.Conn }

type relayAddr struct{}

func (relayedConn) RemoteAddr() net.Addr { return relayAddr{} }
func (relayAddr) Network() string        { return "websocket" }
func (relayAddr) String() string         { return "edge relay" }

// octo's login was confirmed by GitHub as the tests start.
var octo = edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo", Name: "Octo Cat",
	IdentityAt: time.Now()}

func grantFor(account edgeproto.Account, device ssh.Signer, label string) edgeproto.Grant {
	now := time.Now().UTC()
	return edgeproto.Grant{
		Issuer: "https://edge.example.test", ServerID: "wqc4lsjvzdzrwq3k5dabdtajwj", ConnID: edgeproto.NewConnID(), Kind: edgeproto.KindSSH,
		Account: account, DeviceID: "edge-" + label, DeviceKey: edgeproto.DeviceKeyLine(device.PublicKey()),
		DeviceLabel: label, IssuedAt: now, ExpiresAt: now.Add(edgeproto.GrantTTL),
	}
}

func withPolicy(p edgeproto.AccessPolicy) func(*Config) {
	return func(c *Config) { c.EdgeAccess = p }
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
	return dialRelayed(t, func(nc net.Conn) { e.srv.ServeEdgeConn(context.Background(), nc, grant) }, signer, user)
}

// dialRelayed runs serve on the server end of a relayed connection and
// dials SSH on the client end.
func dialRelayed(t *testing.T, serve func(net.Conn), signer ssh.Signer, user string) (*ssh.Client, string, error) {
	t.Helper()
	server, client := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(relayedConn{server})
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
	if _, err = os.Stat(invitePath(dir, code)); err != nil {
		t.Fatalf("relayed dial burned the invite code: %v", err)
	}
	if members, _ := e.store.ListMembers(context.Background()); len(members) != 0 {
		t.Fatalf("relayed dial registered %d members through tailnet or invite", len(members))
	}
	// A claim connection offers no tailnet, bootstrap or invite route either.
	codes := newFakeClaimCode()
	_, banner, err = e.dialClaim(t, codes, claimGrantFor(octo, signer), signer, "invite:"+code+":eve")
	if err == nil || !strings.Contains(banner, "must carry the claim code") || len(codes.attempts) != 0 {
		t.Fatalf("claim connection with an invite-code user = %v, banner %q", err, banner)
	}
	if members, _ := e.store.ListMembers(context.Background()); len(members) != 0 {
		t.Fatalf("claim connection registered %d members through tailnet or invite", len(members))
	}
}

// Under account access signing in is enough, so the invited account's
// first connection is admitted.
func TestEdgeInvitationCreatesMemberOnce(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
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
	google := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-7", Email: "octo@example.com", IdentityAt: time.Now()}
	e.mustRefuseEdge(t, google, newSigner(t), "phone", "google account octo@example.com is not a member of this server")
}

func TestEdgeExpiredInvitationRefused(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "OCTO", Role: domain.RoleAdmin,
		ExpiresAt: time.Now().Add(-time.Minute)})
	e.mustRefuseEdge(t, octo, newSigner(t), "laptop", "not a member of this server")
}

// The server checks the identity's age itself: a login the provider has
// not confirmed for a day may belong to someone else by now.
func TestEdgeStaleIdentityMatchesNoInvitation(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	stale := octo
	stale.IdentityAt = time.Now().Add(-edgeproto.IdentityMaxAge - time.Minute)
	e.mustRefuseEdge(t, stale, newSigner(t), "laptop", "not a member of this server")
	e.mustDialEdge(t, octo, newSigner(t), "laptop")
}

// approvalCode reads the code a device awaiting approval was shown.
func approvalCode(t *testing.T, banner string) string {
	t.Helper()
	_, rest, _ := strings.Cut(banner, "aether device approve ")
	code, _, _ := strings.Cut(rest, "\n")
	if code == "" || !strings.Contains(banner, "sudo aether-server device approve "+code) {
		t.Fatalf("approval banner = %q", banner)
	}
	return code
}

// refusedForApproval dials and returns the approval code the refused
// device was shown.
func (e *testEnv) refusedForApproval(t *testing.T, account edgeproto.Account, signer ssh.Signer, label string) string {
	t.Helper()
	_, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, "aether")
	if err == nil {
		t.Fatalf("device %s connected without approval", label)
	}
	return approvalCode(t, banner)
}

// Under approved-devices a member's first device waits like any other:
// accepting the invitation created the member, not access (rules 1 and
// 5). The device shows its code inside SSH; no list carries it (rule 3).
func TestEdgeFirstDevicePendingUntilApproved(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop, phone := newSigner(t), newSigner(t)
	code := e.refusedForApproval(t, octo, laptop, "laptop")
	if code != store.ApprovalCode(edgeproto.DeviceKeyLine(laptop.PublicKey())) {
		t.Fatalf("approval code %s is not derived from the device key", code)
	}
	if m, err := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject); err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("invitation acceptance = %+v, %v; want the member created", m, err)
	}
	var list json.RawMessage
	admin := controlClient(t, e)
	if err := admin.Call(protocol.MethodMemberDeviceList, struct{}{}, &list); err != nil {
		t.Fatalf("device list: %v", err)
	}
	if !strings.Contains(string(list), `"laptop"`) || strings.Contains(string(list), code) {
		t.Fatalf("device list %s: want the pending laptop without its approval code", list)
	}

	// Another member may not approve it; an admin may.
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	var pe *protocol.Error
	err := controlAs(t, e, bob).Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("other member approving = %v, want CodeDenied", err)
	}
	var approved protocol.MemberDeviceResult
	if err := admin.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: strings.ToLower(code)}, &approved); err != nil {
		t.Fatalf("admin approve: %v", err)
	}
	if approved.Device.Status != string(domain.DeviceApproved) || approved.Device.ApprovedBy != string(e.member.ID) {
		t.Fatalf("approved device = %+v", approved.Device)
	}
	first := controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))

	// A new key is a new device, and waits too (rule 7); the member
	// approves it from the approved one.
	code = e.refusedForApproval(t, octo, phone, "phone")
	if err := first.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: code}, &approved); err != nil {
		t.Fatalf("approve from the approved device: %v", err)
	}
	e.mustDialEdge(t, octo, phone, "phone")
}

func TestEdgeDeviceRevokeRefusesAndCloses(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
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
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
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
	// Claim connections share the relayed budget.
	server, client = tcpPair(t)
	defer func() { _ = client.Close() }()
	shed = make(chan struct{})
	claim := claimGrantFor(octo, newSigner(t))
	go func() {
		e.srv.ServeEdgeClaim(context.Background(), relayedConn{server}, claim, newFakeClaimCode().attempt)
		close(shed)
	}()
	select {
	case <-shed:
	case <-time.After(5 * time.Second):
		t.Fatal("claim connection over the relayed budget was not shed")
	}
	serverInfoMember(t, controlClient(t, e))
}

const testClaimCode = "wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop"

// fakeClaimCode stands in for the edge agent's claim code state: five
// attempts, and the code destroyed once it claimed.
type fakeClaimCode struct {
	mu       sync.Mutex
	left     int
	used     bool
	attempts []string
}

func newFakeClaimCode() *fakeClaimCode { return &fakeClaimCode{left: edgeproto.ClaimCodeAttempts} }

func (f *fakeClaimCode) attempt(code string, claim func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, code)
	switch {
	case f.used || f.left == 0:
		return edgeproto.RefusalClaimExhausted
	case code != testClaimCode:
		f.left--
		return edgeproto.RefusalClaimWrong
	}
	f.left--
	if err := claim(); err != nil {
		return err
	}
	f.used = true
	return nil
}

func claimGrantFor(account edgeproto.Account, device ssh.Signer) edgeproto.Grant {
	g := grantFor(account, device, "laptop")
	g.Kind = edgeproto.KindClaim
	return g
}

// claimUser is the user name a client signed in as a claims with code.
func claimUser(t *testing.T, code string, a edgeproto.Account) string {
	t.Helper()
	user, _, err := edgeproto.ClaimUser(code, edgeproto.AccountPrincipal(a))
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (e *testEnv) dialClaim(t *testing.T, codes *fakeClaimCode, grant edgeproto.Grant, signer ssh.Signer, user string) (*ssh.Client, string, error) {
	t.Helper()
	return dialRelayed(t, func(nc net.Conn) {
		e.srv.ServeEdgeClaim(context.Background(), nc, grant, codes.attempt)
	}, signer, user)
}

// The claim code travels inside SSH as the user name. It makes the
// grant's account the admin and approves the claiming device key, under
// approved-devices too: the code came from the machine's console.
func TestClaimInsideSSH(t *testing.T) {
	t.Parallel()
	e := newFreshTestEnv(t, nil)
	codes := newFakeClaimCode()
	laptop := newSigner(t)

	_, banner, err := e.dialClaim(t, codes, claimGrantFor(octo, laptop), laptop, "aether")
	if err == nil || !strings.Contains(banner, "must carry the claim code") || len(codes.attempts) != 0 {
		t.Fatalf("claim without a claim user = %v, banner %q, attempts %d", err, banner, len(codes.attempts))
	}
	wrong := testClaimCode[:len(testClaimCode)-1] + "q"
	_, banner, err = e.dialClaim(t, codes, claimGrantFor(octo, laptop), laptop, claimUser(t, wrong, octo))
	if err == nil || !strings.Contains(banner, string(edgeproto.RefusalClaimWrong)) {
		t.Fatalf("wrong claim code = %v, banner %q", err, banner)
	}
	// Only the grant's device key reaches the code.
	other := newSigner(t)
	before := len(codes.attempts)
	if _, _, err = e.dialClaim(t, codes, claimGrantFor(octo, laptop), other, claimUser(t, testClaimCode, octo)); err == nil || len(codes.attempts) != before {
		t.Fatalf("claim with another key = %v, attempts %d -> %d", err, before, len(codes.attempts))
	}

	c, banner, err := e.dialClaim(t, codes, claimGrantFor(octo, laptop), laptop, claimUser(t, strings.ToUpper(testClaimCode), octo))
	if err != nil {
		t.Fatalf("claim: %v (banner %q)", err, banner)
	}
	owner := serverInfoMember(t, controlClientOn(t, c))
	if owner.Role != string(domain.RoleAdmin) || owner.DisplayName != "Octo Cat" {
		t.Fatalf("claimed member = %+v, want the account as admin", owner)
	}
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil || dev.Status != domain.DeviceApproved || dev.ApprovedBy != "" {
		t.Fatalf("claiming device = %+v, %v; want approved by the machine", dev, err)
	}
	if got := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))); got.ID != owner.ID {
		t.Fatalf("the claiming device connects as %s, want %s", got.ID, owner.ID)
	}
	if _, _, err = e.dialClaim(t, codes, claimGrantFor(octo, laptop), laptop, claimUser(t, testClaimCode, octo)); err == nil {
		t.Fatal("a used claim code claimed again")
	}
}

// Another account's claim of a server with members is refused even with
// the right code: only an admin who linked that account claims there.
func TestClaimOfAClaimedServerIsRefused(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	mallory := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2002", Login: "mallory", IdentityAt: time.Now()}
	key := newSigner(t)
	_, banner, err := e.dialClaim(t, newFakeClaimCode(), claimGrantFor(mallory, key), key, claimUser(t, testClaimCode, mallory))
	if err == nil || !strings.Contains(banner, string(edgeproto.RefusalClaimed)) {
		t.Fatalf("claim of a claimed server = %v, banner %q", err, banner)
	}
	if _, err := identities(t, e).GetMemberByIdentity(context.Background(), mallory.Provider, mallory.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused claim bound the account: %v", err)
	}
}

func TestClaimLinksAnExistingAdmin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ada := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "3003", Login: "ada", IdentityAt: time.Now()}
	key := newSigner(t)
	if _, _, err := e.dialClaim(t, newFakeClaimCode(), claimGrantFor(ada, key), key, claimUser(t, testClaimCode, ada)); err == nil {
		t.Fatal("claim before the admin linked the account succeeded")
	}
	var link protocol.MemberInvitationResult
	if err := controlClient(t, e).Call(protocol.MethodMemberIdentityLink,
		protocol.MemberIdentityLinkParams{Provider: "github", Login: "ada"}, &link); err != nil {
		t.Fatalf("admin link: %v", err)
	}
	c, banner, err := e.dialClaim(t, newFakeClaimCode(), claimGrantFor(ada, key), key, claimUser(t, testClaimCode, ada))
	if err != nil {
		t.Fatalf("claim through the admin's link: %v (banner %q)", err, banner)
	}
	if m := serverInfoMember(t, controlClientOn(t, c)); m.ID != string(e.member.ID) {
		t.Fatalf("claim through the admin's link bound %s, want %s", m.ID, e.member.ID)
	}
}

// An edge that puts its own account in a claim grant is refused before
// the code is tried: the client names its account in the user name it
// signs. No member is created and no attempt is spent.
func TestClaimThroughAnEdgeThatSubstitutesTheAccount(t *testing.T) {
	t.Parallel()
	e := newFreshTestEnv(t, nil)
	mallory := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2002", Login: "mallory", IdentityAt: time.Now()}
	victim := newSigner(t)
	codes := newFakeClaimCode()
	_, banner, err := e.dialClaim(t, codes, claimGrantFor(mallory, victim), victim, claimUser(t, testClaimCode, octo))
	if err == nil || !strings.Contains(banner, "the edge signed the connection in as github account mallory") {
		t.Fatalf("claim with a substituted account = %v, banner %q", err, banner)
	}
	if len(codes.attempts) != 0 {
		t.Fatalf("substituted claim spent %d attempts", len(codes.attempts))
	}
	members, err := e.store.ListMembers(context.Background())
	if err != nil || len(members) != 0 {
		t.Fatalf("members after a substituted claim = %+v, %v; want none", members, err)
	}
	// The real account still claims with the same code.
	if _, banner, err := e.dialClaim(t, codes, claimGrantFor(octo, victim), victim, claimUser(t, testClaimCode, octo)); err != nil {
		t.Fatalf("claim: %v (banner %q)", err, banner)
	}
}

// A grant of one kind is never served as the other.
func TestGrantKindIsServedOnlyAsItself(t *testing.T) {
	t.Parallel()
	e := newFreshTestEnv(t, nil)
	key := newSigner(t)
	codes := newFakeClaimCode()
	if _, _, err := e.dialEdge(t, claimGrantFor(octo, key), key, claimUser(t, testClaimCode, octo)); err == nil {
		t.Fatal("a claim grant was served as a member connection")
	}
	if _, _, err := e.dialClaim(t, codes, grantFor(octo, key, "laptop"), key, claimUser(t, testClaimCode, octo)); err == nil || len(codes.attempts) != 0 {
		t.Fatalf("an ssh grant reached the claim code: %v, %d attempts", err, len(codes.attempts))
	}
}

// Nothing proves a link's caller holds the account it names, so a member
// who is not an admin cannot bind another person's account to their own
// member ahead of that person's invitation.
func TestMemberCannotLinkAnotherAccount(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	carolKey, _ := addMember(t, e, "Carol", domain.RoleViewer, false)
	var pe *protocol.Error
	err := controlAs(t, e, carolKey).Call(protocol.MethodMemberIdentityLink,
		protocol.MemberIdentityLinkParams{Provider: "github", Login: "octo"}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("viewer linking another account = %v, want CodeDenied", err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Provider: "github", Login: "octo", Role: "collaborator"}, nil); err != nil {
		t.Fatalf("invitation.create: %v", err)
	}
	got := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, newSigner(t), "laptop")))
	if got.DisplayName != "Octo Cat" || got.Role != string(domain.RoleCollaborator) {
		t.Fatalf("octo joined as %+v, want a new collaborator from the admin's invitation", got)
	}
}

// An invitation grants its role when accepted, so it goes when its creator
// stops being an admin.
func TestDemotedAdminsInvitationsAreRevoked(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	carolKey, carol := addMember(t, e, "Carol", domain.RoleAdmin, false)
	if err := controlAs(t, e, carolKey).Call(protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Provider: "github", Login: "octo", Role: "admin"}, nil); err != nil {
		t.Fatalf("invitation.create: %v", err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberRole,
		protocol.MemberRoleParams{MemberID: string(carol.ID), Role: string(domain.RoleViewer)}, nil); err != nil {
		t.Fatalf("member.role: %v", err)
	}
	if entries, err := e.srv.EdgeDirectory(context.Background()); err != nil || len(entries) != 0 {
		t.Fatalf("directory after demotion = %+v, %v; want no invitation", entries, err)
	}
	e.mustRefuseEdge(t, octo, newSigner(t), "laptop", "github account octo is not a member of this server")
}

// The edge accepts a bounded directory; an invitation past it is refused
// when created instead of stopping every later directory push.
func TestInvitationBeyondTheEdgeDirectoryIsRefused(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for i := range edgeproto.MaxDirectoryEntries {
		inviteAccount(t, e, domain.Invitation{Email: fmt.Sprintf("person%d@example.com", i), Role: domain.RoleViewer})
	}
	var pe *protocol.Error
	err := controlClient(t, e).Call(protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Provider: "github", Login: "octo", Role: "collaborator"}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState || !strings.Contains(pe.Message, "the most an edge accepts") {
		t.Fatalf("invitation past the directory limit = %v, want refused", err)
	}
	if entries, err := e.srv.EdgeDirectory(context.Background()); err != nil || len(entries) != edgeproto.MaxDirectoryEntries {
		t.Fatalf("directory = %d entries, %v; want the full directory", len(entries), err)
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

// A device key the server approved authenticates its member on the
// direct path too. The direct path accepts approved device keys only
// (rule 11), under either policy: it never asks the edge whether the
// account is still signed in.
func TestEdgeDeviceKeyOnTheDirectPath(t *testing.T) {
	t.Parallel()
	for _, policy := range []edgeproto.AccessPolicy{edgeproto.PolicyApprovedDevices, edgeproto.PolicyAccount} {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, withPolicy(policy))
			inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
			laptop := newSigner(t)
			if _, err := e.dialWith(laptop, nil); err == nil {
				t.Fatal("a device key authenticated directly before the server registered it")
			}
			if _, _, err := e.dialEdge(t, grantFor(octo, laptop, "laptop"), laptop, "aether"); (err == nil) != (policy == edgeproto.PolicyAccount) {
				t.Fatalf("first relayed connection under %s: %v", policy, err)
			}
			var banner strings.Builder
			if _, err := e.dialWith(laptop, &banner); err == nil || !strings.Contains(banner.String(), "aether device approve") {
				t.Fatalf("direct dial with an unapproved device key: %v, banner %q", err, banner.String())
			}
			if err := controlClient(t, e).Call(protocol.MethodMemberDeviceApprove,
				protocol.MemberDeviceApproveParams{Code: approvalCode(t, banner.String())}, nil); err != nil {
				t.Fatalf("approve: %v", err)
			}
			direct, err := e.dialWith(laptop, nil)
			if err != nil {
				t.Fatalf("direct dial with an approved device key: %v", err)
			}
			if m := serverInfoMember(t, controlClientOn(t, direct)); m.DisplayName != octo.Name {
				t.Fatalf("direct dial authenticated as %+v, want octo's member", m)
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
		})
	}
}
