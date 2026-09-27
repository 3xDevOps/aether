package webgate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func developmentFrame(sequence uint64, run string) protocol.DevBrowserFrame {
	return protocol.DevBrowserFrame{Metadata: protocol.DevBrowserFrameMetadata{RunID: run, SessionID: "session", PageID: "page", PageRevision: 3, ViewportID: "viewport", Width: 800, Height: 600, Sequence: sequence, MIMEType: "image/jpeg"}, Data: []byte{0xff, 0xd8, byte(sequence)}}
}
func developmentRequest(run string) protocol.DevBrowserStreamRequest {
	return protocol.DevBrowserStreamRequest{DevBrowserPageTarget: protocol.DevBrowserPageTarget{DevBrowserTarget: protocol.DevBrowserTarget{DevRunParams: protocol.DevRunParams{RunID: run}, SessionID: "session"}, PageID: "page", PageRevision: 3}}
}

func TestDevelopmentFramesReplaceQueuedImagesForSlowReader(t *testing.T) {
	var encoded bytes.Buffer
	for sequence := uint64(1); sequence <= 64; sequence++ {
		if err := protocol.WriteDevBrowserFrame(&encoded, developmentFrame(sequence, "run")); err != nil {
			t.Fatal(err)
		}
	}
	frames, done := latestDevelopmentFrames(t.Context(), &encoded, developmentRequest("run"))
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("frame producer blocked behind a slow viewer")
	}
	frame := <-frames
	if frame.Metadata.Sequence != 64 || !bytes.Equal(frame.Data, []byte{0xff, 0xd8, 64}) {
		t.Fatalf("queued frame = %+v", frame)
	}
	select {
	case extra := <-frames:
		t.Fatalf("obsolete frame retained: %+v", extra.Metadata)
	default:
	}
}

func TestDevelopmentFramesRejectCrossRunPayload(t *testing.T) {
	var encoded bytes.Buffer
	if err := protocol.WriteDevBrowserFrame(&encoded, developmentFrame(1, "other")); err != nil {
		t.Fatal(err)
	}
	frames, done := latestDevelopmentFrames(t.Context(), &encoded, developmentRequest("run"))
	var perr *protocol.Error
	if err := <-done; !errors.As(err, &perr) || perr.Code != protocol.CodeDenied {
		t.Fatalf("cross-run frame = %v", err)
	}
	select {
	case <-frames:
		t.Fatal("cross-run image escaped identity fence")
	default:
	}
}

type developmentRouteBackend struct {
	stubBackend
	source   io.ReadCloser
	artifact protocol.DevArtifact
}

func (b *developmentRouteBackend) BrowserFrames(context.Context, protocol.DevBrowserStreamRequest) (io.ReadCloser, error) {
	return b.source, nil
}
func (b *developmentRouteBackend) Artifact(context.Context, protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	return b.source, b.artifact, nil
}

func TestDevelopmentBrowserRouteClosesOnBackendRevocation(t *testing.T) {
	reader, writer := io.Pipe()
	backend := &developmentRouteBackend{source: reader}
	g, gateErr := New(Config{Authorize: admitAll(backend)})
	if gateErr != nil {
		t.Fatal(gateErr)
	}
	defer func() { _ = g.Close() }()
	server := httptest.NewServer(g)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, dialErr := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/dev/browser/run", nil)
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := wsjson.Write(ctx, conn, developmentRequest("run")); err != nil {
		t.Fatal(err)
	}
	var ack protocol.DevStreamResponse
	if err := wsjson.Read(ctx, conn, &ack); err != nil || !ack.OK {
		t.Fatalf("ack = %+v: %v", ack, err)
	}
	go func() { _ = protocol.WriteDevBrowserFrame(writer, developmentFrame(9, "run")) }()
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		t.Fatalf("frame = %v: %v", typ, err)
	}
	frame, err := protocol.ReadDevBrowserFrame(bytes.NewReader(data))
	if err != nil || frame.Metadata.Sequence != 9 || frame.Metadata.ViewportID != "viewport" {
		t.Fatalf("frame metadata = %+v: %v", frame.Metadata, err)
	}
	_ = writer.CloseWithError(&protocol.RemoteExitError{Status: protocol.AttachExitSteerRevoked})
	if err := wsjson.Read(ctx, conn, &ack); err != nil || ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("revoke = %+v: %v", ack, err)
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("revoke close = %v", err)
	}
}

func TestDevelopmentRoutesRejectForeignOriginAndArtifactRun(t *testing.T) {
	backend := &developmentRouteBackend{source: io.NopCloser(strings.NewReader("secret")), artifact: protocol.DevArtifact{ID: "capture", RunID: "other", Bytes: 6}}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	for _, path := range []string{"/ws/dev/browser/run", "/api/v1/dev/run/artifacts/capture"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Origin", "https://foreign.invalid")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("foreign %s = %d", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/dev/run/artifacts/capture", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("cross-run capture = %d %s", w.Code, w.Body.String())
	}
}
