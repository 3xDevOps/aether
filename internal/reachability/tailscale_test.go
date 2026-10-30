package reachability

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// Self reads Self.DNSName from the LocalAPI status endpoint and trims the
// trailing MagicDNS dot.
func TestTailscaleSelfDNSName(t *testing.T) {
	sock := startStatusServer(t, `{"Self":{"DNSName":"box.tail1234.ts.net."}}`, http.StatusOK)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewTailscale(sock).Self(ctx)
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if node.DNSName != "box.tail1234.ts.net" {
		t.Errorf("DNSName = %q", node.DNSName)
	}
}

// Self names the tailnet by the name tailscaled reports, or by its MagicDNS
// suffix where tailscaled reports none.
func TestTailscaleSelfNamesTheTailnet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for body, want := range map[string]string{
		`{"Self":{"DNSName":"box.tail1234.ts.net."},"MagicDNSSuffix":"tail1234.ts.net","CurrentTailnet":{"Name":"alice@example.com"}}`: "alice@example.com",
		`{"Self":{"DNSName":"box.tail1234.ts.net."},"MagicDNSSuffix":"tail1234.ts.net","CurrentTailnet":null}`:                         "tail1234.ts.net",
	} {
		node, err := NewTailscale(startStatusServer(t, body, http.StatusOK)).Self(ctx)
		if err != nil || node.Tailnet != want {
			t.Errorf("Self(%s) = %q, %v; want tailnet %q", body, node.Tailnet, err, want)
		}
	}
}

// Self errors on a missing socket, an HTTP error status and an empty DNS
// name.
func TestTailscaleSelfFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dead := NewTailscale(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := dead.Self(ctx); err == nil {
		t.Error("missing socket did not error")
	}

	sock := startStatusServer(t, "denied", http.StatusForbidden)
	if _, err := NewTailscale(sock).Self(ctx); err == nil {
		t.Error("HTTP 403 did not error")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("403 error = %v, want status in message", err)
	}

	empty := startStatusServer(t, `{"Self":{"DNSName":""}}`, http.StatusOK)
	if _, err := NewTailscale(empty).Self(ctx); err == nil {
		t.Error("empty DNS name did not error")
	}
}
