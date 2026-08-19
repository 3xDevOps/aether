package reachability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// DefaultTailscaledSocket is where tailscaled's LocalAPI listens on Linux
// (same path internal/sshd's WhoIs client uses).
const DefaultTailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// Tailscale discovers the server's tailnet MagicDNS name from the local
// tailscaled's LocalAPI status endpoint. Like the sshd WhoIs client it is
// a minimal internal client (one GET, one response field) rather than a
// dependency on the tailscale.com/client/local module. Any failure - no
// socket, daemon down, empty DNS name - means "not present": callers fall
// through to the next adapter.
type Tailscale struct {
	client *http.Client
}

// NewTailscale builds an adapter talking to the tailscaled unix socket at
// socketPath (empty = DefaultTailscaledSocket).
func NewTailscale(socketPath string) *Tailscale {
	if socketPath == "" {
		socketPath = DefaultTailscaledSocket
	}
	return &Tailscale{client: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

// Name implements Adapter.
func (*Tailscale) Name() string { return "tailscale" }

// Discover implements Adapter: GET /localapi/v0/status and read
// Self.DNSName, trimming the trailing MagicDNS dot.
func (t *Tailscale) Discover(ctx context.Context) (Endpoint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://local-tailscaled.sock/localapi/v0/status", nil)
	if err != nil {
		return Endpoint{}, fmt.Errorf("status request: %w", err)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return Endpoint{}, fmt.Errorf("status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return Endpoint{}, fmt.Errorf("status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var st struct {
		Self struct {
			DNSName string
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return Endpoint{}, fmt.Errorf("status decode: %w", err)
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" {
		return Endpoint{}, fmt.Errorf("status has no DNS name")
	}
	return Endpoint{Host: name, Label: "tailnet"}, nil
}
