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
	c, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, e.edgeUser(t, account))
	if err != nil {
		t.Fatalf("edge dial: %v (banner %q)", err, banner)
	}
	return c
}

func (e *testEnv) mustRefuseEdge(t *testing.T, account edgeproto.Account, signer ssh.Signer, label, wantBanner string) {
	t.Helper()
	_, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, e.edgeUser(t, account))
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

// inviteAccount stores inv with a join secret the test env keeps, as if
// its link had been sent.
func inviteAccount(t *testing.T, e *testEnv, inv domain.Invitation) *domain.Invitation {
	t.Helper()
	inv.CreatedBy = e.member.ID
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = time.Now().Add(edgeproto.InvitationTTL)
	}
	secret := edgeproto.NewJoinSecret()
	inv.SecretHash = edgeproto.HashToken(secret)
	if err := identities(t, e).CreateInvitation(context.Background(), &inv); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	e.joinSecrets.Store(inv.ID, secret)
	return &inv
}

// invite calls method, member.invitation.create or member.identity.link,
// over c on a server with an edge, and keeps the secret of the link it
// returns.
func (e *testEnv) invite(t *testing.T, c *protocol.Client, method string, params any) protocol.MemberInvitationResult {
	t.Helper()
	if e.srv.currentEdgeOwner() == nil {
		e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	}
	var out protocol.MemberInvitationResult
	if err := c.Call(method, params, &out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	_, _, id, secret, err := edgeproto.ParseJoinLink(out.Link)
	if err != nil || id != out.Invitation.ID {
		t.Fatalf("%s link %q names %q: %v", method, out.Link, id, err)
	}
	e.joinSecrets.Store(domain.InvitationID(id), secret)
	return out
}

// edgeUser is the SSH user name account connects as: the join user of the
// first open invitation it matches whose link the test env holds, as a
// client that opened that link does, or else the account alone.
func (e *testEnv) edgeUser(t *testing.T, account edgeproto.Account) string {
	t.Helper()
	invs, err := matchingInvitations(context.Background(), identities(t, e), account)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range invs {
		if secret, ok := e.joinSecrets.Load(inv.ID); ok {
			user, err := edgeproto.JoinUser(string(inv.ID), secret.(string), edgeproto.AccountPrincipal(account))
			if err != nil {
				t.Fatal(err)
			}
			return user
		}
	}
	return edgeproto.AccountUser(account)
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
	_, banner, err := e.dialEdge(t, grantFor(octo, granted, "laptop"), offered, e.edgeUser(t, octo))
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
	code, _, err := mintInvite(dir, time.Hour, "")
	if err != nil {
		t.Fatalf("mint invite: %v", err)
	}
	signer := newSigner(t)
	_, banner, err := e.dialEdge(t, grantFor(octo, signer, "laptop"), signer, "invite:"+code+":eve")
	if err == nil || !strings.Contains(banner, "nothing was recorded") {
		t.Fatalf("relayed invite-code user = %v, banner %q; want refused", err, banner)
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

	user := e.edgeUser(t, account)
	var wg sync.WaitGroup
	clients := make([]*ssh.Client, 2)
	for i := range clients {
		wg.Go(func() {
			clients[i], _, _ = e.dialEdge(t, grantFor(account, signer, "laptop"), signer, user)
		})
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
	// The connection that loses the race looks for its invitation after the
	// other one consumed it.
	late, err := e.srv.edgeInvitee(context.Background(), identities(t, e), account, "", "", edgeproto.DeviceKeyLine(signer.PublicKey()), "laptop")
	if err != nil || string(late.ID) != got.ID {
		t.Fatalf("accept after the invitation was consumed = %+v, %v; want member %s", late, err, got.ID)
	}
	// Another account that now holds the verified email is another
	// member, and the consumed invitation admits nobody else.
	other := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "7007", Login: "someone", Email: "octo@example.com", IdentityAt: time.Now()}
	e.mustRefuseEdge(t, other, newSigner(t), "phone", "github account someone is not a member of this server")
}

// An invitation is joined only with the secret of its link, by the
// account it names: under account access the join is the whole of it,
// under approved-devices the device waits on the invitation. A wrong
// secret, another invitation's, a closed invitation's, or the right one
// from another account records nothing.
func TestJoinByLink(t *testing.T) {
	t.Parallel()
	someone := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "7007", Login: "someone", IdentityAt: time.Now()}
	joinUser := func(inv domain.InvitationID, secret string, account edgeproto.Account) string {
		u, _ := edgeproto.JoinUser(string(inv), secret, edgeproto.AccountPrincipal(account))
		return u
	}
	wrongSecret := func(_ *testing.T, _ *testEnv, inv domain.InvitationID, _ string) string {
		return joinUser(inv, edgeproto.NewJoinSecret(), octo)
	}
	for _, tc := range []struct {
		name    string
		policy  edgeproto.AccessPolicy
		account edgeproto.Account
		user    func(t *testing.T, e *testEnv, inv domain.InvitationID, secret string) string
		want    edgeproto.RefusedKind
	}{
		{name: "account", policy: edgeproto.PolicyAccount},
		{name: "approved-devices", policy: edgeproto.PolicyApprovedDevices, want: edgeproto.RefusedDevicePending},
		{name: "wrong secret", policy: edgeproto.PolicyAccount, want: edgeproto.RefusedJoinWrong, user: wrongSecret},
		{name: "wrong secret under approved-devices", policy: edgeproto.PolicyApprovedDevices, want: edgeproto.RefusedJoinWrong, user: wrongSecret},
		{name: "account the invitation does not name", policy: edgeproto.PolicyAccount, account: someone, want: edgeproto.RefusedJoinWrong,
			user: func(_ *testing.T, _ *testEnv, inv domain.InvitationID, secret string) string {
				return joinUser(inv, secret, someone)
			}},
		{name: "another invitation's secret", policy: edgeproto.PolicyAccount, want: edgeproto.RefusedJoinWrong,
			user: func(t *testing.T, e *testEnv, inv domain.InvitationID, _ string) string {
				other := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleViewer})
				secret, _ := e.joinSecrets.Load(other.ID)
				return joinUser(inv, secret.(string), octo)
			}},
		{name: "consumed invitation", policy: edgeproto.PolicyAccount, want: edgeproto.RefusedJoinWrong,
			user: func(t *testing.T, e *testEnv, inv domain.InvitationID, secret string) string {
				open, err := identities(t, e).GetInvitation(context.Background(), inv)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e.srv.acceptInvitation(context.Background(), identities(t, e), open, someone); err != nil {
					t.Fatal(err)
				}
				return joinUser(inv, secret, octo)
			}},
		{name: "no link", policy: edgeproto.PolicyApprovedDevices, want: edgeproto.RefusedInvitationNeedsLink,
			user: func(*testing.T, *testEnv, domain.InvitationID, string) string { return edgeproto.AccountUser(octo) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, withPolicy(tc.policy))
			inv := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
			account := octo
			if tc.account.Subject != "" {
				account = tc.account
			}
			user := e.edgeUser(t, account)
			if tc.user != nil {
				secret, _ := e.joinSecrets.Load(inv.ID)
				user = tc.user(t, e, inv.ID, secret.(string))
			}
			ctx := context.Background()
			open, _ := identities(t, e).ListInvitations(ctx)
			signer := newSigner(t)
			c, banner, err := e.dialEdge(t, grantFor(account, signer, "laptop"), signer, user)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("join: %v (banner %q)", err, banner)
				}
				if m := serverInfoMember(t, controlClientOn(t, c)); m.Role != string(domain.RoleCollaborator) {
					t.Fatalf("joined as %+v", m)
				}
				return
			}
			if r, ok := edgeproto.ParseRefusedBanner(banner); err == nil || !ok || r.Kind != tc.want {
				t.Fatalf("join = %v, banner %q; want %s", err, banner, tc.want)
			}
			if _, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(signer.PublicKey())); errors.Is(err, store.ErrNotFound) == (tc.want == edgeproto.RefusedDevicePending) {
				t.Fatalf("device after %s: %v", tc.want, err)
			}
			if m, err := identities(t, e).GetMemberByIdentity(ctx, account.Provider, account.Subject); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("member after %s = %+v, %v; want none", tc.want, m, err)
			}
			if left, _ := identities(t, e).ListInvitations(ctx); len(left) != len(open) {
				t.Fatalf("invitations after %s = %+v; want %+v", tc.want, left, open)
			}
		})
	}
}

// A device waiting on one invitation that joins with another's link
// becomes a new device on that one, so an approval of the device an admin
// was shown before, by id or by code, approves nothing.
func TestJoinByAnotherLinkMovesTheWaitingDevice(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyApprovedDevices))
	ctx := context.Background()
	first := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleViewer})
	second := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	signer := newSigner(t)
	keyLine := edgeproto.DeviceKeyLine(signer.PublicKey())
	var banner string
	var waited []*domain.Device
	for _, inv := range []*domain.Invitation{first, second} {
		secret, _ := e.joinSecrets.Load(inv.ID)
		user, err := edgeproto.JoinUser(string(inv.ID), secret.(string), edgeproto.AccountPrincipal(octo))
		if err != nil {
			t.Fatal(err)
		}
		if _, banner, err = e.dialEdge(t, grantFor(octo, signer, "laptop"), signer, user); err == nil {
			t.Fatalf("join through %s was admitted before approval", inv.ID)
		}
		if r, ok := edgeproto.ParseRefusedBanner(banner); !ok || r.Kind != edgeproto.RefusedDevicePending ||
			r.Invitation != string(inv.ID) || r.Role != string(inv.Role) {
			t.Fatalf("join through %s (%s): banner %q", inv.ID, inv.Role, banner)
		}
		dev, err := identities(t, e).GetDeviceByCredential(ctx, keyLine)
		if err != nil || dev.Invitation != inv.ID {
			t.Fatalf("device after the join through %s = %+v, %v", inv.ID, dev, err)
		}
		waited = append(waited, dev)
	}
	shown, moved := waited[0], waited[1]
	if moved.ID == shown.ID || moved.ApprovalCode != shown.ApprovalCode {
		t.Fatalf("device after the second join = %s code %s, want a new id and the code %s of %s",
			moved.ID, moved.ApprovalCode, shown.ApprovalCode, shown.ID)
	}
	admin := controlClient(t, e)
	var pe *protocol.Error
	if err := admin.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{DeviceID: string(shown.ID)}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("approval of the device id shown before the move = %v, want not found", err)
	}
	if err := admin.Call(protocol.MethodMemberDeviceApprove,
		protocol.MemberDeviceApproveParams{Code: shown.ApprovalCode, DeviceID: string(shown.ID)}, nil); !errors.As(err, &pe) ||
		pe.Code != protocol.CodeConflict || !strings.Contains(pe.Message, "nothing was approved") {
		t.Fatalf("approval by code of the device id shown before the move = %v, want a conflict", err)
	}
	if _, err := ApproveDevice(ctx, identities(t, e), e.store, shown, ""); !errors.Is(err, store.ErrNotFound) ||
		!strings.Contains(err.Error(), "nothing was approved") {
		t.Fatalf("console approval of the device read before the move = %v, want not found", err)
	}
	if m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member after the refused approvals = %+v, %v; want none", m, err)
	}
	if dev, err := identities(t, e).GetDevice(ctx, moved.ID); err != nil || dev.Status != domain.DevicePending {
		t.Fatalf("device after the refused approvals = %+v, %v; want pending", dev, err)
	}
	approveAsAdmin(t, e, e.approvalCode(t, signer, banner))
	m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject)
	if err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("member after approval = %+v, %v; want a collaborator", m, err)
	}
	if left, err := identities(t, e).GetInvitation(ctx, first.ID); err != nil || left.ConsumedAt != nil {
		t.Fatalf("the invitation the device left = %+v, %v; want open", left, err)
	}
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

// approvalCode returns the approval code of the device signer refused
// with banner: the one the banner carries, or, where the banner names
// only the device id because someone can approve it in the app, the one
// the console reads from the store.
func (e *testEnv) approvalCode(t *testing.T, signer ssh.Signer, banner string) string {
	t.Helper()
	r, ok := edgeproto.ParseRefusedBanner(banner)
	if !ok || r.Kind != edgeproto.RefusedDevicePending {
		t.Fatalf("approval banner = %q", banner)
	}
	if r.ApprovalCode != "" {
		if !strings.Contains(banner, "sudo aether-server device approve "+r.ApprovalCode) {
			t.Fatalf("approval banner = %q", banner)
		}
		return r.ApprovalCode
	}
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(signer.PublicKey()))
	if err != nil || string(dev.ID) != r.DeviceID || !strings.Contains(banner, "aether device approve "+r.DeviceID+"\n") ||
		dev.ApprovalCode == "" || strings.Contains(banner, dev.ApprovalCode) {
		t.Fatalf("approval banner = %q for device %+v, %v", banner, dev, err)
	}
	return dev.ApprovalCode
}

// refusedForApproval dials and returns the approval code of the refused
// device.
func (e *testEnv) refusedForApproval(t *testing.T, account edgeproto.Account, signer ssh.Signer, label string) string {
	t.Helper()
	_, banner, err := e.dialEdge(t, grantFor(account, signer, label), signer, e.edgeUser(t, account))
	if err == nil {
		t.Fatalf("device %s connected without approval", label)
	}
	return e.approvalCode(t, signer, banner)
}

// Under approved-devices a member's first device waits like any other,
// and until a person approves it the invitation is only waited on: no
// member, no bound account, the invitation still open (rules 1 and 5).
// The device is named by its id inside SSH; no list carries its code
// (rule 3).
func TestEdgeFirstDevicePendingUntilApproved(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inv := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop, phone := newSigner(t), newSigner(t)
	code := e.refusedForApproval(t, octo, laptop, "laptop")
	if code != store.ApprovalCode(edgeproto.DeviceKeyLine(laptop.PublicKey())) {
		t.Fatalf("approval code %s is not derived from the device key", code)
	}
	ctx := context.Background()
	if m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a waiting invitation device bound the account to %+v, %v; want no member", m, err)
	}
	if members, _ := e.store.ListMembers(ctx); len(members) != 1 {
		t.Fatalf("members = %d, want only the admin", len(members))
	}
	if open, err := identities(t, e).GetInvitation(ctx, inv.ID); err != nil || open.ConsumedAt != nil {
		t.Fatalf("invitation after the first connection = %+v, %v; want it open", open, err)
	}
	var list protocol.MemberDeviceListResult
	admin := controlClient(t, e)
	if err := admin.Call(protocol.MethodMemberDeviceList, struct{}{}, &list); err != nil {
		t.Fatalf("device list: %v", err)
	}
	if len(list.Devices) != 1 || list.Devices[0].Label != "laptop" || list.Devices[0].InvitationID != string(inv.ID) ||
		list.Devices[0].Account != "octo" || strings.Contains(fmt.Sprint(list), code) {
		t.Fatalf("device list %+v: want the laptop waiting on the invitation, as octo, without its approval code", list)
	}

	// Another member may not approve it; an admin may.
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	var pe *protocol.Error
	err := approveByCode(controlAs(t, e, bob), code, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("other member approving = %v, want CodeDenied", err)
	}
	var approved protocol.MemberDeviceResult
	if err = approveByCode(admin, strings.ToLower(code), &approved); err != nil {
		t.Fatalf("admin approve: %v", err)
	}
	if approved.Device.Status != string(domain.DeviceApproved) || approved.Device.ApprovedBy != string(e.member.ID) {
		t.Fatalf("approved device = %+v", approved.Device)
	}
	m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject)
	if err != nil || m.Role != domain.RoleCollaborator || m.Pending || approved.Device.MemberID != string(m.ID) {
		t.Fatalf("member after approval = %+v, %v; want the invited collaborator owning the device", m, err)
	}
	if used, err := identities(t, e).GetInvitation(ctx, inv.ID); err != nil || used.ConsumedAt == nil {
		t.Fatalf("invitation after approval = %+v, %v; want it used", used, err)
	}
	first := controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))

	// A new key is a new device, and waits too (rule 7); the member
	// approves it from the approved one.
	code = e.refusedForApproval(t, octo, phone, "phone")
	if err := approveByCode(first, code, &approved); err != nil {
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
// attempts, the code destroyed once it claimed, and the admin member the
// code names, if any.
type fakeClaimCode struct {
	mu       sync.Mutex
	left     int
	used     bool
	admin    string
	attempts []string
}

func newFakeClaimCode() *fakeClaimCode { return &fakeClaimCode{left: edgeproto.ClaimCodeAttempts} }

func (f *fakeClaimCode) attempt(code string, claim func(admin string) error) error {
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
	if err := claim(f.admin); err != nil {
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
	e.invite(t, controlClient(t, e), protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Login: "ada"})
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

// An edge that relays a person's device key with a grant for another
// account is refused before any device is recorded, so the code the
// person hands an approver can bind only the account they signed in as:
// on an invitation, and on a member's identity.
func TestRelayedConnectionThroughAnEdgeThatSubstitutesTheAccount(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	forged := octo
	forged.Subject = "6666"
	victim := newSigner(t)
	_, banner, err := e.dialEdge(t, grantFor(forged, victim, "laptop"), victim, edgeproto.AccountUser(octo))
	if err == nil || !strings.Contains(banner, "the edge signed the connection in as github account octo (github:6666)") {
		t.Fatalf("relayed connection with a substituted account = %v, banner %q", err, banner)
	}
	if _, err = identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(victim.PublicKey())); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a substituted grant recorded the device: %v", err)
	}
	approveAsAdmin(t, e, e.refusedForApproval(t, octo, victim, "laptop"))
	if m, merr := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject); merr != nil || m.Role != domain.RoleAdmin {
		t.Fatalf("the approved invitation bound %+v, %v; want octo's own account", m, merr)
	}
	phone := newSigner(t)
	_, banner, err = e.dialEdge(t, grantFor(octo, phone, "phone"), phone, edgeproto.AccountUser(mallory()))
	if err == nil || !strings.Contains(banner, "nothing was recorded") {
		t.Fatalf("relayed connection with a grant for a member's account = %v, banner %q", err, banner)
	}
	if devs, _ := identities(t, e).ListDevices(context.Background(), ""); len(devs) != 1 {
		t.Fatalf("devices = %+v, want only the approved laptop", devs)
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

// A link binds an account only through its join link, so a member who
// is not an admin can name another person's account but not take it: that
// person, joining with an admin's invitation, becomes a member of their
// own.
func TestMemberCannotLinkAnotherAccount(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	carolKey, carol := addMember(t, e, "Carol", domain.RoleViewer, false)
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	if err := controlAs(t, e, carolKey).Call(protocol.MethodMemberIdentityLink,
		protocol.MemberIdentityLinkParams{Login: "octo"}, nil); err != nil {
		t.Fatalf("viewer linking an account to themself: %v", err)
	}
	e.invite(t, controlClient(t, e), protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Login: "octo", Role: "collaborator"})
	got := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, newSigner(t), "laptop")))
	if got.ID == string(carol.ID) || got.DisplayName != "Octo Cat" || got.Role != string(domain.RoleCollaborator) {
		t.Fatalf("octo joined as %+v, want a new collaborator from the admin's invitation", got)
	}
}

// An invitation grants its role when accepted, so it goes when its creator
// stops being an admin.
func TestDemotedAdminsInvitationsAreRevoked(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	carolKey, carol := addMember(t, e, "Carol", domain.RoleAdmin, false)
	e.invite(t, controlAs(t, e, carolKey), protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Login: "octo", Role: "admin"})
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
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	var pe *protocol.Error
	err := controlClient(t, e).Call(protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Login: "octo", Role: "collaborator"}, nil)
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
	params := protocol.MemberInvitationCreateParams{Login: "octo", Role: "collaborator"}

	var pe *protocol.Error
	if err := admin.Call(protocol.MethodMemberInvitationCreate, params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState ||
		pe.Message != protocol.MethodMemberInvitationCreate+": this server is not enrolled with an edge (edge-url is empty)" {
		t.Fatalf("invitation.create without an edge = %v, want CodeInvalidState", err)
	}
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	if err := bob.Call(protocol.MethodMemberInvitationCreate, params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator invitation.create = %v, want CodeDenied", err)
	}
	bad := protocol.MemberInvitationCreateParams{Login: "octo", Email: "octo@example.com", Role: "viewer"}
	if err := admin.Call(protocol.MethodMemberInvitationCreate, bad, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams ||
		!strings.Contains(pe.Message, "give a GitHub login or the verified primary email of a GitHub account") {
		t.Fatalf("invitation naming a login and an email = %v, want CodeInvalidParams", err)
	}
	changed := e.srv.EdgeDirectoryChanged()
	for len(changed) > 0 {
		<-changed
	}
	created := e.invite(t, admin, protocol.MethodMemberInvitationCreate, params)
	<-changed
	edgeURL, serverID, _, secret, _ := edgeproto.ParseJoinLink(created.Link)
	if edgeURL != "https://edge.example" || serverID != edgeproto.ServerID(e.srv.hostKey.PublicKey()) || created.ServerID != serverID {
		t.Fatalf("invitation.create = %+v; want a link to this server through its edge", created)
	}
	if stored, err := identities(t, e).GetInvitation(context.Background(), domain.InvitationID(created.Invitation.ID)); err != nil ||
		stored.SecretHash != edgeproto.HashToken(secret) {
		t.Fatalf("stored invitation = %+v, %v; want only the secret's hash", stored, err)
	}
	var raw json.RawMessage
	if err := admin.Call(protocol.MethodMemberInvitationList, struct{}{}, &raw); err != nil || strings.Contains(string(raw), secret) {
		t.Fatalf("invitation.list = %s, %v; want no secret", raw, err)
	}
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

// Any member reads the server's edge, and it carries only what `aether
// servers` shows: no claim code, token or key, and of the owner only the
// provider and login.
func TestServerEdge(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	bobKey, _ := addMember(t, e, "Bob", domain.RoleViewer, false)
	bob := controlAs(t, e, bobKey)
	id := edgeproto.ServerID(e.srv.hostKey.PublicKey())

	var off protocol.ServerEdgeResult
	if err := bob.Call(protocol.MethodServerEdge, struct{}{}, &off); err != nil ||
		off != (protocol.ServerEdgeResult{ServerID: id, AccessPolicy: string(edgeproto.PolicyApprovedDevices)}) {
		t.Fatalf("server.edge with the edge off = %+v, %v", off, err)
	}
	e.srv.SetEdgeOwner(&fakeEdgeOwner{owner: &octo, state: edgeproto.StateClaimed})
	var raw map[string]any
	if err := bob.Call(protocol.MethodServerEdge, struct{}{}, &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"edge_url": "https://edge.example", "server_id": id, "access_policy": "approved-devices", "connected": true,
		"claimed": true, "owner": map[string]any{"provider": "github", "login": "octo"}}
	if fmt.Sprint(raw) != fmt.Sprint(want) {
		t.Fatalf("server.edge = %v, want %v", raw, want)
	}
}

// While the edge reports the server unclaimed, invitations and links are
// refused before anything is stored. A claimed server without an owner
// keeps relaying for its members, so they are made there.
func TestUnclaimedServerRefusesInvitationsAndOwnerlessOneDoesNot(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	admin := controlClient(t, e)
	e.srv.SetEdgeOwner(&fakeEdgeOwner{state: edgeproto.StateUnclaimed})
	calls := map[string]any{
		protocol.MethodMemberInvitationCreate: protocol.MemberInvitationCreateParams{Login: "octo", Role: "collaborator"},
		protocol.MethodMemberIdentityLink:     protocol.MemberIdentityLinkParams{Login: "octo-alt"},
	}
	var pe *protocol.Error
	for method, params := range calls {
		if err := admin.Call(method, params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState ||
			!strings.HasPrefix(pe.Message, method+": the edge has not recorded this server yet") || !strings.Contains(pe.Message, "aether edge connect") {
			t.Fatalf("%s on an unclaimed server = %v, want CodeInvalidState", method, err)
		}
	}
	if invs, _ := identities(t, e).ListInvitations(context.Background()); len(invs) != 0 {
		t.Fatalf("refused calls stored %+v", invs)
	}
	e.srv.SetEdgeOwner(&fakeEdgeOwner{state: edgeproto.StateClaimed})
	for method, params := range calls {
		if err := admin.Call(method, params, nil); err != nil {
			t.Fatalf("%s on an ownerless claimed server: %v", method, err)
		}
	}
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
			if _, _, err := e.dialEdge(t, grantFor(octo, laptop, "laptop"), laptop, e.edgeUser(t, octo)); (err == nil) != (policy == edgeproto.PolicyAccount) {
				t.Fatalf("first relayed connection under %s: %v", policy, err)
			}
			var banner strings.Builder
			if _, err := e.dialWith(laptop, &banner); err == nil || !strings.Contains(banner.String(), "aether device approve") {
				t.Fatalf("direct dial with an unapproved device key: %v, banner %q", err, banner.String())
			}
			if err := approveByCode(controlClient(t, e), e.approvalCode(t, laptop, banner.String()), nil); err != nil {
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
