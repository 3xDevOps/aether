package servergw

import (
	"context"
	"crypto/tls"
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
	// certRefresh is how old a cached certificate may be before the next
	// handshake asks tailscaled for it again. tailscaled renews the
	// certificate itself; the refresh is what picks the renewal up.
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

type listener = net.Listener

// Start fetches the node's HTTPS certificate, binds the port on every
// tailnet address, and serves in the background. It refuses to start
// without the certificate, naming what the tailnet has to have enabled;
// plain HTTP is never offered. The context bounds setup only; Close
// stops the gateway.
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
	for _, addr := range tn.Node.Addrs {
		ln, err := net.Listen("tcp", netip.AddrPortFrom(addr, uint16(tn.Port)).String())
		if err != nil {
			for _, opened := range g.lns {
				_ = opened.Close()
			}
			g.lns = nil
			return fmt.Errorf("servergw: listen: %w", err)
		}
		g.lns = append(g.lns, ln)
		go func() { _ = g.srv.Serve(tls.NewListener(ln, tlsCfg)) }()
	}
	return nil
}

// certCache holds the certificate handshakes are answered with. A
// handshake never waits on tailscaled: once the cache is stale the next
// one hands out the cached pair and refreshes in the background, and a
// refresh that fails keeps the cached pair and says so.
type certCache struct {
	source *reachability.Tailscale
	domain string

	mu         sync.Mutex
	cert       *tls.Certificate
	fetched    time.Time
	refreshing bool
}

// fetch asks tailscaled for the pair and caches it.
func (c *certCache) fetch(ctx context.Context) error {
	cert, err := c.source.Certificate(ctx, c.domain)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cert = &cert
	c.fetched = time.Now()
	c.mu.Unlock()
	return nil
}

// current returns the cached pair, starting a refresh when it is stale.
func (c *certCache) current() *tls.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetched) > certRefresh && !c.refreshing {
		c.refreshing = true
		go c.refresh()
	}
	return c.cert
}

func (c *certCache) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), certFetchTimeout)
	defer cancel()
	err := c.fetch(ctx)
	if err != nil {
		slog.Warn("servergw: HTTPS certificate refresh failed; serving the cached one", "domain", c.domain, "error", err)
	}
	c.mu.Lock()
	c.refreshing = false
	if err != nil {
		// Try again on the next handshake after another interval rather
		// than on every handshake until tailscaled recovers.
		c.fetched = time.Now()
	}
	c.mu.Unlock()
}
