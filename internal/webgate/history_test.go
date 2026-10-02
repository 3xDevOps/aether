package webgate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type historyBackend struct {
	mu          sync.Mutex
	attachReq   protocol.AttachRequest
	attachTerm  Terminal
	attachAck   protocol.AttachResponse
	attachErr   error
	attachReady chan struct{}
}

func (b *historyBackend) Call(context.Context, string, json.RawMessage) (json.RawMessage, *protocol.Error) {
	return nil, nil
}
func (b *historyBackend) Events(context.Context, protocol.SubscribeRequest) (io.ReadCloser, error) {
	return nil, nil
}
func (b *historyBackend) Attach(_ context.Context, req protocol.AttachRequest) (Terminal, protocol.AttachResponse, error) {
	b.mu.Lock()
	b.attachReq = req
	if b.attachReady != nil {
		close(b.attachReady)
		b.attachReady = nil
	}
	term, ack, err := b.attachTerm, b.attachAck, b.attachErr
	b.mu.Unlock()
	return term, ack, err
}
func (b *historyBackend) Terminal(context.Context, protocol.TerminalRequest) (Terminal, protocol.TerminalResponse, error) {
	return nil, protocol.TerminalResponse{}, nil
}

type historyTerminal struct {
	reader io.Reader
	closed chan struct{}
	once   sync.Once
}

func newHistoryTerminal(reader io.Reader) *historyTerminal {
	return &historyTerminal{reader: reader, closed: make(chan struct{})}
}
func (t *historyTerminal) Read(p []byte) (int, error) {
	if t.reader != nil {
		return t.reader.Read(p)
	}
	<-t.closed
	return 0, io.EOF
}
func (t *historyTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (t *historyTerminal) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}
func (t *historyTerminal) Resize(uint, uint) error { return nil }

func TestHistoryRequiresAuthorization(t *testing.T) {
	backend := &historyBackend{}
	g, err := New(Config{Authorize: func(*http.Request, bool) (Backend, *Refusal) {
		return nil, &Refusal{
			Status: http.StatusForbidden,
			Error:  &protocol.Error{Code: protocol.CodeDenied, Message: "history denied"},
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-secret/terminal-history", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("history refusal status = %d, want 403", rec.Code)
	}
	if backend.attachReq.RunID != "" {
		t.Fatal("unauthorized history request reached backend")
	}
}

func TestHistoryFormUsesBoundedBearerBody(t *testing.T) {
	term := newHistoryTerminal(bytes.NewReader(append([]byte{'o', 0, 0, 0, 2}, []byte("ok")...)))
	backend := &historyBackend{
		attachTerm: term,
		attachAck:  protocol.AttachResponse{OK: true, Framed: true, Replay: 2},
	}
	var authorization string
	g, err := New(Config{Authorize: func(r *http.Request, _ bool) (Backend, *Refusal) {
		authorization = r.Header.Get("Authorization")
		return backend, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/runs/run-form/terminal-history",
		strings.NewReader("token=secret"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("form history response = %d %q", rec.Code, rec.Body.String())
	}
	if authorization != "Bearer secret" {
		t.Fatalf("form authorization = %q", authorization)
	}
}

func TestHistoryStreamsOnlyAcknowledgedReplay(t *testing.T) {
	var wire bytes.Buffer
	if err := protocol.WriteTerminalGeometry(&wire, 120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.WriteTerminalOutput(&wire, []byte("before")); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteTerminalGeometry(&wire, 80, 24); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.WriteTerminalOutput(&wire, []byte("live-tail")); err != nil {
		t.Fatal(err)
	}
	term := newHistoryTerminal(bytes.NewReader(wire.Bytes()))
	backend := &historyBackend{
		attachTerm: term,
		attachAck: protocol.AttachResponse{
			OK: true, Framed: true, Replay: len("before"),
		},
	}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-1/terminal-history", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "before" {
		t.Fatalf("history body = %q, want acknowledged replay only", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="terminal-history-run-1.ansi"` {
		t.Fatalf("content disposition = %q", got)
	}
	backend.mu.Lock()
	got := backend.attachReq
	backend.mu.Unlock()
	if !got.ReadOnly || !got.Follow || !got.Framed || got.Screen {
		t.Fatalf("history attach request = %+v", got)
	}
	select {
	case <-term.closed:
	default:
		t.Fatal("history stream did not close at replay boundary")
	}
}

func TestHistoryCancellationClosesAttachment(t *testing.T) {
	term := newHistoryTerminal(nil)
	ready := make(chan struct{})
	backend := &historyBackend{
		attachTerm:  term,
		attachAck:   protocol.AttachResponse{OK: true, Framed: true, Replay: 1},
		attachReady: ready,
	}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-live/terminal-history", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		g.ServeHTTP(rec, req)
		close(done)
	}()
	<-ready
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("history handler did not stop after cancellation")
	}
	select {
	case <-term.closed:
	default:
		t.Fatal("history cancellation did not close attachment")
	}
}
