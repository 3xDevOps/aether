package edgetest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func wantRefusal(t *testing.T, what string, err error, want edgeproto.Refusal) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, want %q", what, err, want)
	}
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// connectWith asks the edge for a relayed connection with token as the
// bearer token, and returns the refusal.
func (h *harness) connectWith(t *testing.T, serverID, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.origin+edgeproto.ConnectPath(serverID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	var body edgeproto.ErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Error
}

var approveCommand = regexp.MustCompile(`aether device approve (\S+)`)

func TestClaimAndAccess(t *testing.T) {
	h := newHarness(t)
	a, b := h.newServer(), h.newServer()
	cs := h.login(alice, bob, alice)
	al, bo, al2 := cs[0], cs[1], cs[2]

	_, err := h.claim(al, a.claimCode(t, time.Now().Add(-edgeproto.ClaimCodeTTL-time.Minute)))
	wantRefusal(t, "claim with an expired code", err, edgeproto.RefusalClaimExpired)

	code := a.claimCode(t, time.Now())
	wrong := code[:edgeproto.ClaimPrefixLength+1] + strings.Repeat("a", len(code)-edgeproto.ClaimPrefixLength-1)
	for range edgeproto.ClaimCodeAttempts - 1 {
		_, err = h.claim(al, wrong)
		wantRefusal(t, "claim with a wrong code", err, edgeproto.RefusalClaimWrong)
	}
	_, err = h.claim(al, wrong)
	wantRefusal(t, "last wrong attempt", err, edgeproto.RefusalClaimExhausted)
	_, err = h.claim(al, code)
	wantRefusal(t, "right code after the attempts ran out", err, edgeproto.RefusalClaimExhausted)

	h.claimServer(al, a)
	_, err = h.claim(bo, a.claimCode(t, time.Now()))
	wantRefusal(t, "second claim of a claimed server", err, edgeproto.RefusalClaimed)
	h.claimServer(bo, b)

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	servers, err := al.edge.Servers(ctx)
	if err != nil || len(servers) != 1 || servers[0].ID != a.id || servers[0].Role != "admin" || !servers[0].Online {
		t.Fatalf("alice's servers = %+v %v, want %s as admin, online", servers, err, a.id)
	}

	// A control RPC over the relay: the claim made alice the admin.
	ctl := h.control(al, a)
	members := call[protocol.MemberListResult](t, ctl, protocol.MethodMemberList, struct{}{})
	if len(members.Members) != 1 || members.Members[0].Role != "admin" {
		t.Fatalf("members after the claim = %+v", members.Members)
	}

	// Cross-server isolation.
	_, err = h.dial(al, h.link(b))
	wantRefusal(t, "owner of A connecting to B", err, edgeproto.RefusalNotMember)
	_, err = h.dial(bo, h.link(a))
	wantRefusal(t, "owner of B connecting to A", err, edgeproto.RefusalNotMember)

	// No token, or a token the edge never issued.
	if status, msg := h.connectWith(t, a.id, ""); status != http.StatusUnauthorized || msg != string(edgeproto.RefusalTokenRequired) {
		t.Errorf("connect without a token = %d %q", status, msg)
	}
	if status, msg := h.connectWith(t, a.id, edgeproto.NewToken()); status != http.StatusUnauthorized || msg != string(edgeproto.RefusalTokenRevoked) {
		t.Errorf("connect with an unknown token = %d %q", status, msg)
	}

	// Alice's device token, but a key other than her device key: the
	// holder of a stolen token cannot authenticate to the server.
	nc, err := al.edge.Dial(ctx, a.id)
	if err != nil {
		t.Fatal(err)
	}
	var banner strings.Builder
	_, _, _, err = ssh.NewClientConn(nc, "edge", &ssh.ClientConfig{
		User: "aether",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(newSigner(t))},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !edgeproto.HostKeyMatches(key, a.id) {
				return errors.New("host key is not server " + a.id)
			}
			return nil
		},
		BannerCallback: func(m string) error { banner.WriteString(m); return nil },
	})
	_ = nc.Close()
	if err == nil || !strings.Contains(banner.String(), "not the device key") {
		t.Fatalf("handshake with a key other than the grant's: %v, banner %q", err, banner.String())
	}

	// Alice's second device waits for approval from her first.
	_, err = h.dial(al2, h.link(a))
	m := approveCommand.FindStringSubmatch(errString(err))
	if m == nil || !strings.Contains(err.Error(), "waiting for approval") {
		t.Fatalf("second device: %v, want a pending refusal naming the approval command", err)
	}
	approved := call[protocol.MemberDeviceResult](t, ctl, protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: m[1]})
	if approved.Device.Status != "approved" {
		t.Fatalf("approve: %+v", approved.Device)
	}
	h.mustDial(al2, a)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
