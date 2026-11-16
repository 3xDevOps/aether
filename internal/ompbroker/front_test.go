package ompbroker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUnits runs each "unit" as an in-process broker that answers with the
// login directory it serves and the token it was given.
type fakeUnits struct {
	mu      sync.Mutex
	servers map[string]*http.Server
	starts  atomic.Int32
}

func (u *fakeUnits) Start(_ context.Context, name string, argv, props []string) error {
	env := map[string]string{}
	for _, p := range props {
		if kv, ok := strings.CutPrefix(p, "Environment="); ok {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
	}
	bind := strings.TrimPrefix(argv[len(argv)-1], "--bind=")
	token, err := os.ReadFile(filepath.Join(env["HOME"], ".omp", "auth-broker.token"))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/healthz" {
			_, _ = io.WriteString(w, `{"ok":true,"version":"fake"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+string(token) {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/snapshot/stream" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		agent, _ := os.Readlink(bound(props))
		_, _ = fmt.Fprint(w, env["PI_CODING_AGENT_DIR"], " ", agent)
	})}
	go func() { _ = srv.Serve(ln) }()
	u.mu.Lock()
	u.servers[name] = srv
	u.mu.Unlock()
	u.starts.Add(1)
	return nil
}

// bound is the host path a unit binds at unitAgentDir.
func bound(props []string) string {
	for _, p := range props {
		if v, ok := strings.CutPrefix(p, "BindPaths="); ok {
			if src, ok := strings.CutSuffix(v, ":"+unitAgentDir); ok {
				return src
			}
		}
	}
	return ""
}

// ownUser stands in for the account's session user: the test's own.
func ownUser(context.Context) (int, int, error) { return os.Getuid(), os.Getgid(), nil }

// symlinkBind stands in for vm.BindBeneath, which needs root.
func symlinkBind(target, base, subpath string, _, _ int) error {
	_ = os.Remove(target)
	return os.Symlink(filepath.Join(base, subpath), target)
}

func (u *fakeUnits) Stop(_ context.Context, name string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if srv := u.servers[name]; srv != nil {
		_ = srv.Close()
		delete(u.servers, name)
	}
	return nil
}

func get(t *testing.T, sock, path string) (int, string) {
	t.Helper()
	return do(t, sock, http.MethodGet, path)
}

func do(t *testing.T, sock, method, path string) (int, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	req, _ := http.NewRequest(method, "http://127.0.0.1"+path, nil)
	req.Header.Set("Authorization", "Bearer session-placeholder")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestASessionReachesOnlyItsAccountsBrokerWhileItMay(t *testing.T) {
	dir := t.TempDir()
	units := &fakeUnits{servers: map[string]*http.Server{}}
	t.Cleanup(func() {
		for name := range units.servers {
			_ = units.Stop(context.Background(), name)
		}
	})
	binary := filepath.Join(dir, "omp")
	if err := os.WriteFile(binary, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	var unbound []string
	m := NewManager(Config{Binary: binary, Dir: filepath.Join(dir, "accounts"), DataDir: dir, Units: units, Bind: symlinkBind,
		Unbind: func(target string) error { unbound = append(unbound, target); return os.Remove(target) },
		UID:    os.Getuid(), GID: os.Getgid(), Ready: 5 * time.Second})
	var mu sync.Mutex
	accounts := map[string]string{"run-a": "ada", "run-b": "grace"}
	f := NewFront(m, filepath.Join(dir, "sessions"), func(_ context.Context, session string) (Account, error) {
		mu.Lock()
		defer mu.Unlock()
		id, ok := accounts[session]
		if !ok {
			return Account{}, errors.New("account share revoked")
		}
		return Account{ID: id, Home: filepath.Join(dir, "homes", id), Shared: session == "run-b", Prepare: ownUser}, nil
	})
	f.recheck = 50 * time.Millisecond
	a, err := f.Listen("run-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Listen("run-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ada", "grace"} {
		if err = os.MkdirAll(filepath.Join(dir, "homes", id, ".omp", "agent"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if code, body := get(t, a, "/v1/snapshot"); code != http.StatusOK || body != "/run/aether-omp/agent "+filepath.Join(dir, "homes/ada/.omp/agent") {
		t.Fatalf("run-a got %d %q", code, body)
	}
	if code, body := get(t, b, "/v1/snapshot"); code != http.StatusOK || body != "/run/aether-omp/agent "+filepath.Join(dir, "homes/grace/.omp/agent") {
		t.Fatalf("run-b got %d %q", code, body)
	}
	if code, _ := get(t, a, "/v1/snapshot"); code != http.StatusOK || units.starts.Load() != 2 {
		t.Fatalf("a second request started another broker: %d starts", units.starts.Load())
	}
	if code, _ := get(t, a, "/metrics"); code != http.StatusNotFound {
		t.Fatalf("a path outside /v1 got %d", code)
	}
	_ = units.Stop(context.Background(), unitName("ada"))
	if code, _ := get(t, a, "/v1/snapshot"); code != http.StatusBadGateway {
		t.Fatalf("a request to a broker that died got %d", code)
	}
	if code, _ := get(t, a, "/v1/snapshot"); code != http.StatusOK || units.starts.Load() != 3 {
		t.Fatalf("the next request got %d after %d starts; want the broker started again", code, units.starts.Load())
	}
	m.forget("ada")
	if code, _ := get(t, a, "/v1/snapshot"); code != http.StatusOK || units.starts.Load() != 3 {
		t.Fatalf("a broker that still answers was started again: %d, %d starts", code, units.starts.Load())
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/snapshot", http.StatusOK},
		{http.MethodPost, "/v1/credential/7/refresh", http.StatusOK},
		{http.MethodPost, "/v1/usage/observed", http.StatusOK},
		{http.MethodPost, "/v1/credential", http.StatusForbidden},
		{http.MethodPost, "/v1/credential/7/disable", http.StatusForbidden},
		{http.MethodPost, "/v1/credential/7/block", http.StatusForbidden},
		{http.MethodPost, "/v1/credential/7/x/refresh", http.StatusForbidden},
		{http.MethodPost, "/v1/credential/../refresh", http.StatusForbidden},
		{http.MethodPost, "/v1/credential/%2e%2e/refresh", http.StatusForbidden},
	} {
		if code, _ := do(t, b, c.method, c.path); code != c.want {
			t.Errorf("a shared session's %s %s got %d, want %d", c.method, c.path, code, c.want)
		}
	}
	if code, _ := do(t, a, http.MethodPost, "/v1/credential"); code != http.StatusOK {
		t.Errorf("the owner's session storing a login got %d", code)
	}

	conn, err := net.Dial("unix", a)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprint(conn, "GET /v1/snapshot/stream HTTP/1.1\r\nHost: x\r\n\r\n")
	r := bufio.NewReader(conn)
	if status, err := r.ReadString('\n'); err != nil || !strings.Contains(status, "200") {
		t.Fatalf("stream: %q, %v", status, err)
	}
	mu.Lock()
	delete(accounts, "run-a")
	mu.Unlock()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("the stream stayed open after the share was revoked: %v", err)
	}
	if code, body := get(t, a, "/v1/snapshot"); code != http.StatusForbidden || !strings.Contains(body, "account share revoked") {
		t.Fatalf("a revoked session got %d %q", code, body)
	}

	if err := m.Stop(context.Background(), "grace"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "accounts", "grace")); !os.IsNotExist(err) || len(unbound) != 1 || units.servers[unitName("grace")] != nil {
		t.Errorf("a stopped account's broker: home %v, unbound %v, unit %v", err, unbound, units.servers[unitName("grace")])
	}
	f.Close("run-b")
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("a closed session's socket: %v", err)
	}
	if _, err := f.Listen("../escape"); err == nil {
		t.Fatal("a session name with a path was accepted")
	}
}

func TestABrokerNeverRunsAsRoot(t *testing.T) {
	m := NewManager(Config{Dir: t.TempDir()})
	if _, err := m.ensure(context.Background(), Account{ID: "ada", Home: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "refusing to run as root") {
		t.Fatalf("a broker with no user of its own = %v", err)
	}
}
