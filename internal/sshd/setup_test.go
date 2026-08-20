package sshd

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestSetupSubsystem(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemSetup, func(s *ssh.Session) error {
		return s.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{})
	})
	if _, err := pipe.Write([]byte(`{"harness":"claude"}` + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	r := bufio.NewReader(pipe)
	var ack protocol.SetupResponse
	readJSONLine(t, r, &ack)
	if !ack.OK || ack.Cols != 80 || ack.Rows != 24 {
		t.Fatalf("ack = %+v, want ok 80x24", ack)
	}
	buf := make([]byte, len("setup-ready\n"))
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read setup output: %v", err)
	}
	if string(buf) != "setup-ready\n" {
		t.Errorf("output = %q", buf)
	}
	_ = pipe.Close()
	found := false
	for _, call := range e.runs.Calls() {
		if strings.HasPrefix(call, "setup:") && strings.Contains(call, "claude") {
			found = true
		}
	}
	if !found {
		t.Errorf("SetupLogin not called: %v", e.runs.Calls())
	}
}

// serveSetup starts a container on the host Docker daemon, so it is a
// Launch action: a viewer must be refused before the run controller is
// ever reached, while a collaborator still gets a login shell.
func TestSetupSubsystemDeniedForViewer(t *testing.T) {
	e := newTestEnv(t, nil)
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	client, err := e.dialWith(viewer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	pipe := openSubsystem(t, client, protocol.SubsystemSetup, nil)
	if _, werr := pipe.Write([]byte(`{"harness":"claude"}` + "\n")); werr != nil {
		t.Fatalf("write header: %v", werr)
	}
	var ack protocol.SetupResponse
	readJSONLine(t, bufio.NewReader(pipe), &ack)
	if ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("viewer setup ack = %+v, want denied", ack)
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("viewer setup reached the run controller: %v", calls)
	}

	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	collabClient, err := e.dialWith(collab, nil)
	if err != nil {
		t.Fatalf("dial collaborator: %v", err)
	}
	t.Cleanup(func() { _ = collabClient.Close() })
	collabPipe := openSubsystem(t, collabClient, protocol.SubsystemSetup, nil)
	if _, werr := collabPipe.Write([]byte(`{"harness":"claude"}` + "\n")); werr != nil {
		t.Fatalf("write collaborator header: %v", werr)
	}
	ack = protocol.SetupResponse{}
	readJSONLine(t, bufio.NewReader(collabPipe), &ack)
	if !ack.OK {
		t.Fatalf("collaborator setup ack = %+v, want ok", ack)
	}
}

func TestSetupSubsystemReportsControllerFailure(t *testing.T) {
	e := newTestEnv(t, nil)
	e.runs.setErr(errors.New("image unavailable"))
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemSetup, nil)
	if _, err := pipe.Write([]byte(`{"harness":"claude"}` + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	reader := bufio.NewReader(pipe)
	var ack protocol.SetupResponse
	readJSONLine(t, reader, &ack)
	if !ack.OK {
		t.Fatalf("ack = %+v", ack)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read setup failure: %v", err)
	}
	if !strings.Contains(string(body), "image unavailable") {
		t.Fatalf("setup output = %q, want controller error", body)
	}
	if err := pipe.sess.Wait(); err == nil {
		t.Fatal("setup failure returned successful SSH exit status")
	}
}

// When the setup container exits, the controller half-closes the client
// conn. The client must observe EOF on output while the channel still
// delivers the trailing zero exit status; a full close here would drop
// the status entirely. Session.Wait cannot see subsystem exits, so the
// test drives a raw session channel the same way internal/cli does.
func TestSetupSubsystemDeliversExitStatusAfterHalfClose(t *testing.T) {
	e := newTestEnv(t, nil)
	e.runs.setSetupHook(func(conn io.ReadWriter) error {
		cw, ok := conn.(interface{ CloseWrite() error })
		if !ok {
			return errors.New("setup conn does not support CloseWrite")
		}
		return cw.CloseWrite()
	})
	client := e.dial(t)
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	status := make(chan int, 1)
	go func() {
		got := -1
		for req := range reqs {
			if req.Type == "exit-status" && len(req.Payload) >= 4 {
				got = int(binary.BigEndian.Uint32(req.Payload))
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
		status <- got
	}()
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Subsystem string }{protocol.SubsystemSetup}))
	if err != nil || !ok {
		t.Fatalf("subsystem request: ok=%v err=%v", ok, err)
	}
	if _, writeErr := ch.Write([]byte(`{"harness":"claude"}` + "\n")); writeErr != nil {
		t.Fatalf("write header: %v", writeErr)
	}
	reader := bufio.NewReader(ch)
	var ack protocol.SetupResponse
	readJSONLine(t, reader, &ack)
	if !ack.OK {
		t.Fatalf("ack = %+v", ack)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read to EOF after half-close: %v", err)
	}
	if !strings.Contains(string(body), "setup-ready") {
		t.Fatalf("setup output = %q, want setup-ready", body)
	}
	select {
	case got := <-status:
		if got != 0 {
			t.Fatalf("exit status after half-close = %d, want 0", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit status not delivered after half-close")
	}
}

// The subsystem header is bounded well below the shared 32 MiB line cap:
// that budget belongs to control-channel profile pushes, not to a header
// of a handful of short fields.
func TestSetupHeaderLineBounded(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemSetup, nil)
	header := `{"harness":"claude","image":"` + strings.Repeat("A", 1<<20) + `"}` + "\n"
	_, werr := pipe.Write([]byte(header))
	rerr := readLineWithin(t, bufio.NewReader(pipe), 5*time.Second)
	if werr == nil && rerr == nil {
		t.Fatal("oversized setup header was answered instead of refused")
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("oversized setup header reached the run controller: %v", calls)
	}
}
