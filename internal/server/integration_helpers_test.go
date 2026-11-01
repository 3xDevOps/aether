//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func waitCoordSocket(t *testing.T, dir string) string {
	t.Helper()
	socket := filepath.Join(dir, coordtransport.SocketName)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			return socket
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("coordination socket did not appear: %s", socket)
	return ""
}

// openObserver attaches a read-only mirror. It must never acquire the human
// control lease: a controlled child run refuses wakes while it is held.
func openObserver(t *testing.T, client *ssh.Client, runID string) *attachConn {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("observer session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err := sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("observer pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemAttach); err != nil {
		t.Fatalf("observer subsystem: %v", err)
	}
	header, err := json.Marshal(protocol.AttachRequest{
		RunID: runID, ReadOnly: true, Cols: 120, Rows: 30,
		ControlSessionID: "integration-observer-" + runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(header, '\n')); err != nil {
		t.Fatalf("write observer request: %v", err)
	}
	r := bufio.NewReader(stdout)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read observer ack: %v", err)
	}
	var ack protocol.AttachResponse
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode observer ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("observer attach denied: %+v", ack)
	}
	if ack.HasControl {
		t.Fatalf("observer acquired human control: %+v", ack)
	}
	a := &attachConn{sess: sess, stdin: stdin}
	go a.pump(r)
	return a
}

// pacedCall spaces sequential socket calls by at least the transport's
// one-second token refill interval, rather than consuming its burst
// allowance. Every response, including a transport rate-limit error, reaches
// the caller unchanged; this helper never retries a rejected operation.
func pacedCall(ctx context.Context, socket, method string, params, result any) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return coordtransport.Call(ctx, socket, method, params, result)
	}
}

func waitTurns(ctx context.Context, t *testing.T, srv *Server, run domain.RunID, n int) []acphost.Item {
	t.Helper()
	for {
		page, err := srv.sched.ACPHistory(run, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		items := page.Items
		ended := 0
		for _, it := range items {
			if it.Kind == acphost.KindTurnEnd && it.StopReason == "end_turn" {
				ended++
			}
		}
		if ended >= n {
			return items
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s ended %d of %d turns: %+v", run, ended, n, items)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitPrompted(ctx context.Context, t *testing.T, srv *Server, run domain.RunID, instruction string) {
	t.Helper()
	for {
		page, err := srv.sched.ACPHistory(run, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		items := page.Items
		for _, it := range items {
			if it.Kind == acphost.KindMessage && it.Message.Role == "user" && strings.Contains(it.Message.Text, strings.TrimSpace(instruction)) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s was never prompted with %q: %+v", run, instruction, items)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
