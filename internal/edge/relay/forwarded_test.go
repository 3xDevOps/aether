package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// echoAddr answers with the client address the handler sees.
var echoAddr = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, r.RemoteAddr)
})

func serveForwarded(t *testing.T, e *env, remote string, xff ...string) (int, string) {
	t.Helper()
	return serveForwardedFrom(t, e, nil, remote, xff...)
}

func serveForwardedFrom(t *testing.T, e *env, proxies []netip.Prefix, remote string, xff ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	for _, v := range xff {
		req.Header.Add(HeaderForwardedFor, v)
	}
	rec := httptest.NewRecorder()
	e.r.Forwarded(proxies, echoAddr).ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		var e edgeproto.ErrorBody
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			t.Fatalf("status %d: %s", rec.Code, body)
		}
		body = e.Error
	}
	return rec.Code, body
}

func TestForwardedTakesTheProxysOwnEntry(t *testing.T) {
	e := newEnv(t)
	for _, tt := range []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"proxy appended to the client's own header", "127.0.0.1:50000", []string{"6.6.6.6, 203.0.113.9"}, "203.0.113.9:0"},
		{"client's header on a line of its own", "127.0.0.1:50000", []string{"6.6.6.6", "203.0.113.9"}, "203.0.113.9:0"},
		{"IPv6 client", "[::1]:50000", []string{"2001:db8::7"}, "[2001:db8::7]:0"},
		{"IPv4-mapped client", "127.0.0.1:50000", []string{"::ffff:203.0.113.9"}, "203.0.113.9:0"},
		// A peer that is not the proxy on this host chooses nothing.
		{"forged header from a remote peer", "198.51.100.7:4000", []string{"203.0.113.9"}, "198.51.100.7:0"},
		{"remote peer without a header", "198.51.100.7:4000", nil, "198.51.100.7:0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, got := serveForwarded(t, e, tt.remote, tt.xff...)
			if status != http.StatusOK || got != tt.want {
				t.Fatalf("got %d %q, want the address %q", status, got, tt.want)
			}
		})
	}
}

func TestForwardedRefusesWhatTheProxyDidNotSay(t *testing.T) {
	e := newEnv(t)
	for _, tt := range []struct {
		name string
		xff  []string
		want string
	}{
		{"no header", nil, "sent no X-Forwarded-For header"},
		{"empty header", []string{""}, `entry from the proxy in front of this edge, "", is not an IP address`},
		{"trailing comma", []string{"203.0.113.9,"}, `, "", is not an IP address`},
		{"not an address", []string{"203.0.113.9, unknown"}, `"unknown", is not an IP address`},
		{"address with a port", []string{"203.0.113.9:4000"}, `"203.0.113.9:4000", is not an IP address`},
		{"address with a zone", []string{"fe80::1%eth0"}, `"fe80::1%eth0", is not an IP address`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, got := serveForwarded(t, e, "127.0.0.1:50000", tt.xff...)
			if status != http.StatusBadRequest || !strings.Contains(got, tt.want) ||
				!strings.Contains(got, "proxy_set_header X-Forwarded-For $remote_addr") {
				t.Fatalf("got %d %q, want 400 with %q", status, got, tt.want)
			}
		})
	}
	if got := e.r.Metrics().Refusals["no forwarded client address"]; got != 6 {
		t.Fatalf("counted %d refusals, want 6", got)
	}
}

// TestForwardedLimitsOpenRequestsPerClient checks that behind a proxy, the
// limit on open connections counts each client, not the proxy.
func TestForwardedLimitsOpenRequestsPerClient(t *testing.T) {
	e := newEnv(t)
	e.r.maxConnsPerAddress = 1
	entered, release := make(chan struct{}), make(chan struct{})
	held := e.r.Forwarded(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set(HeaderForwardedFor, "203.0.113.9")
		held.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-entered

	if status, got := serveForwarded(t, e, "127.0.0.1:50001", "203.0.113.9"); status != http.StatusTooManyRequests {
		t.Fatalf("second open request of one client: %d %q", status, got)
	}
	if status, got := serveForwarded(t, e, "127.0.0.1:50002", "203.0.113.10"); status != http.StatusOK {
		t.Fatalf("another client behind the same proxy: %d %q", status, got)
	}
	close(release)
	<-done
	if status, got := serveForwarded(t, e, "127.0.0.1:50003", "203.0.113.9"); status != http.StatusOK {
		t.Fatalf("after the first request ended: %d %q", status, got)
	}
}

// TestForwardedFromTrustedProxies runs the edge behind a proxy in another
// container: the proxies are the networks --trusted-proxies names.
func TestForwardedFromTrustedProxies(t *testing.T) {
	e := newEnv(t)
	proxies := []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("fd00:18::/64")}
	for _, tt := range []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"proxy in the network", "172.18.0.2:50000", []string{"203.0.113.9"}, "203.0.113.9:0"},
		{"forged header through the proxy", "172.18.0.2:50000", []string{"6.6.6.6, 203.0.113.9"}, "203.0.113.9:0"},
		{"forged header naming a proxy address", "172.18.0.2:50000", []string{"172.18.0.9", "203.0.113.9"}, "203.0.113.9:0"},
		{"chain of two trusted proxies", "172.18.0.2:50000", []string{"6.6.6.6, 203.0.113.9, 172.18.0.3"}, "203.0.113.9:0"},
		// Every entry is a proxy: the left-most is the client.
		{"client inside the trusted network", "172.18.0.2:50000", []string{"172.18.0.7"}, "172.18.0.7:0"},
		{"IPv6 proxy and client", "[fd00:18::2]:50000", []string{"2001:db8::7"}, "[2001:db8::7]:0"},
		{"IPv4-mapped proxy", "[::ffff:172.18.0.2]:50000", []string{"::ffff:203.0.113.9"}, "203.0.113.9:0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, got := serveForwardedFrom(t, e, proxies, tt.remote, tt.xff...)
			if status != http.StatusOK || got != tt.want {
				t.Fatalf("got %d %q, want the address %q", status, got, tt.want)
			}
		})
	}

	// A peer outside the networks is refused, with or without a header:
	// it neither chooses an address nor is served with its own. The
	// loopback proxy of the mode without --trusted-proxies is no longer
	// trusted either.
	for _, tt := range []struct {
		name   string
		remote string
		xff    []string
	}{
		{"header from an untrusted peer", "198.51.100.7:4000", []string{"203.0.113.9"}},
		{"untrusted peer without a header", "198.51.100.7:4000", nil},
		{"untrusted IPv6 peer", "[2001:db8::66]:4000", []string{"203.0.113.9"}},
		{"untrusted IPv4-mapped peer", "[::ffff:198.51.100.7]:4000", []string{"203.0.113.9"}},
		{"loopback peer", "127.0.0.1:4000", []string{"203.0.113.9"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, got := serveForwardedFrom(t, e, proxies, tt.remote, tt.xff...)
			if status != http.StatusForbidden || !strings.Contains(got, "is not in the edge's --trusted-proxies") {
				t.Fatalf("got %d %q, want 403 naming --trusted-proxies", status, got)
			}
		})
	}
	if got := e.r.Metrics().Refusals["not from a trusted proxy"]; got != 5 {
		t.Fatalf("counted %d refusals, want 5", got)
	}

	for _, xff := range [][]string{nil, {"203.0.113.9, unknown"}} {
		status, got := serveForwardedFrom(t, e, proxies, "172.18.0.2:50000", xff...)
		if status != http.StatusBadRequest || !strings.Contains(got, "proxy_set_header X-Forwarded-For $remote_addr") {
			t.Errorf("%q from the proxy: got %d %q, want 400", xff, status, got)
		}
	}
}
