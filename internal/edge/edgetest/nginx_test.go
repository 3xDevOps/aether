//go:build nginx

package edgetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// nginxIdle is how long the test holds a relayed connection silent: past
// nginx's default proxy_read_timeout of 60 seconds, which the example
// configuration raises.
const nginxIdle = 75 * time.Second

// TestBehindNginx runs the edge the way `aether-edge serve --proxy-listen`
// does, behind a real nginx configured from
// packaging/nginx/aether-edge.conf.example, with a self-signed
// certificate for the two host names localhost (sign-in) and 127.0.0.1
// (relay). A real server enrolls through nginx, a client signs in,
// claims, runs a control method, and holds a relayed connection silent
// past nginx's default timeout. A forged X-Forwarded-For does not change
// the address the edge records.
func TestBehindNginx(t *testing.T) {
	nginx, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx is not installed: " + err.Error())
	}
	dir := t.TempDir()
	port := freePort(t)
	signinURL, relayURL := "https://localhost:"+port, "https://127.0.0.1:"+port
	writeCertificates(t, dir, "localhost", "127.0.0.1")
	// The system roots are read once, at the first TLS handshake.
	t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "live", "localhost", "fullchain.pem"))

	h := &harness{t: t, github: newFakeGitHub(t), edgeDir: t.TempDir(), signinURL: signinURL, relayURL: relayURL}
	svc, err := edge.New(edge.Config{DataDir: h.edgeDir, SigninOrigin: signinURL, RelayOrigin: relayURL, GitHub: h.github.app(), Clock: h.now})
	if err != nil {
		t.Fatal(err)
	}
	rl, err := relay.New(context.Background(), svc.RelayConfig(0))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLink(rl)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	back := &http.Server{Handler: rl.Forwarded(nil, svc.Handler()), ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = back.Serve(upstream) }()
	h.node = &edgeNode{svc: svc, relay: rl, back: back}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		_ = rl.Shutdown(ctx)
		_ = back.Close()
		_ = svc.Close()
	})

	conf := renderNginx(t, dir, port, upstream.Addr().String())
	out, err := exec.Command(nginx, "-t", "-p", dir, "-c", conf, "-e", "stderr").CombinedOutput()
	t.Logf("nginx -t:\n%s", out)
	if err != nil {
		t.Fatalf("nginx -t: %v", err)
	}
	cmd := exec.Command(nginx, "-p", dir, "-c", conf, "-e", "stderr", "-g", "daemon off;")
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGQUIT)
		_ = cmd.Wait()
	})
	eventually(t, "nginx serving the relay host", func() error {
		resp, gerr := http.Get(relayURL + "/healthz")
		if gerr != nil {
			return gerr
		}
		resp.Body.Close() //nolint:errcheck,gosec // probe
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s/healthz: %s", relayURL, resp.Status)
		}
		return nil
	})

	// A request that reaches the edge's loopback listener without the
	// header nginx sets is refused.
	resp, err := http.Get("http://" + upstream.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck,gosec // read in full
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "sent no X-Forwarded-For header") {
		t.Fatalf("request to the edge without the proxy: %s %s", resp.Status, body)
	}

	s := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, s)
	ctl := h.control(al, s)
	if info := call[protocol.ServerInfoResult](t, ctl, protocol.MethodServerInfo, struct{}{}); info.Member.Role != "admin" {
		t.Fatalf("server.info through nginx = %+v", info.Member)
	}
	t.Logf("holding the relayed control channel silent for %s", nginxIdle)
	time.Sleep(nginxIdle)
	if info := call[protocol.ServerInfoResult](t, ctl, protocol.MethodServerInfo, struct{}{}); info.Member.Role != "admin" {
		t.Fatalf("server.info after %s of silence = %+v", nginxIdle, info.Member)
	}

	// A device sign-in started with a forged X-Forwarded-For: the edge
	// records the address nginx saw.
	signer := newSigner(t)
	start, err := json.Marshal(edgeproto.DeviceStartRequest{Label: "forger", Key: edgeproto.DeviceKeyLine(signer.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, signinURL+edgeproto.PathDeviceStart, strings.NewReader(string(start)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var started edgeproto.DeviceStartResponse
	err = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close() //nolint:errcheck,gosec // decoded
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("device start: %s %v", resp.Status, err)
	}
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	b.client.Transport = forgedFor{"198.51.100.7"}
	r, page, err := b.post("/device", url.Values{"user_code": {started.UserCode}})
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("enter the code: %v %v\n%s", err, r, page)
	}
	if !strings.Contains(page, "<code>127.0.0.1</code> (the address of this browser)") ||
		strings.Contains(page, "203.0.113.9") || strings.Contains(page, "198.51.100.7") {
		t.Fatalf("the confirmation page shows another address than the peer nginx saw:\n%s", page)
	}
	t.Log("the edge recorded 127.0.0.1 for requests that sent X-Forwarded-For 203.0.113.9 and 198.51.100.7")
}

// forgedFor adds a client-supplied X-Forwarded-For to every request.
type forgedFor struct{ addr string }

func (f forgedFor) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forwarded-For", f.addr)
	return http.DefaultTransport.RoundTrip(r)
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	return port
}

// writeCertificates writes one self-signed certificate for both host
// names where certbot keeps each host's: live/<host>/fullchain.pem and
// privkey.pem under dir.
func writeCertificates(t *testing.T, dir, signinHost, relayHost string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aether edge test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{signinHost},
		IPAddresses:           []net.IP{net.ParseIP(relayHost)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{signinHost, relayHost} {
		live := filepath.Join(dir, "live", host)
		if err := os.MkdirAll(live, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, block := range map[string]*pem.Block{
			"fullchain.pem": {Type: "CERTIFICATE", Bytes: der},
			"privkey.pem":   {Type: "PRIVATE KEY", Bytes: keyDER},
		} {
			if err := os.WriteFile(filepath.Join(live, name), pem.EncodeToMemory(block), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// renderNginx writes the example configuration with its placeholders
// filled in, inside a main nginx.conf that keeps every file nginx writes
// under dir, and returns the main file's path.
func renderNginx(t *testing.T, dir, port, upstream string) string {
	t.Helper()
	example, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "nginx", "aether-edge.conf.example"))
	if err != nil {
		t.Fatal(err)
	}
	site := strings.NewReplacer(
		"<signin-host>", "localhost",
		"<relay-host>", "127.0.0.1",
		"/etc/letsencrypt/live/", filepath.Join(dir, "live")+"/",
		"listen 443 ssl;", "listen 127.0.0.1:"+port+" ssl;",
		"listen [::]:443 ssl;", "",
		"127.0.0.1:8443", upstream,
	).Replace(string(example))
	if err := os.WriteFile(filepath.Join(dir, "aether-edge.conf"), []byte(site), 0o600); err != nil {
		t.Fatal(err)
	}
	user := ""
	if os.Geteuid() == 0 {
		// Workers read the temporary directories below, which are the
		// test's own.
		user = "user root;\n"
	}
	main := user + `worker_processes 1;
pid ` + filepath.Join(dir, "nginx.pid") + `;
events {}
http {
    access_log off;
    client_body_temp_path ` + filepath.Join(dir, "body") + `;
    proxy_temp_path ` + filepath.Join(dir, "proxy") + `;
    fastcgi_temp_path ` + filepath.Join(dir, "fastcgi") + `;
    uwsgi_temp_path ` + filepath.Join(dir, "uwsgi") + `;
    scgi_temp_path ` + filepath.Join(dir, "scgi") + `;
    include ` + filepath.Join(dir, "aether-edge.conf") + `;
}
`
	path := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
