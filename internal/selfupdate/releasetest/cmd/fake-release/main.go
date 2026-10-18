// Command fake-release serves a signed fake GitHub release to a real
// aether.exe on a Windows runner (scripts/update-smoke.ps1). The client
// pins https://github.com, so this is an HTTPS proxy: the client sends
// CONNECT github.com:443 (HTTPS_PROXY) and gets a certificate for
// github.com signed by a throwaway CA, written to -ca-out for the caller
// to trust. The client must be built to trust releasetest.PublicKey.
//
//	fake-release -assets <dir> -latest v0.5.1 -ca-out ca.pem -pubkey
//
// It prints the proxy's address on one line. GET /slow?d=5s on the proxy
// port itself delays every aether-windows-amd64.exe download by d.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
)

const host = "github.com"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fake-release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("fake-release", flag.ContinueOnError)
	assetDir := fs.String("assets", "", "directory whose files every release tag publishes")
	latest := fs.String("latest", "", "tag /releases/latest redirects to")
	caOut := fs.String("ca-out", "", "file to write the CA certificate (PEM) to")
	listen := fs.String("listen", "127.0.0.1:0", "proxy address")
	pubkey := fs.Bool("pubkey", false, "print the base64 release public key and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubkey {
		fmt.Println(base64.StdEncoding.EncodeToString(releasetest.PublicKey()))
		return nil
	}
	if *assetDir == "" || *latest == "" || *caOut == "" {
		return errors.New("usage: fake-release -assets <dir> -latest <tag> -ca-out <file> [-listen addr] | -pubkey")
	}
	assets, err := readAssets(*assetDir)
	if err != nil {
		return err
	}
	leaf, caPEM, err := certificates()
	if err != nil {
		return err
	}
	if err = os.WriteFile(*caOut, caPEM, 0o644); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	p := &proxy{
		release: releasetest.Handler("https://"+host+"/"+selfupdate.Repo, *latest, assets),
		tls:     &tls.Config{Certificates: []tls.Certificate{leaf}, NextProtos: []string{"http/1.1"}},
	}
	fmt.Println(ln.Addr())
	return (&http.Server{Handler: p}).Serve(ln)
}

func readAssets(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	assets := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if assets[e.Name()], err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
			return nil, err
		}
	}
	return assets, nil
}

// certificates makes a CA and a github.com certificate it signs.
func certificates() (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Aether update smoke CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

type proxy struct {
	release http.Handler
	tls     *tls.Config
	slow    atomic.Int64
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r)
	case r.URL.Path == "/slow":
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.slow.Store(int64(d))
	default:
		http.NotFound(w, r)
	}
}

// connect answers CONNECT github.com:443 and serves the release over TLS on
// the hijacked connection.
func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	if r.Host != host+":443" {
		http.Error(w, "this proxy only knows "+host, http.StatusForbidden)
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = conn.Close()
		return
	}
	ln := &oneConn{conns: make(chan net.Conn, 1)}
	ln.conns <- tls.Server(conn, p.tls)
	_ = (&http.Server{Handler: http.HandlerFunc(p.serveRelease)}).Serve(ln)
}

func (p *proxy) serveRelease(w http.ResponseWriter, r *http.Request) {
	if d := time.Duration(p.slow.Load()); d > 0 && strings.HasSuffix(r.URL.Path, "/aether-windows-amd64.exe") {
		time.Sleep(d)
	}
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+selfupdate.Repo)
	p.release.ServeHTTP(w, r)
}

// oneConn is a listener that yields its connection once, then blocks until
// the connection ends.
type oneConn struct{ conns chan net.Conn }

type closeNotifier struct {
	net.Conn
	done chan struct{}
}

func (c *closeNotifier) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.Conn.Close()
}

func (l *oneConn) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	n := &closeNotifier{Conn: c, done: make(chan struct{})}
	go func() { <-n.done; close(l.conns) }()
	return n, nil
}

func (l *oneConn) Close() error   { return nil }
func (l *oneConn) Addr() net.Addr { return &net.TCPAddr{} }
