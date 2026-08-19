package reachability

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAdapter is a canned Adapter for ordering tests.
type fakeAdapter struct {
	name string
	ep   Endpoint
	err  error
}

func (f fakeAdapter) Name() string                               { return f.name }
func (f fakeAdapter) Discover(context.Context) (Endpoint, error) { return f.ep, f.err }

// Discover returns the first adapter's endpoint when it succeeds and
// falls through to later adapters when earlier ones fail.
func TestDiscoverOrdering(t *testing.T) {
	ctx := context.Background()
	ts := fakeAdapter{name: "tailscale", ep: Endpoint{Host: "box.tailnet.ts.net", Label: "tailnet"}}
	host := fakeAdapter{name: "host", ep: Endpoint{Host: "box", Label: "host"}}
	broken := fakeAdapter{name: "tailscale", err: errors.New("no tailscaled")}

	ep, err := Discover(ctx, ts, host)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if ep.Host != "box.tailnet.ts.net" || ep.Label != "tailnet" {
		t.Errorf("first adapter not preferred: %+v", ep)
	}

	ep, err = Discover(ctx, broken, host)
	if err != nil {
		t.Fatalf("Discover with broken first: %v", err)
	}
	if ep.Host != "box" || ep.Label != "host" {
		t.Errorf("fallback endpoint = %+v", ep)
	}

	if _, err = Discover(ctx, broken, fakeAdapter{name: "host", err: errors.New("no hostname")}); err == nil {
		t.Error("all-failing adapters did not error")
	}
	if _, err = Discover(ctx); err == nil {
		t.Error("zero adapters did not error")
	}
}

// The Host adapter always yields the OS hostname.
func TestHostAdapter(t *testing.T) {
	ep, err := Host{}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Host.Discover: %v", err)
	}
	want, _ := os.Hostname()
	if ep.Host != want || ep.Label != "host" || ep.Port != 0 {
		t.Errorf("endpoint = %+v, want host %q", ep, want)
	}
}

// startStatusServer serves a canned LocalAPI /localapi/v0/status body on
// a unix socket, skipping the test where unix sockets are unavailable
// (Windows) - same pattern as sshd's TestLocalWhoIs.
func startStatusServer(t *testing.T, body string, status int) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/status" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// The Tailscale adapter reads Self.DNSName from the LocalAPI status
// endpoint and trims the trailing MagicDNS dot.
func TestTailscaleDiscover(t *testing.T) {
	sock := startStatusServer(t, `{"Self":{"DNSName":"box.tail1234.ts.net."}}`, http.StatusOK)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ep, err := NewTailscale(sock).Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if ep.Host != "box.tail1234.ts.net" || ep.Label != "tailnet" || ep.Port != 0 {
		t.Errorf("endpoint = %+v", ep)
	}
}

// Any LocalAPI failure means "not present": missing socket, HTTP error
// status, empty DNS name.
func TestTailscaleDiscoverFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dead := NewTailscale(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := dead.Discover(ctx); err == nil {
		t.Error("missing socket did not error")
	}

	sock := startStatusServer(t, "denied", http.StatusForbidden)
	if _, err := NewTailscale(sock).Discover(ctx); err == nil {
		t.Error("HTTP 403 did not error")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("403 error = %v, want status in message", err)
	}

	empty := startStatusServer(t, `{"Self":{"DNSName":""}}`, http.StatusOK)
	if _, err := NewTailscale(empty).Discover(ctx); err == nil {
		t.Error("empty DNS name did not error")
	}
}
