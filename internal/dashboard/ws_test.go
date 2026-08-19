package dashboard

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func (e *env) dial(path, token string) *websocket.Conn {
	e.t.Helper()
	url := "ws" + strings.TrimPrefix(e.base, "http") + path + "?token=" + token
	ctx, cancel := context.WithTimeout(e.t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		e.t.Fatalf("dial %s: %v", path, err)
	}
	e.t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func (e *env) publish(msg string) events.Event {
	e.t.Helper()
	ev, err := e.bus.Publish(e.t.Context(), events.Event{
		SessionID: e.sess.ID,
		RunID:     e.run.ID,
		Payload:   events.RunStatusPayload{To: domain.RunRunning, Reason: msg},
	})
	if err != nil {
		e.t.Fatalf("publish: %v", err)
	}
	return ev
}

func readWire[T any](t *testing.T, conn *websocket.Conn) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var v T
	if err := wsjson.Read(ctx, conn, &v); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return v
}

func writeWire(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, v); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// TestEventsResumeAfterReconnect proves the replay cursor survives a lost
// socket: a client that reconnects with its last seq sees the events it
// missed and no duplicates.
func TestEventsResumeAfterReconnect(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.viewer)

	first := e.publish("one")
	e.publish("two")

	conn := e.dial("/ws/events", token)
	writeWire(t, conn, protocol.SubscribeRequest{Replay: true})
	if ack := readWire[protocol.SubscribeResponse](t, conn); !ack.OK {
		t.Fatalf("subscribe refused: %+v", ack)
	}
	if ev := readWire[protocol.Event](t, conn); ev.Seq != first.Seq {
		t.Fatalf("first replayed seq = %d, want %d", ev.Seq, first.Seq)
	}
	second := readWire[protocol.Event](t, conn)
	_ = conn.CloseNow()

	third := e.publish("three")
	conn = e.dial("/ws/events", token)
	writeWire(t, conn, protocol.SubscribeRequest{Replay: true, AfterSeq: second.Seq})
	if ack := readWire[protocol.SubscribeResponse](t, conn); !ack.OK {
		t.Fatalf("resubscribe refused: %+v", ack)
	}
	ev := readWire[protocol.Event](t, conn)
	if ev.Seq != third.Seq {
		t.Fatalf("resumed at seq %d, want %d - the cursor replayed the wrong window", ev.Seq, third.Seq)
	}

	// A frame after the header is discarded, not fatal: a client
	// keepalive must not cost it the live tail.
	writeWire(t, conn, protocol.SubscribeRequest{})
	fourth := e.publish("four")
	if ev := readWire[protocol.Event](t, conn); ev.Seq != fourth.Seq {
		t.Fatalf("live seq = %d, want %d", ev.Seq, fourth.Seq)
	}
}

func TestEventsRequiresToken(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.base, "http")+"/ws/events", nil); err == nil {
		t.Fatal("untokened event subscription was accepted")
	}
}

// TestAttachMirrorIsReadOnly covers the attach contract: a viewer gets the
// mirror but cannot type into it, and cannot ask for write either.
func TestAttachMirrorIsReadOnly(t *testing.T) {
	e := newEnv(t)
	e.pty.output = []byte("agent output")
	token := e.mint(e.viewer)

	conn := e.dial("/ws/attach/"+string(e.run.ID), token)
	writeWire(t, conn, protocol.DashAttachRequest{Cols: 100, Rows: 30})
	if ack := readWire[protocol.AttachResponse](t, conn); !ack.OK || ack.Cols != 100 {
		t.Fatalf("attach refused or wrong geometry: %+v", ack)
	}
	typ, out, err := conn.Read(t.Context())
	if err != nil || typ != websocket.MessageBinary || string(out) != "agent output" {
		t.Fatalf("output frame = %v %q (%v), want binary %q", typ, out, err, "agent output")
	}

	writeWire(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "rm -rf /\r"})
	// The resize behind it is the sync point: control frames are handled
	// in order, so once it lands the input frame has been dropped.
	writeWire(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachResize, Cols: 120, Rows: 40})
	waitFor(t, func() bool { return len(e.pty.resizes()) > 0 }, "resize to reach the pty host")
	if got := e.pty.input(); got != "" {
		t.Fatalf("read-only attach delivered input %q", got)
	}
	if !e.pty.wasReadOnly() {
		t.Fatal("attach was not opened read-only")
	}

	// Asking for write without the steer capability is refused outright.
	conn = e.dial("/ws/attach/"+string(e.run.ID), token)
	writeWire(t, conn, protocol.DashAttachRequest{Write: true})
	ack := readWire[protocol.AttachResponse](t, conn)
	if ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("viewer write attach = %+v, want refused with code %d", ack, protocol.CodeDenied)
	}
}

func TestAttachWriteDeliversInput(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)

	conn := e.dial("/ws/attach/"+string(e.run.ID), token)
	writeWire(t, conn, protocol.DashAttachRequest{Write: true, Cols: 80, Rows: 24})
	if ack := readWire[protocol.AttachResponse](t, conn); !ack.OK {
		t.Fatalf("write attach refused: %+v", ack)
	}
	writeWire(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "make test\r"})
	waitFor(t, func() bool { return e.pty.input() == "make test\r" }, "input to reach the pty host")
	if e.pty.wasReadOnly() {
		t.Fatal("write attach was opened read-only")
	}
}

func TestAttachUnknownRunIsRefused(t *testing.T) {
	e := newEnv(t)
	conn := e.dial("/ws/attach/run_missing", e.mint(e.admin))
	writeWire(t, conn, protocol.DashAttachRequest{})
	ack := readWire[protocol.AttachResponse](t, conn)
	if ack.OK || ack.Code != protocol.CodeNotFound {
		t.Fatalf("attach to an unknown run = %+v, want refused with code %d", ack, protocol.CodeNotFound)
	}
}

// TestRevokeEndsLiveStreams: revocation has to reach sockets that are
// already open, or a write attach opened a moment before `aether dash`
// exits keeps typing into the agent's terminal with a token that no longer
// exists.
func TestRevokeEndsLiveStreams(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)

	attach := e.dial("/ws/attach/"+string(e.run.ID), token)
	writeWire(t, attach, protocol.DashAttachRequest{Write: true})
	if ack := readWire[protocol.AttachResponse](t, attach); !ack.OK {
		t.Fatalf("attach refused: %+v", ack)
	}
	events := e.dial("/ws/events", token)
	writeWire(t, events, protocol.SubscribeRequest{})
	if ack := readWire[protocol.SubscribeResponse](t, events); !ack.OK {
		t.Fatalf("subscribe refused: %+v", ack)
	}

	resp := e.gw.cfg.RPC.Call(t.Context(), e.admin, protocol.MethodDashTokenRevoke,
		mustJSON(t, protocol.DashTokenRevokeParams{Token: token}))
	if resp.Error != nil {
		t.Fatalf("dash.token.revoke: %v", resp.Error)
	}

	for name, conn := range map[string]*websocket.Conn{"attach": attach, "events": events} {
		expectPolicyClose(t, name, conn)
	}
}

// TestRevokeBeforeAttachHeader: the handshake's token check is a
// snapshot and the header may arrive much later, so a socket parked
// before its header must not survive a revoke and be converted into a
// live attach afterwards.
func TestRevokeBeforeAttachHeader(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)
	conn := e.dial("/ws/attach/"+string(e.run.ID), token)

	resp := e.gw.cfg.RPC.Call(t.Context(), e.admin, protocol.MethodDashTokenRevoke,
		mustJSON(t, protocol.DashTokenRevokeParams{Token: token}))
	if resp.Error != nil {
		t.Fatalf("dash.token.revoke: %v", resp.Error)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, protocol.DashAttachRequest{Write: true}); err != nil {
		// The pre-header watch already tore the socket down.
		return
	}
	var ack protocol.AttachResponse
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("parked socket after revoke: err = %v, want a refusal or policy-violation close", err)
		}
		return
	}
	if ack.OK {
		t.Fatal("revoked token converted a parked socket into a live attach")
	}
	if ack.Code != protocol.CodeDenied {
		t.Fatalf("refusal code = %d, want %d", ack.Code, protocol.CodeDenied)
	}
}

// TestRevokeBeforeEventsHeader: the events socket keeps the guarantee the
// attach socket makes - a token revoked while the socket is parked before
// its header must not be converted into a live subscription.
func TestRevokeBeforeEventsHeader(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)
	conn := e.dial("/ws/events", token)

	resp := e.gw.cfg.RPC.Call(t.Context(), e.admin, protocol.MethodDashTokenRevoke,
		mustJSON(t, protocol.DashTokenRevokeParams{Token: token}))
	if resp.Error != nil {
		t.Fatalf("dash.token.revoke: %v", resp.Error)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, protocol.SubscribeRequest{}); err != nil {
		// The pre-header watch already tore the socket down.
		return
	}
	var ack protocol.SubscribeResponse
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("parked socket after revoke: err = %v, want a refusal or policy-violation close", err)
		}
		return
	}
	if ack.OK {
		t.Fatal("revoked token converted a parked socket into a live subscription")
	}
	if ack.Code != protocol.CodeDenied {
		t.Fatalf("refusal code = %d, want %d", ack.Code, protocol.CodeDenied)
	}
}

// expectPolicyClose reads until the socket fails, so frames already in
// flight - the attach's own presence events reach the event stream in this
// test - do not hide the close that follows them.
func expectPolicyClose(t *testing.T, name string, conn *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Errorf("%s socket after revoke: err = %v, want a policy-violation close", name, err)
			}
			return
		}
	}
}

// TestRemovedMemberLosesLiveStream: a token acts with its member's
// current authority, so a stream has to end when the membership behind it
// does - re-resolving the token alone would keep a removed member on the
// deployment's event feed.
func TestRemovedMemberLosesLiveStream(t *testing.T) {
	e := newEnv(t)
	conn := e.dial("/ws/events", e.mint(e.collab))
	writeWire(t, conn, protocol.SubscribeRequest{})
	if ack := readWire[protocol.SubscribeResponse](t, conn); !ack.OK {
		t.Fatalf("subscribe refused: %+v", ack)
	}
	if err := e.db.DeleteMember(t.Context(), e.collab); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	expectPolicyClose(t, "events", conn)
}

// TestProtectEndsLiveWriteAttach: the steer capability is checked once
// before the attach, and ptyhost is not asked again, so protecting the run
// mid-stream has to end the socket - otherwise the writer keeps typing
// into someone else's agent.
func TestProtectEndsLiveWriteAttach(t *testing.T) {
	e := newEnv(t)
	conn := e.dial("/ws/attach/"+string(e.run.ID), e.mint(e.collab))
	writeWire(t, conn, protocol.DashAttachRequest{Write: true})
	if ack := readWire[protocol.AttachResponse](t, conn); !ack.OK {
		t.Fatalf("collaborator write attach refused: %+v", ack)
	}
	// Protecting the admin's run restricts steering to its owner and
	// admins, which the collaborator is neither of.
	if err := e.db.SetRunProtected(t.Context(), e.run.ID, true); err != nil {
		t.Fatalf("protect run: %v", err)
	}
	expectPolicyClose(t, "attach", conn)
}

// TestAttachPublishesPresence: the roster's watching set is built from
// these two events, so a browser mirror that skips them lets two members
// steer one run each believing they are alone.
func TestAttachPublishesPresence(t *testing.T) {
	e := newEnv(t)
	sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Run: e.run.ID, Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	conn := e.dial("/ws/attach/"+string(e.run.ID), e.mint(e.viewer))
	writeWire(t, conn, protocol.DashAttachRequest{})
	if ack := readWire[protocol.AttachResponse](t, conn); !ack.OK {
		t.Fatalf("attach refused: %+v", ack)
	}
	if got := nextPresence(t, sub); got.State != events.PresenceWatching {
		t.Fatalf("on attach the roster saw %q, want %q", got.State, events.PresenceWatching)
	}
	_ = conn.CloseNow()
	if got := nextPresence(t, sub); got.State != events.PresenceOnline {
		t.Fatalf("on detach the roster saw %q, want %q", got.State, events.PresenceOnline)
	}
}

func nextPresence(t *testing.T, sub events.Subscription) events.PresencePayload {
	t.Helper()
	select {
	case ev, ok := <-sub.Events():
		if !ok {
			t.Fatal("presence subscription closed early")
		}
		p, isPresence := ev.Payload.(events.PresencePayload)
		if !isPresence {
			t.Fatalf("payload = %T, want a presence payload", ev.Payload)
		}
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a presence event")
		return events.PresencePayload{}
	}
}

// TestCloseEndsLiveStreams pins the shutdown contract: an idle event
// subscription and a live attach both hold handler goroutines the HTTP
// server's own Shutdown cannot see, so Close has to end them itself. A
// regression here hangs server shutdown, not just this test.
func TestCloseEndsLiveStreams(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)

	events := e.dial("/ws/events", token)
	writeWire(t, events, protocol.SubscribeRequest{})
	if ack := readWire[protocol.SubscribeResponse](t, events); !ack.OK {
		t.Fatalf("subscribe refused: %+v", ack)
	}
	attach := e.dial("/ws/attach/"+string(e.run.ID), token)
	writeWire(t, attach, protocol.DashAttachRequest{})
	if ack := readWire[protocol.AttachResponse](t, attach); !ack.OK {
		t.Fatalf("attach refused: %+v", ack)
	}

	closed := make(chan error, 1)
	go func() { closed <- e.gw.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Gateway.Close blocked while streams were still attached")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fakePTY stands in for the PTY host: it echoes a fixed banner and records
// everything the gateway hands it, so the tests can assert on what a
// read-only attach is allowed to deliver.
type fakePTY struct {
	output []byte

	mu       sync.Mutex
	in       []byte
	sizes    [][2]uint
	readOnly bool
}

func newFakePTY() *fakePTY { return &fakePTY{} }

func (p *fakePTY) input() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.in)
}

func (p *fakePTY) resizes() [][2]uint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][2]uint(nil), p.sizes...)
}

func (p *fakePTY) wasReadOnly() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readOnly
}

func (p *fakePTY) Attach(ctx context.Context, _ domain.RunID, _ domain.MemberID, _, _ uint, readOnly bool, conn io.ReadWriter, resize <-chan [2]uint) error {
	p.mu.Lock()
	p.readOnly = readOnly
	p.mu.Unlock()
	if len(p.output) > 0 {
		if _, err := conn.Write(p.output); err != nil {
			return err
		}
	}
	go func() {
		for {
			select {
			case sz := <-resize:
				p.mu.Lock()
				p.sizes = append(p.sizes, sz)
				p.mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	buf := make([]byte, 256)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.in = append(p.in, buf[:n]...)
			p.mu.Unlock()
		}
		if err != nil {
			return nil
		}
	}
}
