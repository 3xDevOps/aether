//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationMasterTerminal drives the member terminal end to end over
// the wired server against real Docker: first open starts the container,
// the shell answers in the member's home, a second tab shares the
// container, the session survives a detach and a server restart with the
// container re-adopted, and terminal.stop tears it down. Plan verification
// item 3.
func TestIntegrationMasterTerminal(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Skip("the master terminal needs a real shell in the container; Docker daemon unreachable")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	newServer := func() *Server {
		srv, err := New(ctx, Config{
			DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: rt,
			StandardImage: image,
		})
		if err != nil {
			t.Fatalf("server.New: %v", err)
		}
		return srv
	}
	srv := newServer()

	_, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Terminal Owner",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleAdmin,
	}
	if err := srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)
	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)

	// First open starts the container; the shell runs in the member home.
	main := openTerminal(t, client, "main")
	main.stdin.Write([]byte("echo home=$HOME\n"))
	main.waitOutput(t, "home=/root")

	// A second tab shares the same container via exec.
	t2 := openTerminal(t, client, "t2")
	t2.stdin.Write([]byte("echo tab-two-ready\n"))
	t2.waitOutput(t, "tab-two-ready")

	var status protocol.TerminalStatusResult
	if err := ctrl.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
		t.Fatalf("terminal.status: %v", err)
	}
	if !status.Running || len(status.Tabs) < 2 {
		t.Fatalf("status = %+v, want running with both tabs", status)
	}

	// Detach both tabs, restart the server: the container is re-adopted
	// and the same main session answers a fresh attach.
	main.close()
	t2.close()
	stopServer()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server.Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server did not shut down")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("server.Close: %v", err)
	}

	srv = newServer()
	runCtx2, stopServer2 := context.WithCancel(ctx)
	defer stopServer2()
	runDone2 := make(chan error, 1)
	go func() { runDone2 <- srv.Run(runCtx2) }()
	addr = waitSSHAddr(t, srv)
	client = dialSSH(t, addr, signer)
	ctrl = openControl(t, client)

	// Recovery probes the container with a short Wait before adopting it,
	// so the first status calls can race it; poll with a fresh decode.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var fresh protocol.TerminalStatusResult
		if err := ctrl.Call(protocol.MethodTerminalStatus, struct{}{}, &fresh); err != nil {
			t.Fatalf("terminal.status after restart: %v", err)
		}
		if fresh.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status after restart = %+v, want the re-adopted container running", fresh)
		}
		time.Sleep(200 * time.Millisecond)
	}
	reattached := openTerminal(t, client, "main")
	reattached.stdin.Write([]byte("echo back-again\n"))
	reattached.waitOutput(t, "back-again")
	reattached.close()

	// terminal.stop tears the container down; status reports not running.
	if err := ctrl.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop: %v", err)
	}
	if err := ctrl.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
		t.Fatalf("terminal.status after stop: %v", err)
	}
	if status.Running {
		t.Fatalf("status after stop = %+v, want stopped", status)
	}

	stopServer2()
	select {
	case err := <-runDone2:
		if err != nil {
			t.Fatalf("server.Run after restart: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("restarted server did not shut down")
	}
	verifyNoLeaks(t)
}

// openTerminal opens the aether-terminal subsystem for one tab and returns
// the live stream.
func openTerminal(t *testing.T, client *ssh.Client, tab string) *attachConn {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("terminal session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err = sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = sess.RequestSubsystem(protocol.SubsystemTerminal); err != nil {
		t.Fatalf("aether-terminal subsystem: %v", err)
	}
	header, err := json.Marshal(protocol.TerminalRequest{Tab: tab})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stdin.Write(append(header, '\n')); err != nil {
		t.Fatalf("write terminal header: %v", err)
	}
	r := bufio.NewReader(stdout)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read terminal ack: %v", err)
	}
	var ack protocol.TerminalResponse
	if err := json.Unmarshal(line, &ack); err != nil || !ack.OK {
		t.Fatalf("terminal ack = %s (err %v)", line, fmt.Errorf("%v", err))
	}
	if ack.Tab != tab {
		t.Fatalf("terminal ack tab = %q, want %q", ack.Tab, tab)
	}
	a := &attachConn{sess: sess, stdin: stdin}
	go a.pump(r)
	return a
}
