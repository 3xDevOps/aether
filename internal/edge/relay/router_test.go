package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// selfSigned returns a certificate for host and a pool that trusts it.
func selfSigned(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// serveRouter runs Serve on a loopback listener and returns its address.
func serveRouter(t *testing.T, e *env) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.r.Serve(ln) }()
	t.Cleanup(func() {
		_ = ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return ln.Addr().String()
}

// echoTLS terminates TLS on c with cert and echoes one line.
func echoTLS(t *testing.T, c net.Conn, cert tls.Certificate, protos ...string) {
	conn := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: protos})
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Error(err)
		return
	}
	if _, err := conn.Write(buf); err != nil {
		t.Error(err)
	}
}

func dialTLS(t *testing.T, addr, host string, pool *x509.CertPool, protos ...string) (*tls.Conn, error) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	conn := tls.Client(raw, &tls.Config{ServerName: host, RootCAs: pool, NextProtos: protos})
	return conn, conn.Handshake()
}

func expectEcho(t *testing.T, conn *tls.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo %q: %v", buf, err)
	}
}

func TestRouterPassesThroughBySNI(t *testing.T) {
	e := newEnv(t)
	addr := serveRouter(t, e)
	a := claimedAgent(t, e)
	host := edgeproto.ServerHostname(a.id, testDomain)
	cert, pool := selfSigned(t, host)

	for _, protos := range [][]string{{"h2", "http/1.1"}, {"acme-tls/1"}} {
		t.Run(strings.Join(protos, ","), func(t *testing.T) {
			go func() { echoTLS(t, a.nextData(t), cert, protos...) }()
			// A handshake that completes end to end proves the relay
			// replayed the ClientHello byte for byte: the Finished
			// messages cover every handshake byte.
			conn, err := dialTLS(t, addr, strings.ToUpper(host), pool, protos...)
			if err != nil {
				t.Fatal(err)
			}
			if conn.ConnectionState().NegotiatedProtocol != protos[0] {
				t.Fatalf("negotiated %q", conn.ConnectionState().NegotiatedProtocol)
			}
			expectEcho(t, conn)
			o := next[edgeproto.Open](t, a)
			if o.Kind != edgeproto.KindWeb || o.Grant != "" || !strings.HasPrefix(o.ClientAddr, "127.0.0.1:") {
				t.Fatalf("open %+v", o)
			}
		})
	}
}

func TestRouterServesEdgeHostLocally(t *testing.T) {
	e := newEnv(t)
	addr := serveRouter(t, e)
	cert, pool := selfSigned(t, "localhost")
	remote := make(chan net.Addr, 1)
	go func() {
		c, err := e.r.Listener().Accept()
		if err != nil {
			t.Error(err)
			return
		}
		remote <- c.RemoteAddr()
		echoTLS(t, c, cert)
	}()
	conn, err := dialTLS(t, addr, "localhost", pool)
	if err != nil {
		t.Fatal(err)
	}
	expectEcho(t, conn)
	if got := <-remote; got.String() != conn.LocalAddr().String() {
		t.Fatalf("edge server sees %s, client is %s", got, conn.LocalAddr())
	}
}

func TestRouterClosesOtherConnections(t *testing.T) {
	e := newEnv(t)
	addr := serveRouter(t, e)
	unclaimed := enroll(t, e, newSigner(t))
	go unclaimed.run()

	for _, host := range []string{
		"evil.example.test",
		"localhost.evil.example.test",
		edgeproto.ServerHostname(edgeproto.ServerID(newSigner(t).PublicKey()), testDomain),
		edgeproto.ServerHostname(unclaimed.id, testDomain),
		"not-a-server-id." + testDomain,
	} {
		t.Run(host, func(t *testing.T) {
			if _, err := dialTLS(t, addr, host, nil); err == nil {
				t.Fatal("handshake succeeded")
			}
		})
	}
	m := e.r.Metrics()
	if m.Refusals["unknown host"] != 3 || m.Refusals[string(edgeproto.RefusalNotConnected)] != 2 {
		t.Fatalf("refusals %v", m.Refusals)
	}

	t.Run("not TLS", func(t *testing.T) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if n, err := c.Read(make([]byte, 64)); err == nil {
			t.Fatalf("read %d bytes from a non-TLS connection", n)
		}
	})
}

func TestPeekClientHelloIsBounded(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		// A handshake record header announcing more than the cap, then
		// junk up to it.
		_, _ = client.Write([]byte{0x16, 0x03, 0x01, 0x40, 0x00})
		_, _ = client.Write(make([]byte, edgeproto.MaxClientHelloSize))
		_ = client.Close()
	}()
	if _, _, err := peekClientHello(server); err == nil {
		t.Fatal("oversized ClientHello accepted")
	}
}
