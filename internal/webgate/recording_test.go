package webgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type recordingTestTerminal struct {
	mu     sync.Mutex
	body   io.Reader
	closed bool
}

func (t *recordingTestTerminal) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, io.EOF
	}
	return t.body.Read(p)
}
func (t *recordingTestTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (t *recordingTestTerminal) Resize(uint, uint) error     { return nil }
func (t *recordingTestTerminal) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return nil
}
func (t *recordingTestTerminal) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

type recordingTestBackend struct {
	stubBackend
	term *recordingTestTerminal
	req  protocol.AttachRequest
	ack  protocol.AttachResponse
}

func (b *recordingTestBackend) Attach(_ context.Context, req protocol.AttachRequest) (Terminal, protocol.AttachResponse, error) {
	b.req = req
	return b.term, b.ack, nil
}

func TestRecordingRouteStreamsRawCast(t *testing.T) {
	body := []byte("{\"version\":2}\n[0,\"o\",\"history\"]\n")
	backend := &recordingTestBackend{
		term: &recordingTestTerminal{body: bytes.NewReader(body)},
		ack:  protocol.AttachResponse{OK: true, Recording: true},
	}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/run/run-1/recording", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Fatalf("recording response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != recordingContentType || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("recording headers = %v", rec.Header())
	}
}

func TestRecordingRouteRejectsUnauthorisedCallerBeforeBackend(t *testing.T) {
	g, err := New(Config{Authorize: func(*http.Request, bool) (Backend, *Refusal) {
		return nil, &Refusal{Status: http.StatusForbidden, Error: &protocol.Error{Code: protocol.CodeDenied, Message: "not a member"}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/run/run-1/recording", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthorised recording status = %d", rec.Code)
	}
	var out ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error == nil || out.Error.Code != protocol.CodeDenied {
		t.Fatalf("unauthorised recording body = %s", rec.Body)
	}
}

func TestRecordingRouteRejectsOldServerAcknowledgement(t *testing.T) {
	term := &recordingTestTerminal{body: bytes.NewReader([]byte("live stream"))}
	backend := &recordingTestBackend{term: term, ack: protocol.AttachResponse{OK: true}}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/run/run-1/recording", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("old recording acknowledgement status = %d", rec.Code)
	}
	if !term.isClosed() {
		t.Fatal("old recording acknowledgement left the stream open")
	}
	var out ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error == nil || out.Error.Code != protocol.CodeInternal {
		t.Fatalf("old recording acknowledgement body = %s", rec.Body)
	}
}

type brokenRecording struct{}

func (brokenRecording) Read([]byte) (int, error) {
	return 0, errors.New("recording read failed")
}

func TestRecordingStreamFailureDoesNotReportTruncatedSuccess(t *testing.T) {
	prefix := bytes.Repeat([]byte("[0,\"o\",\"recorded output\"]\n"), 4096)
	backend := &recordingTestBackend{
		term: &recordingTestTerminal{body: io.MultiReader(bytes.NewReader(prefix), brokenRecording{})},
		ack:  protocol.AttachResponse{OK: true, Recording: true},
	}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	server := httptest.NewServer(g)
	defer server.Close()
	resp, err := server.Client().Get(server.URL + "/api/v1/run/run-1/recording")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("truncated recording was presented as a complete response")
	}
}
