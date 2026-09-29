package edgetest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// deadAddr is a loopback address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestEdgeRestartAndDirectFallback(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)
	relayed := h.mustDial(al, a)
	both := cli.Config{ServerID: a.id, EdgeURL: h.relayURL, Addr: a.addr}
	deadDirect := cli.Config{ServerID: a.id, EdgeURL: h.relayURL, Addr: deadAddr(t)}

	start := time.Now()
	h.stopEdge()
	closedWithin(t, "the edge stopping", relayed, start, waitTimeout)

	// The server keeps serving its direct address, and a link that has
	// one uses it while the edge is down.
	sc, err := h.dial(al, both)
	if err != nil {
		t.Fatalf("direct address while the edge is down: %v", err)
	}
	_ = sc.Close()
	_, err = h.dial(al, deadDirect)
	if err == nil || !strings.Contains(err.Error(), "direct: ") || !strings.Contains(err.Error(), "edge: ") {
		t.Fatalf("neither path reachable: %v, want both causes", err)
	}

	// The server enrolls again once the edge is back, still claimed.
	h.startEdge()
	eventually(t, "server back on the restarted edge", func() error {
		if !h.relay().Online(a.id) {
			return errors.New("not online")
		}
		return nil
	})
	h.mustDial(al, a)
	sc, err = h.dial(al, deadDirect)
	if err != nil {
		t.Fatalf("edge after the direct address failed: %v", err)
	}
	_ = sc.Close()
	servers, err := al.edge.Servers(context.Background())
	if err != nil || len(servers) != 1 || servers[0].ID != a.id || servers[0].Role != "admin" {
		t.Fatalf("servers after the restart = %+v %v", servers, err)
	}
}

// TestServerRemovedWhileOffline removes a server on the edge's Servers
// page while it is disconnected. When it comes back the edge knows no
// owner, and a new claim code claims it again.
func TestServerRemovedWhileOffline(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}

	h.proxy.cutServers()
	eventually(t, "server off the edge", func() error {
		if h.relay().Online(a.id) {
			return errors.New("still online")
		}
		return nil
	})
	resp, page, err := b.post("/servers/remove", url.Values{"server": {a.id}})
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove on the Servers page: %v %v\n%s", err, resp, page)
	}
	from := h.proxy.mark()
	h.proxy.restoreServers()
	h.proxy.await(t, "the server back, unclaimed", from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && r.ServerID == a.id && r.State == edgeproto.StateUnclaimed
	})

	_, err = h.dial(al, h.link(a))
	wantRefusal(t, "connect to a removed server", err, edgeproto.RefusalUnknownServer)
	h.claimServer(al, a)
	h.mustDial(al, a)
}

// A server the operator blocks, as `aether-edge servers block` does, is
// refused at its next enrollment, and its status, which
// `aether-server edge status` prints, says why.
func TestBlockedServerStatusSaysWhy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	h.claimServer(h.login(alice)[0], a)
	if err := h.edgeStore().BlockServer(context.Background(), a.id, time.Now()); err != nil {
		t.Fatal(err)
	}
	h.proxy.cutServers()
	h.proxy.restoreServers()
	eventually(t, "the server's status names the block", func() error {
		status, _, err := a.state().Status()
		if err == nil && (status.Connected || !strings.Contains(status.Error, string(edgeproto.RefusalServerBlocked))) {
			err = fmt.Errorf("status %+v", status)
		}
		return err
	})
}

// keyMember adds a member who reaches the server by SSH key alone, as
// `aether link <address>` with an invite code or an admin's key does.
func keyMember(t *testing.T, s *serverNode, name string, role domain.Role) (*domain.Member, ssh.Signer) {
	t.Helper()
	key := newSigner(t)
	m := &domain.Member{DisplayName: name, Color: "#4363d8", Role: role, PublicKey: string(ssh.MarshalAuthorizedKey(key.PublicKey()))}
	if err := s.db.CreateMember(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m, key
}

// dialKey connects to s's address with an SSH key, as a key-linked
// client does.
func dialKey(s *serverNode, key ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", s.addr, &ssh.ClientConfig{
		User: "aether",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if !edgeproto.HostKeyMatches(k, s.id) {
				return errors.New("host key is not server " + s.id)
			}
			return nil
		},
		Timeout: waitTimeout,
	})
}

// Switching from account access to approved devices inherits nothing:
// every device registered by signing in is refused until someone
// reviews it, while approved devices, member SSH keys and tailnet
// identities keep working.
func TestPolicySwitchToApprovedDevices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyAccount)
	cs := h.login(alice, alice, bob)
	al, al2, bo := cs[0], cs[1], cs[2]
	h.claimServer(al, a)
	h.join(h.control(al, a), bo, a, "collaborator")
	h.mustDial(al2, a)
	keyed, key := keyMember(t, a, "Keyed", domain.RoleCollaborator)
	for _, c := range []*client{al2, bo} {
		if got := a.deviceStatus(t, c); got != domain.DeviceRegistered {
			t.Fatalf("%s's device under account access is %q, want registered", c.user.Login, got)
		}
	}

	from := h.proxy.mark()
	a.restart(edgeproto.PolicyApprovedDevices)
	hello := h.proxy.await(t, "the hello after the switch", from, func(e logEntry) bool {
		_, ok := e.msg.(edgeproto.Hello)
		return ok && e.serverID == a.id
	}).msg.(edgeproto.Hello)
	if hello.AccessPolicy != edgeproto.PolicyApprovedDevices {
		t.Fatalf("hello announces %q", hello.AccessPolicy)
	}
	eventually(t, "the edge lists the new policy", func() error {
		servers, err := bo.edge.Servers(context.Background())
		if err == nil && (len(servers) != 1 || servers[0].AccessPolicy != edgeproto.PolicyApprovedDevices) {
			err = fmt.Errorf("servers %+v", servers)
		}
		return err
	})

	for _, c := range []*client{al2, bo} {
		_, err := h.dial(c, h.link(a))
		code := waitingCode(t, c.user.Login+"'s registered device after the switch", err)
		if !strings.Contains(err.Error(), "admitted by signing in alone") {
			t.Fatalf("%s's refusal does not say why: %v", c.user.Login, err)
		}
		if c == bo {
			// `sudo aether-server device review` approves it on the machine.
			a.consoleApprove(t, code)
		}
	}
	h.mustDial(al, a)
	h.mustDial(bo, a)
	sc, err := dialKey(a, key)
	if err != nil {
		t.Fatalf("member SSH key after the switch: %v", err)
	}
	defer sc.Close() //nolint:errcheck // test client
	if info := call[protocol.ServerInfoResult](t, controlOver(t, sc), protocol.MethodServerInfo, struct{}{}); info.Member.ID != string(keyed.ID) {
		t.Fatalf("key connection is %+v", info.Member)
	}
	var info protocol.ServerInfoResult
	if err := a.local(t, keyed.ID, protocol.MethodServerInfo, struct{}{}, &info); err != nil || info.Member.ID != string(keyed.ID) {
		t.Fatalf("tailnet dashboard after the switch: %+v %v", info.Member, err)
	}
}

// newKeyFile creates an SSH key and saves it as an OpenSSH private key
// file, as ~/.ssh/id_ed25519.
func newKeyFile(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, path
}

// The paths that never involve the edge work with the edge down, on a
// server that enrolled with it: an invite code redeemed with an SSH key,
// that key afterwards, and the in-process client of the tailnet-hosted
// dashboard.
func TestExistingPathsWithoutTheEdge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)
	code := call[protocol.MemberInviteResult](t, h.control(al, a), protocol.MethodMemberInvite, protocol.MemberInviteParams{}).Code
	h.stopEdge()

	signer, keyPath := newKeyFile(t)
	cfg := cli.Config{Addr: a.addr, Key: keyPath, KnownHosts: filepath.Join(t.TempDir(), "known_hosts")}
	for i, dial := range []func() (*cli.Conn, error){
		func() (*cli.Conn, error) { return cli.DialInvite(cfg, code, "Newcomer") },
		func() (*cli.Conn, error) { return cli.Dial(cfg) },
	} {
		conn, err := dial()
		if err != nil {
			t.Fatalf("dial %d with the invited key: %v", i, err)
		}
		ctl, err := conn.Control()
		if err != nil {
			t.Fatal(err)
		}
		if info := call[protocol.ServerInfoResult](t, ctl, protocol.MethodServerInfo, struct{}{}); info.Member.DisplayName != "Newcomer" {
			t.Fatalf("invited member = %+v", info.Member)
		}
		_ = conn.Close()
	}
	if _, err := dialKey(a, signer); err != nil {
		t.Fatalf("the invited key, raw: %v", err)
	}
	var list protocol.MemberListResult
	if err := a.local(t, a.memberOf(t, alice).ID, protocol.MethodMemberList, struct{}{}, &list); err != nil || len(list.Members) != 2 {
		t.Fatalf("tailnet dashboard with the edge down: %+v %v", list, err)
	}
}

// The server trusts the edge's signing key, not its host name. When the
// edge's relay host name changes and the server's edge-url follows, the
// server keeps its pin and its owner, the edge keeps the server claimed,
// and an admin still transfers ownership.
func TestRelayHostNameChangeKeepsPinAndOwner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	h.claimServer(al, a)
	h.join(h.control(al, a), bo, a, "admin")
	pin, err := a.state().PinnedKey()
	if err != nil || pin == nil {
		t.Fatalf("pin before the rename = %x, %v", pin, err)
	}
	oldRelay := h.relayURL

	h.renameHosts()
	a.restart(a.policy)
	if h.relayURL == oldRelay {
		t.Fatalf("the relay origin is still %s", oldRelay)
	}
	if got, err := a.state().PinnedKey(); err != nil || !got.Equal(pin) {
		t.Fatalf("pin after the rename = %x, %v; want the edge's key", got, err)
	}
	if owner, err := a.state().Owner(); err != nil || owner == nil || owner.Login != alice.Login {
		t.Fatalf("owner after the rename = %+v, %v; want alice", owner, err)
	}
	h.waitEdgeOwner(t, a, &alice)

	var res protocol.ServerOwnerTransferResult
	if err := a.local(t, a.memberOf(t, alice).ID, protocol.MethodServerOwnerTransfer,
		protocol.ServerOwnerTransferParams{MemberID: string(a.memberOf(t, bob).ID)}, &res); err != nil {
		t.Fatalf("transfer after the rename: %v", err)
	}
	h.waitEdgeOwner(t, a, &bob)
}
