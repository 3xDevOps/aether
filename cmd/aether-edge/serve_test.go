package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestMain makes the test binary aether-edge itself when
// AETHER_EDGE_TEST_MAIN is set, so a test can run the edge as a process
// of its own, as a container does.
func TestMain(m *testing.M) {
	if os.Getenv("AETHER_EDGE_TEST_MAIN") != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const fakeSecret = "fake-client-secret-for-tests"

// edgeProc is an aether-edge serve process in development mode.
type edgeProc struct {
	cmd     *exec.Cmd
	stderr  *lockedBuffer
	done    chan struct{}
	listen  string
	metrics string
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// edgeCommand runs aether-edge with only env for its environment, in dir,
// with HOME and TMPDIR also pointing at dir.
func edgeCommand(dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"AETHER_EDGE_TEST_MAIN=1", "HOME=" + dir, "TMPDIR=" + dir}, env...)
	return cmd
}

func startEdge(t *testing.T, dataDir, emptyDir, secretFile string) *edgeProc {
	t.Helper()
	return startEdgeCommand(t, serveCommand(dataDir, emptyDir, secretFile))
}

// serveCommand is aether-edge serve in development mode.
func serveCommand(dataDir, emptyDir, secretFile string) *exec.Cmd {
	return edgeCommand(emptyDir, []string{
		"AETHER_EDGE_DATA=" + dataDir,
		"AETHER_EDGE_DEV_LISTEN=127.0.0.1:0",
		"AETHER_EDGE_METRICS_LISTEN=127.0.0.1:0",
		"AETHER_EDGE_SIGNIN_ORIGIN=http://localhost:8080",
		"AETHER_EDGE_RELAY_ORIGIN=http://127.0.0.1:8080",
		"AETHER_EDGE_GITHUB_CLIENT_ID=fake-client-id",
		"AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=" + secretFile,
	}, "serve")
}

func startEdgeCommand(t *testing.T, cmd *exec.Cmd) *edgeProc {
	t.Helper()
	p := &edgeProc{cmd: cmd, stderr: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	listen := regexp.MustCompile(`development mode.* listen=(\S+)`)
	metrics := regexp.MustCompile(`aether-edge: serving .* metrics=(\S+)`)
	deadline := time.After(30 * time.Second)
	for {
		out := p.stderr.String()
		if m := metrics.FindStringSubmatch(out); m != nil {
			p.metrics = m[1]
			p.listen = listen.FindStringSubmatch(out)[1]
			return p
		}
		select {
		case <-p.done:
			t.Fatalf("aether-edge serve exited: %s\n%s", p.cmd.ProcessState, out)
		case <-deadline:
			t.Fatalf("aether-edge serve did not start:\n%s", out)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// fingerprint reads the edge key's fingerprint from PathEdgeInfo on the
// relay origin, as a server does.
func (p *edgeProc) fingerprint(t *testing.T) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+p.listen+edgeproto.PathEdgeInfo, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:8080"
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	var info edgeproto.EdgeInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Fingerprint == "" {
		t.Fatalf("GET %s: %s %v", edgeproto.PathEdgeInfo, resp.Status, err)
	}
	return info.Fingerprint
}

// stop sends SIGTERM, as a container runtime does, and wants exit status
// 0 within the shutdown timeout.
func (p *edgeProc) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatalf("aether-edge serve still runs %s after SIGTERM:\n%s", shutdownTimeout+5*time.Second, p.stderr.String())
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("aether-edge serve exited %d after SIGTERM:\n%s", code, p.stderr.String())
	}
}

func healthcheckProc(dir, metrics string) (string, error) {
	out, err := edgeCommand(dir, []string{"AETHER_EDGE_METRICS_LISTEN=" + metrics}, "healthcheck").CombinedOutput()
	return string(out), err
}

// TestServeAsAContainerRunsIt starts, checks, stops and replaces the edge
// as a container runtime does, with an empty working directory, HOME and
// TMPDIR that it must not write to.
func TestServeAsAContainerRunsIt(t *testing.T) {
	empty, volume := t.TempDir(), filepath.Join(t.TempDir(), "data")
	secretFile := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(secretFile, []byte(fakeSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(empty, 0o555); err != nil {
		t.Fatal(err)
	}

	first := startEdge(t, volume, empty, secretFile)
	fingerprint := first.fingerprint(t)
	if out, err := healthcheckProc(empty, first.metrics); err != nil {
		t.Fatalf("healthcheck of a running edge: %v\n%s", err, out)
	}
	resp, err := http.Get("http://" + first.metrics + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	keyPath := filepath.Join(volume, "edge_key")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("signing key: %v %v, want mode 0600", info, err)
	}
	first.stop(t)
	for what, text := range map[string]string{"the log": first.stderr.String(), "/metrics": string(metrics)} {
		if strings.Contains(text, fakeSecret) {
			t.Errorf("the client secret is in %s", what)
		}
	}
	out, err := healthcheckProc(empty, first.metrics)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, "aether-edge: healthcheck: no edge answers on ") {
		t.Fatalf("healthcheck of a stopped edge: %v\n%s", err, out)
	}

	// A replacement with the same data directory presents the same key,
	// and never rewrites it; one with an empty directory makes another.
	second := startEdge(t, volume, empty, secretFile)
	if got := second.fingerprint(t); got != fingerprint {
		t.Errorf("same data directory: fingerprint %s, want %s", got, fingerprint)
	}
	second.stop(t)
	if again, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(again, key) {
		t.Errorf("signing key file changed on the second start: %v", err)
	}
	fresh := startEdge(t, filepath.Join(t.TempDir(), "data"), empty, secretFile)
	if got := fresh.fingerprint(t); got == fingerprint {
		t.Errorf("new data directory kept the fingerprint %s", got)
	}
	fresh.stop(t)

	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Errorf("the edge wrote outside its data directory: %v %v", entries, err)
	}
}

func TestServeWithoutConfigurationSaysWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	out, err := edgeCommand(dir, []string{"AETHER_EDGE_DATA=" + filepath.Join(dir, "data")}, "serve").CombinedOutput()
	var exit *exec.ExitError
	want := `aether-edge: --signin-origin must be https://host[:port], not ""; set it or AETHER_EDGE_SIGNIN_ORIGIN` + "\n"
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || string(out) != want {
		t.Fatalf("serve with no configuration: %v\n%s", err, out)
	}
}

// TestServeHelpDescribesTheProxyOptions checks that the help of the proxy
// options matches both modes: a proxy on this host, and trusted proxies
// elsewhere.
func TestServeHelpDescribesTheProxyOptions(t *testing.T) {
	out, _ := edgeCommand(t.TempDir(), nil, "serve", "-h").CombinedOutput()
	help := strings.Join(strings.Fields(string(out)), " ")
	for _, want := range []string{
		"serve plain HTTP on this address instead of --listen, and read client addresses from the proxy's X-Forwarded-For header; " +
			"a loopback address unless --trusted-proxies names the proxies",
		"each proxy's own address, such as 10.0.0.5/32, or a network that holds only proxies, since every peer in them can set the client address.",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("aether-edge serve -h lacks %q:\n%s", want, out)
		}
	}
}
