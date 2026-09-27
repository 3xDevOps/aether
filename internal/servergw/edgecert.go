package servergw

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const (
	// certMinBackoff is the first wait after a failed issuance. autocert
	// answers every attempt within a minute of a failure with that
	// failure, so retrying sooner asks the CA nothing.
	certMinBackoff = time.Minute
	certMaxBackoff = time.Hour
)

// edgeCerts answers handshakes for exactly one hostname, with a
// certificate issued through TLS-ALPN-01 over the edge's passthrough.
type edgeCerts struct {
	host  string
	fixed *tls.Certificate
	acme  *autocert.Manager
	// ready is set once issuance succeeded. Until then an ordinary
	// handshake is refused instead of starting an order of its own, so
	// browsers retrying during an outage cannot spend the CA's rate limit.
	ready atomic.Bool
}

func newEdgeCerts(host, dir, directory string, fixed *tls.Certificate) *edgeCerts {
	c := &edgeCerts{host: host, fixed: fixed}
	if fixed == nil {
		c.acme = &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			// DirCache creates the directory with mode 0700.
			Cache:      autocert.DirCache(dir),
			HostPolicy: autocert.HostWhitelist(host),
			Client:     &acme.Client{DirectoryURL: directory},
		}
	}
	return c
}

func (c *edgeCerts) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if !strings.EqualFold(strings.TrimSuffix(hello.ServerName, "."), c.host) {
		return nil, fmt.Errorf("servergw: no certificate for server name %q; this server answers %s", hello.ServerName, c.host)
	}
	if c.fixed != nil {
		return c.fixed, nil
	}
	if len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto {
		return c.acme.GetCertificate(hello)
	}
	if !c.ready.Load() {
		return nil, fmt.Errorf("servergw: the certificate for %s has not been issued yet", c.host)
	}
	return c.acme.GetCertificate(c.hello())
}

// hello asks autocert for the ECDSA certificate. autocert keys
// certificates by the key type a ClientHello supports, so passing the
// browser's own hello could start a second, RSA, order.
func (c *edgeCerts) hello() *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName:       c.host,
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
}

// issue obtains the certificate, from the cache or the CA, retrying with
// backoff until it succeeds or ctx ends. autocert renews it from then on.
func (c *edgeCerts) issue(ctx context.Context) {
	backoff := certMinBackoff
	for {
		_, err := c.acme.GetCertificate(c.hello())
		if err == nil {
			c.ready.Store(true)
			slog.Info("servergw: dashboard certificate for the edge ready", "host", c.host)
			return
		}
		slog.Warn("servergw: dashboard certificate for the edge not issued; the dashboard over the edge is unavailable until it is, SSH is not affected",
			"host", c.host, "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, certMaxBackoff)
	}
}
