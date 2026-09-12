package webgate

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// followTerminal exposes an ordered framed terminal stream, including a
// geometry record between two output records.
type followTerminal struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	once sync.Once
}

func newFollowTerminal() *followTerminal {
	r, w := io.Pipe()
	return &followTerminal{r: r, w: w}
}

func (t *followTerminal) Read(p []byte) (int, error)  { return t.r.Read(p) }
func (t *followTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (t *followTerminal) Resize(uint, uint) error     { return nil }

func (t *followTerminal) Close() error {
	t.once.Do(func() {
		_ = t.r.Close()
		_ = t.w.Close()
	})
	return nil
}

type followBackend struct {
	stubBackend
	term    *followTerminal
	request protocol.AttachRequest
}

func (b *followBackend) Attach(_ context.Context, req protocol.AttachRequest) (Terminal, protocol.AttachResponse, error) {
	b.request = req
	return b.term, protocol.AttachResponse{OK: true, Framed: true, Cols: 132, Rows: 43}, nil
}

// The browser half of the framed follow contract: output and geometry records
// preserve their order, and the dashboard request enables framing.
func TestAttachRelaysOrderedTerminalRecordsToBrowser(t *testing.T) {
	term := newFollowTerminal()
	backend := &followBackend{term: term}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	ts := httptest.NewServer(g)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/attach/run_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err = wsjson.Write(ctx, conn, protocol.DashAttachRequest{
		Write: true, Cols: 80, Rows: 24, Follow: true,
	}); err != nil {
		t.Fatal(err)
	}

	var ack protocol.AttachResponse
	if err = wsjson.Read(ctx, conn, &ack); err != nil {
		t.Fatal(err)
	}
	if !ack.OK || ack.Cols != 132 || ack.Rows != 43 {
		t.Fatalf("ack = %+v, want the session's 132x43", ack)
	}
	if !backend.request.Follow || backend.request.ReadOnly || !backend.request.Framed {
		t.Fatalf("attach request = %+v, want a writable framed follower", backend.request)
	}

	go func() {
		_, _ = protocol.WriteTerminalOutput(term.w, []byte("old"))
		_ = protocol.WriteTerminalGeometry(term.w, 120, 40)
		_, _ = protocol.WriteTerminalOutput(term.w, []byte("new"))
		_ = term.w.Close()
	}()

	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(data) != "old" {
		t.Fatalf("first frame = (%v,%q), want binary old output", typ, data)
	}
	var frame protocol.DashAttachControl
	if err = wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != protocol.DashAttachGeometry || frame.Cols != 120 || frame.Rows != 40 {
		t.Fatalf("frame = %+v, want a geometry frame of 120x40", frame)
	}
	typ, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(data) != "new" {
		t.Fatalf("last frame = (%v,%q), want binary new output", typ, data)
	}
}
