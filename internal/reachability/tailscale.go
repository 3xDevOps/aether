package reachability

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// DefaultTailscaledSocket is where tailscaled's LocalAPI listens on Linux
// (same path internal/sshd's WhoIs client uses).
const DefaultTailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// Tailscale reads what the local tailscaled knows about this node - its
// MagicDNS name and addresses, and the HTTPS certificate it can issue for
// that name - over the LocalAPI unix socket. Like the sshd WhoIs client it
// is a minimal internal client rather than a dependency on the
// tailscale.com/client/local module. As an Adapter, any failure - no
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

// Discover implements Adapter: the node's MagicDNS name from Self.
func (t *Tailscale) Discover(ctx context.Context) (Endpoint, error) {
	self, err := t.Self(ctx)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: self.DNSName, Label: "tailnet"}, nil
}

// Node is what the local tailscaled reports about this machine.
type Node struct {
	// DNSName is the MagicDNS name without its trailing dot.
	DNSName string
	// Addrs are the node's tailnet addresses, IPv4 and IPv6.
	Addrs []netip.Addr
	// CertDomains are the names tailscaled can issue HTTPS certificates
	// for; empty until HTTPS certificates are enabled for the tailnet.
	CertDomains []string
}

// Self reads GET /localapi/v0/status for this node.
func (t *Tailscale) Self(ctx context.Context) (Node, error) {
	body, err := t.get(ctx, "/localapi/v0/status")
	if err != nil {
		return Node{}, fmt.Errorf("status: %w", err)
	}
	var st struct {
		Self struct {
			DNSName      string
			TailscaleIPs []netip.Addr
		}
		CertDomains []string
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return Node{}, fmt.Errorf("status decode: %w", err)
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" {
		return Node{}, fmt.Errorf("status has no DNS name")
	}
	return Node{DNSName: name, Addrs: st.Self.TailscaleIPs, CertDomains: st.CertDomains}, nil
}

// Certificate fetches the node's HTTPS certificate and key for domain
// from GET /localapi/v0/cert/<domain>?type=pair. tailscaled issues the
// pair through Let's Encrypt on first use, which can take a minute, and
// renews it on later calls, so callers re-fetch periodically rather than
// caching for the certificate's lifetime. Only root or the tailscaled
// operator may fetch it.
func (t *Tailscale) Certificate(ctx context.Context, domain string) (tls.Certificate, error) {
	body, err := t.get(ctx, "/localapi/v0/cert/"+url.PathEscape(domain)+"?type=pair")
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cert %s: %w", domain, err)
	}
	// The pair is the certificate chain followed by the key, both PEM;
	// X509KeyPair finds each block by type.
	cert, err := tls.X509KeyPair(body, body)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cert %s: parse: %w", domain, err)
	}
	return cert, nil
}

// get performs one LocalAPI GET, reporting a non-200 answer with the
// body tailscaled sent, which names what the operator has to enable.
func (t *Tailscale) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
