package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCaptureRejectsWrongTargetAndImageGeometry(t *testing.T) {
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		page  string
		width int
	}{{"wrong-page", "other", 2}, {"wrong-width", "page", 3}} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				metadata, err := json.Marshal(Metadata{Page: Page{SessionID: "session", PageID: test.page, PageRevision: 1, Width: test.width, Height: 3}, ContentType: "image/png"})
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("X-Aether-Metadata", base64.RawURLEncoding.EncodeToString(metadata))
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(imageBytes.Bytes())
			}))
			defer server.Close()
			client := NewClient("unused")
			defer client.Close()
			transport := server.Client().Transport
			client.http.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
				copy := request.Clone(request.Context())
				copy.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return transport.RoundTrip(copy)
			})
			capture, err := client.Capture(t.Context(), Request{SessionID: "session", PageID: "page", PageRevision: 1})
			if err == nil || capture.Bytes != nil {
				t.Fatalf("accepted mismatched image: %+v, %v", capture.Metadata, err)
			}
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTypedOperationLimits(t *testing.T) {
	base := Request{SessionID: "session", PageID: "page", PageRevision: 1, ViewportID: "viewport"}
	for _, test := range []struct {
		name string
		edit func(*Request)
	}{
		{"unknown-operation", func(r *Request) { r.Operation = "evaluate" }},
		{"private-cleanup-not-action", func(r *Request) { r.Operation = "release_input" }},
		{"unbounded-timeout", func(r *Request) { r.Operation = "reload"; r.TimeoutMS = 30001 }},
		{"oversized-snapshot", func(r *Request) { r.Operation = "snapshot"; r.MaxNodes = 129 }},
		{"stale-node-identity", func(r *Request) { r.Operation = "click" }},
		{"nonfinite-scroll", func(r *Request) { r.Operation = "scroll"; r.DeltaY = math.Inf(1) }},
		{"empty-success-condition", func(r *Request) { r.Operation = "wait"; r.Condition = "text" }},
		{"file-url", func(r *Request) { r.Operation = "navigate"; r.URL = "file:///etc/passwd" }},
		{"unsafe-integer-cursor", func(r *Request) { r.Operation = "console"; r.After = 1 << 53 }},
		{"open-oversized-empty-url", func(r *Request) { r.Operation = "open"; r.Width = 2561 }},
		{"unknown-modifier", func(r *Request) { r.Operation = "key"; r.Key = "a"; r.Modifiers = []string{"Hyper"} }},
		{"pointer-modifiers-not-silent", func(r *Request) { r.Operation = "pointer"; r.Action = "click"; r.Modifiers = []string{"Shift"} }},
		{"keydown-modifiers-not-silent", func(r *Request) {
			r.Operation = "key"
			r.Action = "down"
			r.Key = "a"
			r.Modifiers = []string{"Control"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.edit(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("accepted invalid typed operation")
			}
		})
	}
	request := base
	request.Operation, request.MaxNodes, request.MaxChars = "snapshot", 128, 8192
	if err := request.Validate(); err != nil {
		t.Fatalf("rejected supported snapshot boundary: %v", err)
	}
	request = base
	request.Operation, request.Condition, request.Text, request.TimeoutMS = "wait", "text", "ready", 30000
	if err := request.Validate(); err != nil {
		t.Fatalf("rejected declared public wait ceiling: %v", err)
	}
}

func TestStalledCompanionIsATimeout(t *testing.T) {
	client := NewClient("unused")
	defer client.Close()
	client.http.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := client.Health(ctx)
	var browserErr *Error
	if !errors.As(err, &browserErr) || browserErr.Code != "timeout" {
		t.Fatalf("stalled companion = %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Health(cancelled); errors.As(err, &browserErr) {
		t.Fatalf("caller cancellation reported as %v", err)
	}
}
