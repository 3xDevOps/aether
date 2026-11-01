package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/coordtransport"
)

// socketPath is where a run's app-server listens. Codex links it to its own
// socket under /tmp/codex-daemon-<uid>.
func socketPath() string {
	return filepath.Join(os.TempDir(), "aether-codex", "app-server.sock")
}

// UpdateCommand updates Codex in the member's home, as its install did.
const UpdateCommand = `npm install -g --prefix "$HOME/.local" @openai/codex@latest`

// minVersion is the first Codex whose terminal attaches to an app-server
// over a Unix socket.
var minVersion = [3]int{0, 131, 0}

const (
	listenWait = 30 * time.Second
	// bridgeWait covers a cold app-server start, since the server starts
	// the bridge as soon as the terminal starts.
	bridgeWait = time.Minute
	// maxMessage is the longest line the server's JSON-RPC client reads.
	maxMessage = 10 << 20
)

// LaunchCommand runs tui, a Codex terminal command, attached to an app-server
// started with serverArgs.
func LaunchCommand(serverArgs, tui []string) []string {
	argv := append([]string{coordtransport.BinaryPath, "codex-app-server"}, serverArgs...)
	return append(append(argv, "--"), tui...)
}

// BridgeCommand connects its stdin and stdout to the run's app-server.
var BridgeCommand = []string{coordtransport.BinaryPath, "codex-bridge"}

// Launch is the codex-app-server command: args are app-server arguments, a
// "--", then the terminal's argv. It starts the app-server, waits for it to
// listen, and replaces itself with the terminal attached to it. The
// app-server ends with the terminal.
func Launch(args []string, stderr io.Writer) int {
	code, err := launch(args)
	_, _ = fmt.Fprintln(stderr, err)
	return code
}

// launch returns only on failure.
func launch(args []string) (int, error) {
	split := slices.Index(args, "--")
	if split < 0 || split == len(args)-1 {
		return 2, errors.New("usage: aether-server codex-app-server [app-server-argument ...] -- codex [argument ...]")
	}
	serverArgs, tui := args[:split], args[split+1:]
	exe, err := exec.LookPath(tui[0])
	if err != nil {
		return 127, err
	}
	if err = checkVersion(exe); err != nil {
		return 1, err
	}
	sock := socketPath()
	logPath := filepath.Join(filepath.Dir(sock), "app-server.log")
	if err = os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return 1, fmt.Errorf("codex app-server: %w", err)
	}
	if err = os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 1, fmt.Errorf("codex app-server: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return 1, fmt.Errorf("codex app-server: %w", err)
	}
	// The parent-death signal fires when the forking thread ends, and this
	// thread becomes the terminal's: the app-server lives exactly as long as
	// the terminal.
	runtime.LockOSThread()
	server := exec.Command(exe, append([]string{"app-server", "--listen", "unix://" + sock}, serverArgs...)...)
	server.Stdout, server.Stderr = logFile, logFile
	server.SysProcAttr = serverAttr()
	err = server.Start()
	_ = logFile.Close()
	if err != nil {
		return 1, fmt.Errorf("codex app-server: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- server.Wait() }()
	if err = awaitListen(sock, exited); err != nil {
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
		output, _ := os.ReadFile(logPath)
		return 1, fmt.Errorf("codex app-server %w. Its output:\n%s", err, tail(output))
	}
	argv := append([]string{tui[0], "--remote", "unix://" + sock}, tui[1:]...)
	err = syscall.Exec(exe, argv, os.Environ())
	_ = syscall.Kill(-server.Process.Pid, syscall.SIGTERM)
	return 1, fmt.Errorf("start %s: %w", tui[0], err)
}

// checkVersion refuses a Codex older than minVersion. An unrecognized
// version string passes: the attach then fails with Codex's own error.
func checkVersion(exe string) error {
	out, err := exec.Command(exe, "--version").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(exit.Stderr))
		}
		return fmt.Errorf("%s --version: %w", exe, err)
	}
	installed := strings.TrimSpace(string(out))
	fields := strings.Fields(installed)
	if len(fields) == 0 {
		return nil
	}
	parts := strings.SplitN(strings.SplitN(fields[len(fields)-1], "-", 2)[0], ".", 3)
	if len(parts) != 3 {
		return nil
	}
	var got [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		got[i] = n
	}
	for i := range got {
		if got[i] > minVersion[i] {
			return nil
		}
		if got[i] < minVersion[i] {
			return fmt.Errorf("%s is too old: Aether attaches Codex's terminal to its app-server, which needs Codex %d.%d.%d or newer.\nUpdate it here, then start a new session:\n  %s",
				installed, minVersion[0], minVersion[1], minVersion[2], UpdateCommand)
		}
	}
	return nil
}

func awaitListen(sock string, exited <-chan error) error {
	deadline := time.Now().Add(listenWait)
	for {
		if conn, err := net.Dial("unix", sock); err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-exited:
			if err == nil {
				return fmt.Errorf("exited before listening on %s", sock)
			}
			return fmt.Errorf("exited before listening on %s: %w", sock, err)
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("did not listen on %s within %s", sock, listenWait)
		}
	}
}

// tail keeps the end of the app-server's log, which carries its error.
func tail(output []byte) []byte {
	const max = 4 << 10
	if len(output) > max {
		output = output[len(output)-max:]
	}
	return bytes.TrimRight(output, "\n")
}

// Bridge is the codex-bridge command: it relays JSON-RPC between lines on
// in and out and the app-server's WebSocket, until either side closes. It
// waits for the app-server to listen.
func Bridge(ctx context.Context, in io.Reader, out io.Writer) error {
	sock := socketPath()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	var conn *websocket.Conn
	deadline := time.Now().Add(bridgeWait)
	for {
		var err error
		conn, _, err = websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no Codex app-server at %s after %s: %w", sock, bridgeWait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	conn.SetReadLimit(-1)
	defer func() { _ = conn.CloseNow() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		lines := bufio.NewScanner(in)
		lines.Buffer(make([]byte, 64<<10), maxMessage)
		for lines.Scan() {
			if err := conn.Write(ctx, websocket.MessageText, lines.Bytes()); err != nil {
				return
			}
		}
	}()
	for {
		_, r, err := conn.Reader(ctx)
		if err != nil {
			// The app-server ends with the terminal.
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("codex app-server: %w", err)
		}
		message, err := io.ReadAll(io.LimitReader(r, maxMessage))
		if err != nil {
			return fmt.Errorf("codex app-server: %w", err)
		}
		// A message the server's client could not read is dropped whole.
		if n, _ := io.Copy(io.Discard, r); n > 0 {
			continue
		}
		if _, err := out.Write(append(message, '\n')); err != nil {
			return err
		}
	}
}
