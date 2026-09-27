package servergw

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func selfSigned(t *testing.T, host string) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// handshake dials the gateway's listener with serverName as SNI.
func handshake(t *testing.T, addr, serverName string, roots *x509.CertPool) error {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: roots, InsecureSkipVerify: roots == nil}).Handshake()
}

func TestEdgeServesTLSForItsHostnameOnly(t *testing.T) {
	host := "aaaaaaaaaaaaaaaaaaaaaaaaaa." + testDomain
	cert, roots := selfSigned(t, host)
	env := newEdgeEnv(t, EdgeConfig{Certificate: cert})
	env.edge.Start()
	addr := env.agent.ln.Addr().String()

	if env.edge.Host() != host {
		t.Fatalf("host %q, want %q", env.edge.Host(), host)
	}
	if err := handshake(t, addr, "other."+testDomain, nil); err == nil {
		t.Fatal("handshake for another server name succeeded")
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{ServerName: host, RootCAs: roots},
	}}
	resp, err := client.Get("https://" + host + "/api/v1/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("capabilities over TLS without a session: %d %v, want 401 with the security headers", resp.StatusCode, resp.Header)
	}
}

// A CA that cannot issue leaves the gateway accepting connections and
// refusing their handshakes, and never ends it. Issuance starts on Start,
// before any browser connects.
func TestEdgeCertificateFailureKeepsServing(t *testing.T) {
	var asked atomic.Int32
	ca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		http.Error(w, "ACME directory unavailable", http.StatusInternalServerError)
	}))
	defer ca.Close()
	certDir := filepath.Join(t.TempDir(), "edge", "certs")
	env := newEdgeEnv(t, EdgeConfig{ACMEDirectory: ca.URL, CertDir: certDir})
	env.edge.Start()

	deadline := time.Now().Add(10 * time.Second)
	for asked.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("issuance did not start when the listener started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(certDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("certificate cache mode %v, want 0700", info.Mode().Perm())
	}
	addr := env.agent.ln.Addr().String()
	for range 2 {
		err := handshake(t, addr, env.edge.Host(), nil)
		var netErr net.Error
		if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
			t.Fatalf("handshake without a certificate: %v, want a TLS alert", err)
		}
	}
	select {
	case <-env.edge.core.Done():
		t.Fatalf("the gateway stopped serving: %v", env.edge.core.Err())
	default:
	}
	if status, _ := env.call(env.origin(), nil); status != http.StatusUnauthorized {
		t.Fatalf("gateway after a certificate failure: %d, want it answering", status)
	}
}
