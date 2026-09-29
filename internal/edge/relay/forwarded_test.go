package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	for _, v := range xff {
		req.Header.Add(HeaderForwardedFor, v)
	}
	rec := httptest.NewRecorder()
	e.r.Forwarded(echoAddr).ServeHTTP(rec, req)
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
	held := e.r.Forwarded(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
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
