package ompbroker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// execUnits runs a unit's command directly, with its environment and with
// the path it would bind at unitAgentDir in its place: no systemd, no
// sandbox.
type execUnits struct {
	mu    sync.Mutex
	procs map[string]*exec.Cmd
}

func (u *execUnits) Start(_ context.Context, name string, argv, props []string) error {
	var env []string
	agent := bound(props)
	for _, p := range props {
		if kv, ok := strings.CutPrefix(p, "Environment="); ok {
			env = append(env, strings.ReplaceAll(kv, unitAgentDir, agent))
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(env, "PATH=/usr/bin:/bin")
	if err := cmd.Start(); err != nil {
		return err
	}
	u.mu.Lock()
	u.procs[name] = cmd
	u.mu.Unlock()
	return nil
}

func (u *execUnits) Stop(_ context.Context, name string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if cmd := u.procs[name]; cmd != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		delete(u.procs, name)
	}
	return nil
}

// relay listens on a loopback port and relays to sock, as a session VM's
// helper does.
func relay(t *testing.T, sock string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				s, err := net.Dial("unix", sock)
				if err != nil {
					return
				}
				defer func() { _ = s.Close() }()
				go func() { _, _ = io.Copy(s, c) }()
				_, _ = io.Copy(c, s)
			}()
		}
	}()
	return ln.Addr().String()
}

// TestTheRealOMPGetsItsLoginFromTheBroker runs the omp release named by
// OMP_BROKER_E2E_BINARY as both the account's broker and the session's
// client: the client, in a home of its own with no login, signs in to a
// fake model API with the key the account's broker holds, and fails once
// the session loses the account.
func TestTheRealOMPGetsItsLoginFromTheBroker(t *testing.T) {
	binary := os.Getenv("OMP_BROKER_E2E_BINARY")
	if binary == "" {
		t.Skip("set OMP_BROKER_E2E_BINARY to an omp release binary")
	}
	dir := t.TempDir()
	units := &execUnits{procs: map[string]*exec.Cmd{}}
	t.Cleanup(func() { _ = units.Stop(context.Background(), unitName("ada")) })
	accountHome := filepath.Join(dir, "homes", "ada")
	if err := os.MkdirAll(filepath.Join(accountHome, ".omp", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{Binary: binary, Dir: filepath.Join(dir, "brokers"), DataDir: dir, Units: units, Bind: symlinkBind,
		Unbind: os.Remove, UID: os.Getuid(), GID: os.Getgid(), Ready: time.Minute})
	var mu sync.Mutex
	allowed, shared := true, false
	f := NewFront(m, filepath.Join(dir, "sessions"), func(context.Context, string) (Account, error) {
		mu.Lock()
		defer mu.Unlock()
		if !allowed {
			return Account{}, errors.New("account share revoked")
		}
		return Account{ID: "ada", Home: accountHome, Shared: shared, Prepare: ownUser}, nil
	})
	sock, err := f.Listen("run-e2e")
	if err != nil {
		t.Fatal(err)
	}
	addr := relay(t, sock)
	resp, err := http.Post("http://"+addr+"/v1/credential", "application/json",
		strings.NewReader(`{"provider":"anthropic","credential":{"type":"api_key","key":"fake-key-for-the-broker-test"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("store the account's login through the session: %s", resp.Status)
	}
	if _, err = os.Stat(filepath.Join(accountHome, ".omp", "agent", "agent.db")); err != nil {
		t.Fatalf("the account's login database: %v", err)
	}

	// From here the session runs on Ada's account as a share recipient,
	// which may read and refresh her logins but not change them.
	mu.Lock()
	shared = true
	mu.Unlock()
	var keys []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-api-key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"fake"}}`)
	}))
	t.Cleanup(model.Close)
	client := filepath.Join(dir, "session-home")
	for _, d := range []string{".local/share/omp", ".local/state/omp", ".cache/omp"} {
		if err = os.MkdirAll(filepath.Join(client, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runOMP := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-p", "--model=anthropic/claude-sonnet-4-5", "say hi")
		cmd.Env = []string{
			"HOME=" + client, "PATH=/usr/bin:/bin", "TERM=dumb",
			"XDG_DATA_HOME=" + filepath.Join(client, ".local/share"), "XDG_STATE_HOME=" + filepath.Join(client, ".local/state"),
			"XDG_CACHE_HOME=" + filepath.Join(client, ".cache"),
			"OMP_AUTH_BROKER_URL=http://" + addr, "OMP_AUTH_BROKER_TOKEN=aether-session", "OMP_AUTH_BROKER_SNAPSHOT_TTL_MS=0",
			"ANTHROPIC_BASE_URL=" + model.URL,
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		_ = cmd.Run()
		return out.String()
	}
	out := runOMP()
	mu.Lock()
	got := strings.Join(keys, ",")
	mu.Unlock()
	if !strings.Contains(got, "fake-key-for-the-broker-test") {
		t.Fatalf("the model saw keys %q; omp said:\n%s", got, out)
	}
	if _, err = os.Stat(filepath.Join(client, ".local/share/omp/agent.db")); err != nil {
		t.Errorf("the session's own omp database: %v", err)
	}
	if _, err = os.Stat(filepath.Join(client, ".omp/agent/agent.db")); !os.IsNotExist(err) {
		t.Errorf("omp opened ~/.omp/agent/agent.db in the session: %v", err)
	}

	mu.Lock()
	allowed, keys = false, nil
	mu.Unlock()
	out = runOMP()
	if !strings.Contains(out, "Auth broker") || len(keys) != 0 {
		t.Fatalf("after revocation omp still ran (model calls %d):\n%s", len(keys), out)
	}
	t.Logf("omp after revocation:\n%s", out)
}
