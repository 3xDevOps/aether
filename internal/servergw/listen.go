package servergw

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/reachability"
)

const (
	// certFetchTimeout bounds one certificate fetch. The first fetch for a
	// name has tailscaled complete an ACME issuance, which takes well
	// under a minute when the tailnet has HTTPS certificates enabled.
	certFetchTimeout = 90 * time.Second
	// certRefresh is how often the certificate is fetched again.
	// tailscaled renews the certificate itself; the refresh is what picks
	// the renewal up, whether or not anyone opens the dashboard meanwhile.
	certRefresh = time.Hour
)

// Tailnet is where the gateway listens and as whom: the node's addresses
// and MagicDNS name, the port, and the tailscaled that issues the
// certificate for that name.
type Tailnet struct {
	Node  reachability.Node
	Port  int
	Certs *reachability.Tailscale
}

// Start fetches the node's HTTPS certificate, binds the port on every
// tailnet address that binds, and serves in the background. It refuses
// to start without the certificate, naming what the tailnet has to have
// enabled, or when no address binds at all; plain HTTP is never offered.
// The context bounds setup only; Close stops the gateway.
func (g *Gateway) Start(ctx context.Context, tn Tailnet) error {
	if len(tn.Node.Addrs) == 0 {
		return fmt.Errorf("servergw: tailscaled reports no tailnet address for %s", tn.Node.DNSName)
	}
	certs := &certCache{source: tn.Certs, domain: tn.Node.DNSName}
	fetchCtx, cancel := context.WithTimeout(ctx, certFetchTimeout)
	err := certs.fetch(fetchCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("servergw: HTTPS certificate for %s: %w; enable MagicDNS and HTTPS certificates for the tailnet in the Tailscale admin console (DNS page), or set web-port to 0", tn.Node.DNSName, err)
	}
	tlsCfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return certs.current(), nil },
	}
	// tailscaled reports the IPv6 address whether or not the kernel has
	// IPv6 enabled, so one address that will not bind is a warning; only
	// none binding is a refusal.
	var failed []error
	for _, addr := range tn.Node.Addrs {
		hostPort := netip.AddrPortFrom(addr, uint16(tn.Port)).String()
		ln, err := net.Listen("tcp", hostPort)
		if err != nil {
			slog.Warn("servergw: tailnet address not bound", "addr", hostPort, "error", err)
			failed = append(failed, err)
			continue
		}
		g.lns = append(g.lns, ln)
		g.core.Serve(tls.NewListener(ln, tlsCfg))
	}
	if len(g.lns) == 0 {
		return fmt.Errorf("servergw: listen: no tailnet address could be bound: %w", errors.Join(failed...))
	}
	go certs.run(g.ctx)
	return nil
}

// certCache holds the certificate handshakes are answered with. A
// handshake never waits on tailscaled: run fetches the pair again every
// certRefresh, and a refresh that fails keeps the cached pair and says so.
type certCache struct {
	source *reachability.Tailscale
	domain string

	mu   sync.Mutex
	cert *tls.Certificate
}

// fetch asks tailscaled for the pair and caches it.
func (c *certCache) fetch(ctx context.Context) error {
	cert, err := c.source.Certificate(ctx, c.domain)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cert = &cert
	c.mu.Unlock()
	return nil
}

// current returns the cached pair.
func (c *certCache) current() *tls.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cert
}

// run refreshes the pair every certRefresh until ctx ends.
func (c *certCache) run(ctx context.Context) {
	ticker := time.NewTicker(certRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetchCtx, cancel := context.WithTimeout(ctx, certFetchTimeout)
			err := c.fetch(fetchCtx)
			cancel()
			if err != nil {
				slog.Warn("servergw: HTTPS certificate refresh failed; serving the cached one", "domain", c.domain, "error", err)
			}
		}
	}
}
