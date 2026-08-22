package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// wsStubBackend records the requests the WS handlers hand it and answers
// with configured streams, terminals, and acks.
type wsStubBackend struct {
	mu        sync.Mutex
	eventsReq protocol.SubscribeRequest
	attachReq protocol.AttachRequest
	shellReq  protocol.WorkspaceShellRequest

	eventsStream io.ReadWriteCloser
	eventsErr    error
	attachTerm   cli.Terminal
	attachAck    protocol.AttachResponse
	attachErr    error
	shellTerm    cli.Terminal
	shellAck     protocol.WorkspaceShellResponse
	shellErr     error
}

func (b *wsStubBackend) Call(context.Context, string, json.RawMessage) (json.RawMessage, *protocol.Error) {
	return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "not implemented"}
}

func (b *wsStubBackend) Events(req protocol.SubscribeRequest) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	b.eventsReq = req
	b.mu.Unlock()
	return b.eventsStream, b.eventsErr
}

func (b *wsStubBackend) Attach(req protocol.AttachRequest) (cli.Terminal, protocol.AttachResponse, error) {
	b.mu.Lock()
	b.attachReq = req
	b.mu.Unlock()
	return b.attachTerm, b.attachAck, b.attachErr
}

func (b *wsStubBackend) Shell(req protocol.WorkspaceShellRequest) (cli.Terminal, protocol.WorkspaceShellResponse, error) {
	b.mu.Lock()
	b.shellReq = req
	b.mu.Unlock()
	return b.shellTerm, b.shellAck, b.shellErr
}

func (b *wsStubBackend) Sync(string, bool) (io.ReadWriteCloser, error) {
	return nil, errors.New("not implemented")
}

func (b *wsStubBackend) recordedAttach() protocol.AttachRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attachReq
}

func (b *wsStubBackend) recordedEvents() protocol.SubscribeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eventsReq
}

// wsStubStream is a Backend.Events stream: canned NDJSON, discarded
// writes, idempotent close.
type wsStubStream struct {
	io.Reader
	once sync.Once
}

func newWSStubStream(lines string) *wsStubStream {
	return &wsStubStream{Reader: strings.NewReader(lines)}
}

func (s *wsStubStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *wsStubStream) Close() error                { s.once.Do(func() {}); return nil }

// wsStubTerminal is a cli.Terminal recording input and resizes; output is
// emitted on demand and finish ends Read with readErr.
type wsStubTerminal struct {
	inputCh  chan []byte
	resizeCh chan [2]uint

	out     chan []byte
	pending []byte
	done    chan struct{}
	once    sync.Once
	readErr error
}

func newWSStubTerminal(readErr error) *wsStubTerminal {
	return &wsStubTerminal{
		inputCh:  make(chan []byte, 16),
		resizeCh: make(chan [2]uint, 16),
		out:      make(chan []byte, 16),
		done:     make(chan struct{}),
		readErr:  readErr,
	}
}

func (t *wsStubTerminal) emit(p []byte) { t.out <- p }
func (t *wsStubTerminal) finish()       { t.once.Do(func() { close(t.done) }) }

func (t *wsStubTerminal) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		select {
		case b := <-t.out:
			t.pending = b
		case <-t.done:
			select {
			case b := <-t.out:
				t.pending = b
			default:
				return 0, t.readErr
			}
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

func (t *wsStubTerminal) Write(p []byte) (int, error) {
	t.inputCh <- append([]byte(nil), p...)
	return len(p), nil
}

func (t *wsStubTerminal) Resize(cols, rows uint) error {
	t.resizeCh <- [2]uint{cols, rows}
	return nil
}

func (t *wsStubTerminal) Close() error { t.finish(); return nil }

func newWSGateway(t *testing.T, b Backend) (*Gateway, string) {
	t.Helper()
	g, err := New(Config{Backend: b})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(g.mux)
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func wsDial(t *testing.T, base, path, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(base, "http") + path + "?token=" + token
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readWSJSON[T any](t *testing.T, conn *websocket.Conn) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var v T
	if err := wsjson.Read(ctx, conn, &v); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return v
}

func writeWSJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, v); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// expectClose reads until the socket fails and asserts the close code,
// returning the close reason.
func expectClose(t *testing.T, conn *websocket.Conn, want websocket.StatusCode) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("read ended without a close frame: %v", err)
		}
		if ce.Code != want {
			t.Fatalf("close code = %d (%q), want %d", ce.Code, ce.Reason, want)
		}
		return ce.Reason
	}
}

// TestEventsStreamsThenSignalsBacklogDrop covers the happy path: header,
// ok ack, event lines as text frames, and stream end reported as 4000 so
// the SPA resubscribes with its cursor.
func TestEventsStreamsThenSignalsBacklogDrop(t *testing.T) {
	b := &wsStubBackend{eventsStream: newWSStubStream("{\"seq\":1}\n{\"seq\":2}\n")}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/events", g.Token())

	writeWSJSON(t, conn, protocol.SubscribeRequest{RunID: "r1", AfterSeq: 5})
	if ack := readWSJSON[protocol.SubscribeResponse](t, conn); !ack.OK {
		t.Fatalf("ack = %+v, want ok", ack)
	}
	if req := b.recordedEvents(); req.RunID != "r1" || req.AfterSeq != 5 {
		t.Fatalf("subscribe request = %+v", req)
	}
	type seqFrame struct {
		Seq int `json:"seq"`
	}
	for want := 1; want <= 2; want++ {
		if ev := readWSJSON[seqFrame](t, conn); ev.Seq != want {
			t.Fatalf("event seq = %d, want %d", ev.Seq, want)
		}
	}
	reason := expectClose(t, conn, websocket.StatusCode(statusBacklogDropped))
	if !strings.Contains(reason, "after_seq") {
		t.Fatalf("close reason = %q, want resubscribe hint", reason)
	}
}

// TestEventsRefusalForwardsCode: a coded backend refusal reaches the
// client as an ok:false frame with the code, then a policy close.
func TestEventsRefusalForwardsCode(t *testing.T) {
	b := &wsStubBackend{eventsErr: &protocol.Error{Code: protocol.CodeDenied, Message: "subscription denied"}}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/events", g.Token())

	writeWSJSON(t, conn, protocol.SubscribeRequest{})
	ack := readWSJSON[protocol.SubscribeResponse](t, conn)
	if ack.OK || ack.Code != protocol.CodeDenied || ack.Error != "subscription denied" {
		t.Fatalf("ack = %+v", ack)
	}
	expectClose(t, conn, websocket.StatusPolicyViolation)
}

// TestAttachMirrorHeaderMapsToReadOnly: write:false must become
// ReadOnly:true toward the backend, with default geometry filled in, and
// mirror input must never reach the terminal.
func TestAttachMirrorHeaderMapsToReadOnly(t *testing.T) {
	term := newWSStubTerminal(io.EOF)
	b := &wsStubBackend{attachTerm: term, attachAck: protocol.AttachResponse{OK: true, Cols: 80, Rows: 24}}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/attach/run-1", g.Token())

	writeWSJSON(t, conn, protocol.DashAttachRequest{Write: false})
	if ack := readWSJSON[protocol.AttachResponse](t, conn); !ack.OK {
		t.Fatalf("ack = %+v, want ok", ack)
	}
	req := b.recordedAttach()
	if req.RunID != "run-1" || !req.ReadOnly || req.Cols != 80 || req.Rows != 24 {
		t.Fatalf("attach request = %+v, want run-1 read-only 80x24", req)
	}

	writeWSJSON(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "rm -rf\n"})
	writeWSJSON(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachResize, Cols: 132, Rows: 43})
	term.finish()
	expectClose(t, conn, websocket.StatusNormalClosure)
	select {
	case in := <-term.inputCh:
		t.Fatalf("mirror input reached terminal: %q", in)
	default:
	}
	select {
	case rs := <-term.resizeCh:
		t.Fatalf("mirror resize reached terminal: %v", rs)
	default:
	}
}

// TestAttachForwardsOutputInputAndResize covers a write attach end to end:
// terminal output as binary frames, input and resize control frames
// forwarded to the terminal, clean EOF as 1000.
func TestAttachForwardsOutputInputAndResize(t *testing.T) {
	term := newWSStubTerminal(io.EOF)
	b := &wsStubBackend{attachTerm: term, attachAck: protocol.AttachResponse{OK: true, Cols: 100, Rows: 40}}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/attach/run-1", g.Token())

	writeWSJSON(t, conn, protocol.DashAttachRequest{Write: true, Cols: 100, Rows: 40})
	if ack := readWSJSON[protocol.AttachResponse](t, conn); !ack.OK || ack.Cols != 100 || ack.Rows != 40 {
		t.Fatalf("ack = %+v", ack)
	}
	if req := b.recordedAttach(); req.ReadOnly || req.Cols != 100 || req.Rows != 40 {
		t.Fatalf("attach request = %+v, want writable 100x40", req)
	}

	term.emit([]byte("hello"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	typ, data, err := conn.Read(ctx)
	cancel()
	if err != nil || typ != websocket.MessageBinary || string(data) != "hello" {
		t.Fatalf("output frame = %v %q (%v), want binary hello", typ, data, err)
	}

	writeWSJSON(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "ls\n"})
	select {
	case in := <-term.inputCh:
		if string(in) != "ls\n" {
			t.Fatalf("input = %q, want ls\\n", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input never reached terminal")
	}

	writeWSJSON(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachResize, Cols: 132, Rows: 43})
	select {
	case rs := <-term.resizeCh:
		if rs != [2]uint{132, 43} {
			t.Fatalf("resize = %v, want [132 43]", rs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resize never reached terminal")
	}

	term.finish()
	expectClose(t, conn, websocket.StatusNormalClosure)
}

// TestAttachRefusalForwardsAck: a refused attach forwards the backend's
// ack frame verbatim, then closes 1008.
func TestAttachRefusalForwardsAck(t *testing.T) {
	b := &wsStubBackend{
		attachAck: protocol.AttachResponse{OK: false, Code: protocol.CodeNotFound, Error: "run not found"},
		attachErr: errors.New("cli: attach: run not found"),
	}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/attach/run-x", g.Token())

	writeWSJSON(t, conn, protocol.DashAttachRequest{Write: true})
	ack := readWSJSON[protocol.AttachResponse](t, conn)
	if ack.OK || ack.Code != protocol.CodeNotFound || ack.Error != "run not found" {
		t.Fatalf("ack = %+v", ack)
	}
	if reason := expectClose(t, conn, websocket.StatusPolicyViolation); reason != "attach refused" {
		t.Fatalf("close reason = %q, want attach refused", reason)
	}
}

// TestShellCleanExit covers the shell happy path: valid header, ack echo,
// output, input, resize honored, and clean EOF as 1000 "shell exited".
func TestShellCleanExit(t *testing.T) {
	term := newWSStubTerminal(io.EOF)
	b := &wsStubBackend{shellTerm: term, shellAck: protocol.WorkspaceShellResponse{OK: true, Cols: 90, Rows: 30}}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/shell", g.Token())

	writeWSJSON(t, conn, protocol.WorkspaceShellRequest{
		Workspace: protocol.WorkspaceSelector{Name: "dev"},
		Mode:      protocol.WorkspaceShellBootstrapTools,
		Cols:      90, Rows: 30,
	})
	if ack := readWSJSON[protocol.WorkspaceShellResponse](t, conn); !ack.OK || ack.Cols != 90 {
		t.Fatalf("ack = %+v", ack)
	}

	writeWSJSON(t, conn, protocol.DashAttachControl{Type: protocol.DashAttachResize, Cols: 132, Rows: 43})
	select {
	case rs := <-term.resizeCh:
		if rs != [2]uint{132, 43} {
			t.Fatalf("resize = %v, want [132 43]", rs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resize never reached terminal")
	}

	term.finish()
	if reason := expectClose(t, conn, websocket.StatusNormalClosure); reason != "shell exited" {
		t.Fatalf("close reason = %q, want shell exited", reason)
	}
}

// TestShellDirtyExitCloses4001: a terminal read error carrying the remote
// exit status closes 4001 with the error text as the reason.
func TestShellDirtyExitCloses4001(t *testing.T) {
	term := newWSStubTerminal(errors.New("cli: remote exited with status 3"))
	b := &wsStubBackend{shellTerm: term, shellAck: protocol.WorkspaceShellResponse{OK: true}}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/shell", g.Token())

	writeWSJSON(t, conn, protocol.WorkspaceShellRequest{
		Workspace: protocol.WorkspaceSelector{Name: "dev"},
		Mode:      protocol.WorkspaceShellBootstrapTools,
	})
	if ack := readWSJSON[protocol.WorkspaceShellResponse](t, conn); !ack.OK {
		t.Fatalf("ack = %+v", ack)
	}

	term.emit([]byte("boom")) // some output before the dirty end
	term.finish()
	reason := expectClose(t, conn, websocket.StatusCode(statusDirtyExit))
	if !strings.Contains(reason, "status 3") {
		t.Fatalf("close reason = %q, want remote exit status", reason)
	}
}

// TestShellInvalidHeaderRefusedWithInvalidParams: a header that fails
// Validate is refused with -32602 before the backend is dialed.
func TestShellInvalidHeaderRefusedWithInvalidParams(t *testing.T) {
	b := &wsStubBackend{}
	g, base := newWSGateway(t, b)
	conn := wsDial(t, base, "/ws/shell", g.Token())

	// No workspace selector: Validate fails.
	writeWSJSON(t, conn, protocol.WorkspaceShellRequest{Mode: protocol.WorkspaceShellBootstrapTools})
	ack := readWSJSON[protocol.WorkspaceShellResponse](t, conn)
	if ack.OK || ack.Code != protocol.CodeInvalidParams {
		t.Fatalf("ack = %+v, want code %d", ack, protocol.CodeInvalidParams)
	}
	expectClose(t, conn, websocket.StatusPolicyViolation)
}
