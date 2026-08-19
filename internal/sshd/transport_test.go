package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestGitExecUploadPack(t *testing.T) {
	e := newTestEnv(t, nil)
	client := e.dial(t)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close() //nolint:errcheck
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	if err := sess.Start(fmt.Sprintf("git-upload-pack '/%s.git'", e.ws.ID)); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var adv []byte
	buf := make([]byte, 1024)
	for !strings.Contains(string(adv), "refs/heads/main") {
		n, err := stdout.Read(buf)
		adv = append(adv, buf[:n]...)
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(adv), "refs/heads/main") {
		t.Errorf("advertisement %q does not name refs/heads/main", adv)
	}
	if _, err := stdin.Write([]byte("0000")); err != nil {
		t.Fatalf("write flush: %v", err)
	}
	_ = stdin.Close()
	if err := sess.Wait(); err != nil {
		t.Fatalf("exit status: %v", err)
	}
	if calls := e.git.Calls(); len(calls) != 1 || calls[0] != "upload-pack:"+string(e.ws.ID) {
		t.Errorf("git calls = %v", calls)
	}
}

func TestGitExecUnknownWorkspace(t *testing.T) {
	e := newTestEnv(t, nil)
	sess, err := e.dial(t).NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close() //nolint:errcheck
	var stderr strings.Builder
	sess.Stderr = &stderr
	err = sess.Run("git-upload-pack 'ws_missing.git'")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 128 {
		t.Fatalf("err = %v, want exit-status 128", err)
	}
	if !strings.Contains(stderr.String(), "ws_missing") {
		t.Errorf("stderr = %q, want it to name the workspace", stderr.String())
	}
	if len(e.git.Calls()) != 0 {
		t.Errorf("git transport was called for an unknown workspace: %v", e.git.Calls())
	}
}

// gitExecAs runs one git transport command as the given signer and
// returns the exit status and stderr.
func gitExecAs(t *testing.T, e *testEnv, signer ssh.Signer, cmd string) (int, string) {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close() //nolint:errcheck
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close() //nolint:errcheck
	var stderr strings.Builder
	sess.Stderr = &stderr
	runErr := sess.Run(cmd)
	var exitErr *ssh.ExitError
	switch {
	case runErr == nil:
		return 0, stderr.String()
	case errors.As(runErr, &exitErr):
		return exitErr.ExitStatus(), stderr.String()
	default:
		t.Fatalf("run %q: %v", cmd, runErr)
		return 0, ""
	}
}

// TestGitPushDeniedForViewer pins the git transport to the same role
// boundary as the control channel: a viewer may fetch but not push, and
// the denial happens before the git seam is touched.
func TestGitPushDeniedForViewer(t *testing.T) {
	e := newTestEnv(t, nil)
	viewer, _ := addMember(t, e, "vera", domain.RoleViewer, false)

	code, stderr := gitExecAs(t, e, viewer, fmt.Sprintf("git-receive-pack '/%s.git'", e.ws.ID))
	if code == 0 {
		t.Fatalf("viewer push succeeded, want refusal (stderr %q)", stderr)
	}
	if !strings.Contains(stderr, "collaborator") {
		t.Errorf("stderr = %q, want it to name the required role", stderr)
	}
	if calls := e.git.Calls(); len(calls) != 0 {
		t.Errorf("git transport reached on denied push: %v", calls)
	}

	// The same viewer must still be able to read.
	if code, stderr := gitExecAs(t, e, viewer, fmt.Sprintf("git-upload-pack '/%s.git'", e.ws.ID)); code != 0 {
		t.Errorf("viewer fetch = exit %d (stderr %q), want success", code, stderr)
	}
	if calls := e.git.Calls(); len(calls) != 1 || calls[0] != "upload-pack:"+string(e.ws.ID) {
		t.Errorf("git calls = %v, want one upload-pack", calls)
	}
}

// TestGitPushAllowedForCollaborator is the positive half of the boundary:
// the gate must not block members who legitimately push.
func TestGitPushAllowedForCollaborator(t *testing.T) {
	e := newTestEnv(t, nil)
	collab, _ := addMember(t, e, "cass", domain.RoleCollaborator, false)

	if code, stderr := gitExecAs(t, e, collab, fmt.Sprintf("git-receive-pack '/%s.git'", e.ws.ID)); code != 0 {
		t.Fatalf("collaborator push = exit %d (stderr %q), want success", code, stderr)
	}
	if calls := e.git.Calls(); len(calls) != 1 || calls[0] != "receive-pack:"+string(e.ws.ID) {
		t.Errorf("git calls = %v, want one receive-pack", calls)
	}
}

func TestExecAndShellRejected(t *testing.T) {
	e := newTestEnv(t, nil)
	client := e.dial(t)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if rerr := sess.Run("ls -la"); rerr == nil {
		t.Error("arbitrary exec was accepted")
	}
	_ = sess.Close()

	sess, err = client.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if serr := sess.Shell(); serr == nil {
		t.Error("shell was accepted")
	}
	_ = sess.Close()

	sess, err = client.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if serr := sess.RequestSubsystem("sftp"); serr == nil {
		t.Error("unknown subsystem was accepted")
	}
	_ = sess.Close()
}

func TestDirectTCPIPForward(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "dashboard says hi")
	}))
	defer backend.Close()
	_, portStr, _ := net.SplitHostPort(backend.Listener.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("backend port %q: %v", portStr, err)
	}

	e := newTestEnv(t, func(c *Config) { c.DashboardPort = port })
	client := e.dial(t)

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			return client.Dial(network, addr)
		},
	}}
	resp, err := httpc.Get("http://127.0.0.1:" + portStr + "/")
	if err != nil {
		t.Fatalf("GET through forward: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "dashboard says hi" {
		t.Errorf("body = %q", body)
	}

	if _, err := client.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Error("forward to a non-dashboard port was allowed")
	}
	if _, err := client.Dial("tcp", "example.com:"+portStr); err == nil {
		t.Error("forward to a non-loopback host was allowed")
	}
}

func TestDirectTCPIPDeniedByDefault(t *testing.T) {
	e := newTestEnv(t, nil) // DashboardPort 0
	client := e.dial(t)
	if _, err := client.Dial("tcp", "127.0.0.1:80"); err == nil {
		t.Error("forward allowed with DashboardPort 0")
	}
}

func TestEventsSubscribeAndStream(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemEvents, nil)
	r := bufio.NewReader(pipe)

	sub, _ := json.Marshal(protocol.SubscribeRequest{RunID: string(e.run.ID), Types: []string{"run.status"}})
	if _, err := pipe.Write(append(sub, '\n')); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	var ack protocol.SubscribeResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("subscribe ack = %+v", ack)
	}

	// A non-matching event (different run) must not arrive.
	if _, err := e.bus.Publish(context.Background(), events.Event{
		SessionID: e.sess.ID, RunID: "run_other",
		Payload: events.RunStatusPayload{To: "running"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := e.bus.Publish(context.Background(), events.Event{
		SessionID: e.sess.ID, RunID: e.run.ID, ActorID: e.member.ID,
		Payload: events.RunStatusPayload{From: "running", To: "needs-attention", Reason: "stalled"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var ev protocol.Event
	readJSONLine(t, r, &ev)
	if ev.Type != "run.status" || ev.RunID != string(e.run.ID) || ev.SessionID != string(e.sess.ID) {
		t.Errorf("event = %+v", ev)
	}
	if ev.Seq == 0 || ev.ID == "" {
		t.Errorf("event missing seq/id: %+v", ev)
	}
	var payload events.RunStatusPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.To != "needs-attention" || payload.Reason != "stalled" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestEventsReplayWithoutLog(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemEvents, nil)
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"replay":true}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ack protocol.SubscribeResponse
	readJSONLine(t, r, &ack)
	if ack.OK || ack.Code != protocol.CodeUnavailable {
		t.Errorf("ack = %+v, want unavailable", ack)
	}
}

func readJSONLine(t *testing.T, r *bufio.Reader, v any) {
	t.Helper()
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := protocol.ReadLine(r)
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read line: %v", res.err)
		}
		if err := json.Unmarshal(res.line, v); err != nil {
			t.Fatalf("unmarshal %q: %v", res.line, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a line")
	}
}

func TestAttachRoundTrip(t *testing.T) {
	e := newTestEnv(t, nil)
	e.pty.replay = []byte("scrollback$ ")

	presence, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer presence.Close() //nolint:errcheck

	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemAttach, func(s *ssh.Session) error {
		return s.RequestPty("xterm-256color", 40, 100, ssh.TerminalModes{})
	})
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	if !ack.OK || ack.Cols != 100 || ack.Rows != 40 {
		t.Fatalf("ack = %+v, want ok with pty-req geometry 100x40", ack)
	}

	buf := make([]byte, len(e.pty.replay))
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if string(buf) != "scrollback$ " {
		t.Errorf("replay = %q", buf)
	}

	if _, err := pipe.Write([]byte("make test\r")); err != nil {
		t.Fatalf("write keystrokes: %v", err)
	}
	echo := make([]byte, len("echo:make test\r"))
	if _, err := io.ReadFull(r, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != "echo:make test\r" {
		t.Errorf("echo = %q", echo)
	}

	wantPresence(t, presence, events.PresenceWatching, e)

	_ = pipe.Close() // detach

	wantPresence(t, presence, events.PresenceOnline, e)

	cols, rows, readOnly, input, _ := e.pty.state()
	if cols != 100 || rows != 40 || readOnly {
		t.Errorf("attach params = %dx%d readOnly=%v, want 100x40 writable", cols, rows, readOnly)
	}
	if input != "make test\r" {
		t.Errorf("pty input = %q", input)
	}
}

func wantPresence(t *testing.T, sub events.Subscription, state events.PresenceState, e *testEnv) {
	t.Helper()
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.PresencePayload)
		if !ok || p.State != state {
			t.Errorf("presence = %+v, want %s", ev.Payload, state)
		}
		if ev.RunID != e.run.ID || ev.ActorID != e.member.ID {
			t.Errorf("presence run/actor = %s/%s", ev.RunID, ev.ActorID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s presence event", state)
	}
}

func TestAttachWithoutPTYReqIsReadOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	e.pty.replay = []byte("out")
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemAttach, nil)
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(e.run.ID) + `","cols":90,"rows":25}` + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	if !ack.OK || ack.Cols != 90 || ack.Rows != 25 {
		t.Fatalf("ack = %+v, want header geometry 90x25", ack)
	}
	_, _, readOnly, _, _ := e.pty.state()
	if !readOnly {
		t.Error("attach without pty-req was not forced read-only")
	}
}

func TestAttachErrors(t *testing.T) {
	e := newTestEnv(t, nil)

	// Unknown run.
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemAttach, nil)
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"run_missing"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	if ack.OK || ack.Code != protocol.CodeNotFound {
		t.Errorf("unknown run ack = %+v, want not-found", ack)
	}

	// Write denied by the gate.
	e.pty.setErr(errWriteDenied)
	pipe = openSubsystem(t, e.dial(t), protocol.SubsystemAttach, func(s *ssh.Session) error {
		return s.RequestPty("xterm", 24, 80, ssh.TerminalModes{})
	})
	r = bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	readJSONLine(t, r, &ack)
	if ack.OK || ack.Code != protocol.CodeDenied {
		t.Errorf("denied ack = %+v, want denied", ack)
	}

	// No live session.
	e.pty.setErr(errNoSession)
	pipe = openSubsystem(t, e.dial(t), protocol.SubsystemAttach, nil)
	r = bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	readJSONLine(t, r, &ack)
	if ack.OK || ack.Code != protocol.CodeUnavailable {
		t.Errorf("no-session ack = %+v, want unavailable", ack)
	}
}

func TestWindowChangeFeedsResize(t *testing.T) {
	e := newTestEnv(t, nil)
	e.pty.replay = []byte("x")
	var sess *ssh.Session
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemAttach, func(s *ssh.Session) error {
		sess = s
		return s.RequestPty("xterm", 24, 80, ssh.TerminalModes{})
	})
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("ack = %+v", ack)
	}
	if err := sess.WindowChange(50, 132); err != nil {
		t.Fatalf("window-change: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, _, _, resizes := e.pty.state()
		if len(resizes) > 0 {
			if resizes[0] != [2]uint{132, 50} {
				t.Errorf("resize = %v, want [132 50]", resizes[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("resize never reached the PTY host")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
