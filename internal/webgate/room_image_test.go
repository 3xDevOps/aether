package webgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const roomImageTestPath = "/api/v1/run/run/messages/message/attachments/0"

type roomImageRouteBackend struct {
	developmentRouteBackend
	request protocol.DevArtifactDownloadRequest
	calls   int
	err     error
}

func (b *roomImageRouteBackend) Artifact(ctx context.Context, req protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	b.calls++
	b.request = req
	if b.err != nil {
		return nil, protocol.DevArtifact{}, b.err
	}
	return b.developmentRouteBackend.Artifact(ctx, req)
}

type roomImageReadCloser struct {
	*bytes.Reader
	closed bool
}

func (r *roomImageReadCloser) Close() error {
	r.closed = true
	return nil
}

func roomImageFixture(t *testing.T) (*roomImageRouteBackend, *roomImageReadCloser, []byte) {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	source := &roomImageReadCloser{Reader: bytes.NewReader(data)}
	backend := &roomImageRouteBackend{developmentRouteBackend: developmentRouteBackend{
		source:   source,
		artifact: protocol.DevArtifact{ID: "room:message:0", RunID: "run", ContentType: "image/png", Bytes: int64(len(data))},
	}}
	return backend, source, data
}

func roomImageGateway(t *testing.T, authorize Authorizer) *Gateway {
	t.Helper()
	g, err := New(Config{Authorize: authorize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func requireRoomImageError(t *testing.T, w *httptest.ResponseRecorder, status, code int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), status)
	}
	var body ErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not JSON: %v", err)
	}
	if body.Error == nil || body.Error.Code != code {
		t.Fatalf("error=%+v, want code %d", body.Error, code)
	}
}

func TestRoomImageRouteServesBoundedBinaryWithSafeHeaders(t *testing.T) {
	backend, source, data := roomImageFixture(t)
	// Extra stream bytes must never escape the declared artifact boundary.
	source.Reader = bytes.NewReader(append(append([]byte(nil), data...), []byte("private trailing data")...))
	g := roomImageGateway(t, admitAll(backend))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, roomImageTestPath, nil))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("response status=%d body bytes=%d, want exact %d-byte PNG", w.Code, w.Body.Len(), len(data))
	}
	if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatalf("downloaded image cannot be decoded: %v", err)
	}
	for name, want := range map[string]string{
		"Content-Type":            "image/png",
		"Content-Length":          strconv.Itoa(len(data)),
		"Content-Disposition":     `attachment; filename="capture"`,
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
	if !source.closed {
		t.Fatal("image stream was not closed")
	}
	if backend.calls != 1 || backend.request.RunID != "run" || backend.request.RoomMessageID != "message" || backend.request.ArtifactID != "room:message:0" || backend.request.AttachmentIndex == nil || *backend.request.AttachmentIndex != 0 || backend.request.EvidencePacketID != "" {
		t.Fatalf("image request=%+v calls=%d", backend.request, backend.calls)
	}
}

func TestRoomImageRouteRejectsUnauthenticatedRequest(t *testing.T) {
	backend, _, _ := roomImageFixture(t)
	g := roomImageGateway(t, func(*http.Request, bool) (Backend, *Refusal) {
		return backend, &Refusal{Status: http.StatusUnauthorized, Error: &protocol.Error{Code: protocol.CodeDenied, Message: "authentication required"}}
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, roomImageTestPath, nil))
	requireRoomImageError(t, w, http.StatusUnauthorized, protocol.CodeDenied)
	if w.Header().Get("WWW-Authenticate") != "Bearer" || backend.calls != 0 {
		t.Fatalf("unauthenticated response headers=%v backend calls=%d", w.Header(), backend.calls)
	}
}

func TestRoomImageRouteRejectsInvalidSelectorsBeforeOpeningStream(t *testing.T) {
	for _, selector := range []string{"-1", "not-an-index", "999999999999999999999999999999", "0?evidence_packet_id=packet"} {
		t.Run(selector, func(t *testing.T) {
			backend, _, _ := roomImageFixture(t)
			g := roomImageGateway(t, admitAll(backend))
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/run/run/messages/message/attachments/"+selector, nil))
			requireRoomImageError(t, w, http.StatusBadRequest, protocol.CodeInvalidParams)
			if backend.calls != 0 {
				t.Fatal("invalid selector opened image stream")
			}
		})
	}
}

func TestRoomImageRouteRejectsMismatchedOrUnsafeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*protocol.DevArtifact)
	}{
		{"foreign run", func(a *protocol.DevArtifact) { a.RunID = "other" }},
		{"foreign message", func(a *protocol.DevArtifact) { a.ID = "room:other:0" }},
		{"foreign index", func(a *protocol.DevArtifact) { a.ID = "room:message:1" }},
		{"zero size", func(a *protocol.DevArtifact) { a.Bytes = 0 }},
		{"negative size", func(a *protocol.DevArtifact) { a.Bytes = -1 }},
		{"oversized", func(a *protocol.DevArtifact) { a.Bytes = domain.MaxImageBytes + 1 }},
		{"active content", func(a *protocol.DevArtifact) { a.ContentType = "image/svg+xml" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, source, data := roomImageFixture(t)
			tc.alter(&backend.artifact)
			g := roomImageGateway(t, admitAll(backend))
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, roomImageTestPath, nil))
			requireRoomImageError(t, w, http.StatusForbidden, protocol.CodeDenied)
			if bytes.Contains(w.Body.Bytes(), data) || source.Len() != len(data) || !source.closed {
				t.Fatalf("refused image was read/leaked or not closed: unread=%d closed=%v", source.Len(), source.closed)
			}
		})
	}
}

func TestRoomImageRoutePropagatesTransportAndAuthorizationErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   int
	}{
		{"transport", errors.New("artifact transport unavailable"), http.StatusServiceUnavailable, protocol.CodeUnavailable},
		{"authorization", &protocol.Error{Code: protocol.CodeDenied, Message: "room membership revoked"}, http.StatusForbidden, protocol.CodeDenied},
		{"revoked stream authority", &protocol.RemoteExitError{Status: protocol.AttachExitMembershipRevoked}, http.StatusForbidden, protocol.CodeDenied},
		{"deleted message", &protocol.Error{Code: protocol.CodeNotFound, Message: "room message missing"}, http.StatusNotFound, protocol.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, _, _ := roomImageFixture(t)
			backend.err = tc.err
			g := roomImageGateway(t, admitAll(backend))
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, roomImageTestPath, nil))
			requireRoomImageError(t, w, tc.status, tc.code)
			if backend.calls != 1 || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("failure was not exposed as an error: calls=%d headers=%v", backend.calls, w.Header())
			}
		})
	}
}
