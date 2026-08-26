package sshd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// scriptedSub is a hand-driven events.Subscription: the test controls
// exactly which events the server dequeues and when the drop counter
// becomes visible.
type scriptedSub struct {
	out     chan events.Event
	dropped atomic.Uint64
	once    sync.Once
	closed  chan struct{}
}

func newScriptedSub() *scriptedSub {
	return &scriptedSub{out: make(chan events.Event), closed: make(chan struct{})}
}

func (s *scriptedSub) Events() <-chan events.Event { return s.out }
func (s *scriptedSub) Dropped() uint64             { return s.dropped.Load() }
func (s *scriptedSub) Err() error                  { return nil }
func (s *scriptedSub) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// scriptedBus hands out one scriptedSub and forwards Publish to a real
// bus so unrelated publishes (presence) still work.
type scriptedBus struct {
	events.Bus
	sub *scriptedSub
}

func (b *scriptedBus) Subscribe(context.Context, events.SubscribeOptions) (events.Subscription, error) {
	return b.sub, nil
}

// TestEventsDropClosesBeforePostGapWrite reproduces the drop-detection
// race: after the bus drops an event, the server must close the channel
// without writing any post-gap event, or the client's resubscribe cursor
// permanently skips the dropped event.
func TestEventsDropClosesBeforePostGapWrite(t *testing.T) {
	sub := newScriptedSub()
	var inner events.Bus
	e := newTestEnv(t, func(c *Config) {
		inner = c.Bus
		c.Bus = &scriptedBus{Bus: inner, sub: sub}
	})
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemEvents, nil)
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte("{}\n")); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	var ack protocol.SubscribeResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("ack = %+v", ack)
	}

	mk := func(seq uint64) events.Event {
		return events.Event{
			ID: "evt", Seq: seq, Time: time.Now().UTC(), WorkspaceID: e.ws.ID,
			RunID: e.run.ID, Type: events.TypeRunStatus,
			Payload: events.RunStatusPayload{To: "running"},
		}
	}

	// e2 is delivered cleanly.
	sub.out <- mk(2)
	var ev protocol.Event
	readJSONLine(t, r, &ev)
	if ev.Seq != 2 {
		t.Fatalf("first event seq = %d, want 2", ev.Seq)
	}

	// e3 is dropped by the bus; e4 (post-gap) is the next dequeued event.
	// The server must close without writing e4: if the client sees seq 4,
	// resubscribing with after_seq=4 loses e3 forever.
	sub.dropped.Store(1)
	go func() {
		select {
		case sub.out <- mk(4):
		case <-sub.closed:
		}
	}()

	got := make(chan []byte, 1)
	fail := make(chan error, 1)
	go func() {
		line, err := protocol.ReadLine(r)
		if err != nil {
			fail <- err
			return
		}
		got <- line
	}()
	select {
	case line := <-got:
		if strings.Contains(string(line), `"seq":4`) {
			t.Fatalf("post-gap event was written after a drop: %s", line)
		}
	case err := <-fail:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("stream ended with %v, want EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server neither closed nor wrote after the drop")
	}
}

// TestEventsSurviveStdinHalfClose reproduces the piped-client trap: a
// client that half-closes its write side (stdin EOF) must keep receiving
// events - per the contract only closing the channel unsubscribes.
func TestEventsSurviveStdinHalfClose(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemEvents, nil)
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	var ack protocol.SubscribeResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("ack = %+v", ack)
	}

	if err := pipe.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // let the EOF reach the server

	if _, err := e.bus.Publish(context.Background(), events.Event{
		WorkspaceID: e.ws.ID, RunID: e.run.ID,
		Payload: events.RunStatusPayload{To: "running"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var ev protocol.Event
	readJSONLine(t, r, &ev)
	if ev.Type != "run.status" {
		t.Errorf("event = %+v", ev)
	}
}

// TestServeContextCancelClosesConnections reproduces the shutdown gap:
// canceling the Serve context must terminate established connections,
// not just the accept loop.
func TestServeContextCancelClosesConnections(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	c := protocol.NewClient(pipe)
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, nil); err != nil {
		t.Fatalf("server.info before cancel: %v", err)
	}

	e.serveCancel()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := c.Call(protocol.MethodServerInfo, struct{}{}, nil); err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				t.Fatalf("connection survived ctx cancel; got rpc error %v", pe)
			}
			return // transport error: the connection is gone
		}
		if time.Now().After(deadline) {
			t.Fatal("established connection still serving RPCs after Serve ctx cancel")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAttachLateErrorReportsFailure reproduces the swallowed late attach
// failure: when Attach errors after the ack grace, the client must still
// be able to distinguish the failure from a clean session end (which is
// exit-status 0).
func TestAttachLateErrorReportsFailure(t *testing.T) {
	e := newTestEnv(t, nil)
	e.pty.setErr(errWriteDenied)
	e.pty.mu.Lock()
	e.pty.errDelay = 2 * attachAckGrace
	e.pty.mu.Unlock()

	ch, reqs, err := e.dial(t).OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer ch.Close() //nolint:errcheck
	exitCh := make(chan uint32, 1)
	go func() {
		for req := range reqs {
			if req.Type == "exit-status" {
				var p struct{ Status uint32 }
				if ssh.Unmarshal(req.Payload, &p) == nil {
					select {
					case exitCh <- p.Status:
					default:
					}
				}
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
		close(exitCh)
	}()
	ptyReq := struct {
		Term          string
		Cols, Rows    uint32
		Width, Height uint32
		Modes         string
	}{Term: "xterm", Cols: 80, Rows: 24}
	if ok, rerr := ch.SendRequest("pty-req", true, ssh.Marshal(&ptyReq)); rerr != nil || !ok {
		t.Fatalf("pty-req: ok=%v err=%v", ok, rerr)
	}
	if ok, rerr := ch.SendRequest("subsystem", true, ssh.Marshal(&struct{ Name string }{protocol.SubsystemAttach})); rerr != nil || !ok {
		t.Fatalf("subsystem: ok=%v err=%v", ok, rerr)
	}
	r := bufio.NewReader(ch)
	if _, err := ch.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		// Even better: the error beat the ack. Nothing more to check.
		if ack.Code != protocol.CodeDenied {
			t.Errorf("ack = %+v, want denied", ack)
		}
		return
	}

	select {
	case status, ok := <-exitCh:
		if !ok {
			t.Fatal("channel closed without an exit-status: late attach failure is indistinguishable from a clean session end")
		}
		if status == 0 {
			t.Fatalf("exit-status = 0, want nonzero for the failed attach")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit-status after the failed attach")
	}
}

// TestHandshakeDeadline reproduces the unauthenticated resource hold: a
// TCP client that stalls mid-handshake must be disconnected once the
// handshake deadline passes.
func TestHandshakeDeadline(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.handshakeTimeout = 200 * time.Millisecond })
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	for {
		if _, rerr := conn.Read(buf); rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return // server hung up on the stalled handshake
			}
			t.Fatalf("read = %v, want EOF from the server-side deadline", rerr)
		}
	}
}

// TestHandshakeCap reproduces the unbounded concurrent-handshake hold:
// with the cap saturated by a stalled pre-auth connection, further
// connections are shed immediately instead of pinning goroutines.
func TestHandshakeCap(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.handshakeTimeout = 10 * time.Second
		c.maxHandshakes = 1
	})
	stalled, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer stalled.Close() //nolint:errcheck
	buf := make([]byte, 512)
	_ = stalled.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, rerr := stalled.Read(buf); rerr != nil {
		t.Fatalf("read server ident: %v", rerr)
	}

	shed, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer shed.Close() //nolint:errcheck
	_ = shed.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, rerr := shed.Read(buf); rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return // shed while over the cap
			}
			t.Fatalf("read = %v, want EOF for the over-cap connection", rerr)
		}
	}
}

// TestRemovedMemberLosesAccess reproduces the missing revocation path:
// deleting a member must cut off their established connection's control
// RPCs, git transport, and attach - not just future handshakes.
func TestRemovedMemberLosesAccess(t *testing.T) {
	e := newTestEnv(t, nil)
	signer := newSigner(t)
	ghost := &domain.Member{
		DisplayName: "Ghost",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#911eb4",
		Role:        domain.RoleCollaborator,
	}
	if err := e.store.CreateMember(context.Background(), ghost); err != nil {
		t.Fatalf("create member: %v", err)
	}
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	c := protocol.NewClient(pipe)
	if cerr := c.Call(protocol.MethodServerInfo, struct{}{}, nil); cerr != nil {
		t.Fatalf("server.info before removal: %v", cerr)
	}

	if derr := e.store.DeleteMember(context.Background(), ghost.ID); derr != nil {
		t.Fatalf("delete member: %v", derr)
	}

	err = c.Call(protocol.MethodServerInfo, struct{}{}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("server.info after removal = %v, want CodeDenied", err)
	}

	// Git transport on the same connection is denied too.
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close() //nolint:errcheck
	var exitErr *ssh.ExitError
	if rerr := sess.Run("git-upload-pack '" + string(e.ws.ID) + ".git'"); !errors.As(rerr, &exitErr) || exitErr.ExitStatus() == 0 {
		t.Errorf("git exec after removal = %v, want nonzero exit", rerr)
	}
	if len(e.git.Calls()) != 0 {
		t.Errorf("git transport reached for a removed member: %v", e.git.Calls())
	}
}

// blockingRuns parks Launch until released so a handler is provably
// in-flight when the server shuts down.
type blockingRuns struct {
	*fakeRuns
	entered chan struct{}
	release chan struct{}
}

func (b *blockingRuns) Launch(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	close(b.entered)
	<-b.release
	return b.fakeRuns.Launch(ctx, workspace, member, task, harness, mode)
}

// TestCloseWaitsForInFlightHandlers pins the shutdown contract Close now
// provides: it must not return while a connection handler is still calling
// into a seam collaborator, so the wired server can close the scheduler,
// store, and git engine afterwards without racing live handlers.
func TestCloseWaitsForInFlightHandlers(t *testing.T) {
	br := &blockingRuns{fakeRuns: &fakeRuns{}, entered: make(chan struct{}), release: make(chan struct{})}
	e := newTestEnv(t, func(c *Config) { c.Runs = br })
	c := controlClient(t, e)

	go func() {
		_ = c.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			WorkspaceID: string(e.ws.ID), Task: "t", Harness: "claude",
		}, nil)
	}()
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Launch never entered")
	}

	closed := make(chan struct{})
	go func() {
		_ = e.srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a handler was still inside RunController.Launch")
	case <-time.After(200 * time.Millisecond):
	}
	close(br.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the handler finished")
	}
}
