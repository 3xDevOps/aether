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

// followTerminal is an attach that follows its session: it never produces
// output, and reports the sizes the session takes the way both real
// backends do.
type followTerminal struct {
	sizes chan [2]uint
	done  chan struct{}
	once  sync.Once
}

func (t *followTerminal) Read([]byte) (int, error) {
	<-t.done
	return 0, io.EOF
}
func (t *followTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (t *followTerminal) Resize(uint, uint) error     { return nil }

// Closed by both pump loops, as the real ones are.
func (t *followTerminal) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}
func (t *followTerminal) Geometry() <-chan [2]uint { return t.sizes }

type followBackend struct {
	stubBackend
	term    *followTerminal
	request protocol.AttachRequest
}

func (b *followBackend) Attach(_ context.Context, req protocol.AttachRequest) (Terminal, protocol.AttachResponse, error) {
	b.request = req
	return b.term, protocol.AttachResponse{OK: true, Cols: 132, Rows: 43}, nil
}

// The browser half of the follow contract: the header's flag reaches the
// server, the ack's geometry reaches the page, and a resize of the session
// arrives as a geometry frame rather than as bytes in the terminal stream.
func TestAttachRelaysTheSessionGeometryToTheBrowser(t *testing.T) {
	term := &followTerminal{sizes: make(chan [2]uint, 1), done: make(chan struct{})}
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
	if err := wsjson.Write(ctx, conn, protocol.DashAttachRequest{
		Write: true, Cols: 80, Rows: 24, Follow: true,
	}); err != nil {
		t.Fatal(err)
	}

	var ack protocol.AttachResponse
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		t.Fatal(err)
	}
	if !ack.OK || ack.Cols != 132 || ack.Rows != 43 {
		t.Fatalf("ack = %+v, want the session's 132x43", ack)
	}
	if !backend.request.Follow || backend.request.ReadOnly {
		t.Fatalf("attach request = %+v, want a writable follower", backend.request)
	}

	term.sizes <- [2]uint{120, 40}
	var frame protocol.DashAttachControl
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != protocol.DashAttachGeometry || frame.Cols != 120 || frame.Rows != 40 {
		t.Fatalf("frame = %+v, want a geometry frame of 120x40", frame)
	}
}
