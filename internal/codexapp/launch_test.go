//go:build linux

package codexapp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/codexapp"
)

// The test binary also plays the codex-app-server command and the fake
// Codex app-server it starts.
func TestMain(m *testing.M) {
	switch os.Getenv("CODEXAPP_TEST_ROLE") {
	case "launch":
		os.Exit(codexapp.Launch(os.Args[1:], os.Stderr))
	case "app-server":
		os.Exit(fakeAppServer(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeAppServer listens where `codex app-server --listen unix://PATH` would
// and answers each request with its method. A request for "big" first gets
// a message longer than the server's client reads.
func fakeAppServer(args []string) int {
	dir := os.Getenv("FAKE_DIR")
	_ = os.WriteFile(filepath.Join(dir, "server-args"), []byte(strings.Join(args, "\n")), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "server-pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	if os.Getenv("FAKE_APP_SERVER_FAIL") != "" {
		fmt.Fprintln(os.Stderr, "boom: no login")
		return 3
	}
	ln, err := net.Listen("unix", strings.TrimPrefix(args[2], "unix://"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &req)
			if req.Method == "big" {
				_ = c.Write(r.Context(), websocket.MessageText, bytes.Repeat([]byte("x"), 11<<20))
			}
			reply, _ := json.Marshal(map[string]any{"id": req.ID, "result": map[string]string{"method": req.Method}})
			_ = c.Write(r.Context(), websocket.MessageText, reply)
		}
	}))
	return 0
}

type fakeCodex struct {
	dir string
	env []string
}

// newFakeCodex puts a codex on PATH that reports version, runs this test
// binary as its app-server, and as its terminal records its arguments and
// waits for the stop file.
func newFakeCodex(t *testing.T, version string, env ...string) *fakeCodex {
	t.Helper()
	// Short, for the Unix socket path limit.
	dir, err := os.MkdirTemp("", "cx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "stop"), nil, 0o600)
		_ = os.RemoveAll(dir)
	})
	script := `#!/bin/sh
case "$1" in
--version) echo "codex-cli ` + version + `" ;;
app-server) CODEXAPP_TEST_ROLE=app-server exec "$FAKE_TEST_BINARY" "$@" ;;
*) printf '%s\n' "$@" > "$FAKE_DIR/tui-args"; while [ ! -e "$FAKE_DIR/stop" ]; do sleep 0.05; done ;;
esac
`
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeCodex{dir: dir, env: append(append(os.Environ(),
		"PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "TMPDIR="+dir,
		"FAKE_DIR="+dir, "FAKE_TEST_BINARY="+os.Args[0]), env...)}
}

func (f *fakeCodex) launch(t *testing.T, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(f.env, "CODEXAPP_TEST_ROLE=launch")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &stderr
}

func (f *fakeCodex) socket() string { return filepath.Join(f.dir, "aether-codex", "app-server.sock") }

func (f *fakeCodex) read(t *testing.T, name string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(filepath.Join(f.dir, name)); err == nil && len(data) > 0 {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLaunchAttachesTheTerminalToItsAppServer(t *testing.T) {
	codex := newFakeCodex(t, "0.164.0")
	cmd, stderr := codex.launch(t, "-c", "developer_instructions=be brief", "--", "codex", "--model", "m", "a task")
	if got, want := codex.read(t, "tui-args"), "--remote\nunix://"+codex.socket()+"\n--model\nm\na task\n"; got != want {
		t.Fatalf("terminal argv = %q, want %q", got, want)
	}
	if got, want := codex.read(t, "server-args"), "app-server\n--listen\nunix://"+codex.socket()+"\n-c\ndeveloper_instructions=be brief"; got != want {
		t.Fatalf("app-server argv = %q, want %q", got, want)
	}

	t.Setenv("TMPDIR", codex.dir)
	toBridge, input := io.Pipe()
	output, fromBridge := io.Pipe()
	bridged := make(chan error, 1)
	go func() { bridged <- codexapp.Bridge(t.Context(), toBridge, fromBridge) }()
	go func() {
		_, _ = io.WriteString(input, `{"id":1,"method":"big"}`+"\n"+`{"id":2,"method":"initialize"}`+"\n")
	}()
	lines := bufio.NewScanner(output)
	for _, want := range []string{`{"id":1,"result":{"method":"big"}}`, `{"id":2,"result":{"method":"initialize"}}`} {
		if !lines.Scan() || lines.Text() != want {
			t.Fatalf("bridged line = %.80q (%v), want %s: an unreadable message must be dropped whole", lines.Text(), lines.Err(), want)
		}
	}
	_ = input.Close()
	select {
	case err := <-bridged:
		if err != nil {
			t.Fatalf("Bridge after its input closed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge outlived its input")
	}

	pid, _ := strconv.Atoi(codex.read(t, "server-pid"))
	if err := os.WriteFile(filepath.Join(codex.dir, "stop"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("terminal exit: %v\n%s", err, stderr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the app-server outlived its terminal")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLaunchRefusesAnOldCodex(t *testing.T) {
	codex := newFakeCodex(t, "0.130.0")
	cmd, stderr := codex.launch(t, "--", "codex", "a task")
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want 1", err)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "codex-cli 0.130.0 is too old") || !strings.Contains(got, "Codex 0.131.0 or newer") || !strings.Contains(got, codexapp.UpdateCommand) {
		t.Fatalf("stderr = %q", got)
	}
	if _, err := os.Stat(filepath.Join(codex.dir, "server-args")); err == nil {
		t.Fatal("an app-server started for a Codex too old to attach to it")
	}
}

func TestLaunchShowsWhyTheAppServerExited(t *testing.T) {
	codex := newFakeCodex(t, "0.164.0", "FAKE_APP_SERVER_FAIL=1")
	cmd, stderr := codex.launch(t, "--", "codex", "a task")
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want 1", err)
	}
	want := "codex app-server exited before listening on " + codex.socket() + ": exit status 3. Its output:\nboom: no login\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(codex.dir, "tui-args")); err == nil {
		t.Fatal("the terminal started without an app-server")
	}
}

func TestBridgeWaitsForTheAppServer(t *testing.T) {
	codex := newFakeCodex(t, "0.164.0")
	t.Setenv("TMPDIR", codex.dir)
	toBridge, input := io.Pipe()
	output, fromBridge := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	go func() { _ = codexapp.Bridge(ctx, toBridge, fromBridge) }()
	time.Sleep(300 * time.Millisecond)
	codex.launch(t, "--", "codex")
	go func() { _, _ = io.WriteString(input, `{"id":7,"method":"initialize"}`+"\n") }()
	lines := bufio.NewScanner(output)
	if !lines.Scan() || lines.Text() != `{"id":7,"result":{"method":"initialize"}}` {
		t.Fatalf("bridged line = %q (%v)", lines.Text(), lines.Err())
	}
	_ = input.Close()
}
