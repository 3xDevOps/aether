package webgate

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// silentStream is an events stream that never produces a line; Close
// reports that the handler released it.
type silentStream struct {
	*io.PipeReader
	closed chan struct{}
}

type silentBackend struct {
	stubBackend
	stream *silentStream
}

func (b *silentBackend) Events(context.Context, protocol.SubscribeRequest) (io.ReadCloser, error) {
	return b.stream, nil
}

// A peer that stops reading never answers pings. The keepalive must end
// the socket and, through it, the handler and the stream it holds -
// which is what stops a vanished phone from holding a PTY client open.
func TestKeepAliveDropsAPeerThatStopsAnsweringPings(t *testing.T) {
	interval, timeout := pingInterval, pingTimeout
	pingInterval, pingTimeout = 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { pingInterval, pingTimeout = interval, timeout })

	pr, pw := io.Pipe()
	stream := &silentStream{PipeReader: pr, closed: make(chan struct{})}
	backend := &silentBackend{stream: stream}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	defer func() { _ = pw.Close() }()
	ts := httptest.NewServer(g)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := wsjson.Write(ctx, conn, protocol.SubscribeRequest{}); err != nil {
		t.Fatal(err)
	}
	var ack protocol.SubscribeResponse
	if err := wsjson.Read(ctx, conn, &ack); err != nil || !ack.OK {
		t.Fatalf("ack = %+v (%v)", ack, err)
	}
	// No further Read: the client answers no pong from here on.
	select {
	case <-stream.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler kept its stream open although the peer answered no ping")
	}
}

func (s *silentStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return s.PipeReader.Close()
}
