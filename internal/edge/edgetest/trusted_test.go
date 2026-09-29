package edgetest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// nonLoopback returns an IPv4 address of this machine that is not
// loopback: the address a proxy in another container reaches the edge
// from.
func nonLoopback(t *testing.T) netip.Addr {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				if ip = ip.Unmap(); ip.Is4() && ip.IsGlobalUnicast() {
					return ip
				}
			}
		}
	}
	t.Skip("this machine has no non-loopback IPv4 address to reach the edge from")
	return netip.Addr{}
}

// newHarnessBehind is newHarness with the edge as `aether-edge serve
// --proxy-listen <addr>:<port> --trusted-proxies <addr>/32` runs it: the
// proxy reaches the edge's listener on addr from addr, not from loopback,
// and forwards each client's address.
func newHarnessBehind(t *testing.T, addr netip.Addr) *harness {
	t.Helper()
	h := &harness{t: t, github: newFakeGitHub(t), edgeDir: t.TempDir(), proxy: newProxy(),
		frontAddr: "127.0.0.1:0", backAddr: netip.AddrPortFrom(addr, 0).String(),
		proxies: []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())}}
	h.startEdge()
	t.Cleanup(h.stopEdge)
	return h
}

// TestBehindTrustedProxies runs the whole GitHub-only flow through an
// edge whose reverse proxy is not on its host, under each access policy:
// servers enroll, people sign in with GitHub, the owner claims a server
// and invites a collaborator, and both call a method over the relay. The
// edge records the address the proxy forwarded, not the proxy's own, and
// refuses a request that reaches it from any other peer, loopback
// included. Google sign-in, which builds from the v0.5.2-alpha.3 tag
// offered, has no route.
func TestBehindTrustedProxies(t *testing.T) {
	t.Parallel()
	proxyAddr := nonLoopback(t)
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarnessBehind(t, proxyAddr)
			s := h.newServer(policy)
			cs := h.login(alice, bob)
			al, bo := cs[0], cs[1]
			h.claimServer(al, s)
			ctl := h.control(al, s)
			inviteLogin(t, ctl, bo, s.id, "collaborator")
			if policy == edgeproto.PolicyApprovedDevices {
				_, err := h.dial(bo, h.link(s))
				approve(t, ctl, waitingCode(t, "bob's first device", err))
			}
			for _, c := range []*client{al, bo} {
				info := call[protocol.ServerInfoResult](t, h.control(c, s), protocol.MethodServerInfo, struct{}{})
				if want := map[*client]string{al: "admin", bo: "collaborator"}[c]; info.Member.Role != want {
					t.Fatalf("%s over the relay: %+v, want %s", c.user.Login, info.Member, want)
				}
			}

			// The device page shows the address the proxy accepted the
			// browser's connection from, 127.0.0.1, not the proxy's.
			ec, err := edgeclient.New(filepath.Join(t.TempDir(), "aether"), h.relayURL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
			defer cancel()
			l, err := ec.StartLogin(ctx, "carol-laptop")
			if err != nil {
				t.Fatal(err)
			}
			h.advance(5 * time.Minute)
			b, err := h.signIn(carol)
			if err != nil {
				t.Fatal(err)
			}
			resp, page, err := b.post("/device", url.Values{"user_code": {l.UserCode}})
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("enter the code: %v %v\n%s", err, resp, page)
			}
			if !strings.Contains(page, "<code>127.0.0.1</code> (the address of this browser)") || strings.Contains(page, proxyAddr.String()) {
				t.Fatalf("the device page shows another address than the one the proxy forwarded:\n%s", page)
			}
			for _, path := range []string{"/signin/google?next=/device", "/signin/google/callback?code=x&state=y"} {
				if resp, _, err = b.do(http.MethodGet, path, nil); err != nil || resp.StatusCode != http.StatusNotFound {
					t.Fatalf("GET %s: %v %v, want 404", path, resp, err)
				}
			}

			// A peer outside the trusted network reaches the edge's
			// listener directly: loopback, here.
			direct := &http.Client{Timeout: waitTimeout, Transport: &http.Transport{
				DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}}).DialContext,
			}}
			resp, err = direct.Get("http://" + h.backAddr + "/healthz")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close() //nolint:errcheck,gosec // read in full
			if want := "127.0.0.1 is not in the edge's --trusted-proxies"; resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), want) {
				t.Fatalf("request from loopback past the proxy: %s %s, want 403 saying %q", resp.Status, body, want)
			}
			if n := h.relay().Metrics().Refusals["not from a trusted proxy"]; n != 1 {
				t.Fatalf("%d refusals counted as not from a trusted proxy, want 1", n)
			}
		})
	}
}
