package edgeagent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
)

// An http open carries no grant. Its data socket serves request after
// request to Config.HTTP, and its slot is free once it closes.
func TestHTTPOpenServesRequestsOnOneConnection(t *testing.T) {
	var served atomic.Int32
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// A handler that takes a moment lets net/http start its
		// background read, which it then aborts with a past deadline.
		time.Sleep(20 * time.Millisecond)
		_, _ = fmt.Fprintf(w, "%d %s %s %s", served.Add(1), r.Method, r.URL.RequestURI(), body)
	})
	edge, _, a, ec := enrolledWith(t, mcp)
	connID := edgeproto.NewConnID()
	edge.openKind(ec, edgeproto.KindHTTP, connID, "")
	if r := expect[edgeproto.OpenResult](t, ec); r.ConnID != connID || r.Error != "" {
		t.Fatalf("http open result %+v, want success", r)
	}
	nc := websocket.NetConn(context.Background(), edge.nextData(t), websocket.MessageBinary)
	_ = nc.SetDeadline(time.Now().Add(waitFor))
	br := bufio.NewReader(nc)
	for i, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/mcp/x?a=1&b=two%20words", `{"jsonrpc":"2.0"}`},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp/x", ""},
		{http.MethodPost, "/mcp/x/token", "grant_type=refresh_token"},
	} {
		req, err := http.NewRequest(tc.method, "http://edge.example.test"+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if err = req.Write(nc); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			t.Fatalf("request %d on the same connection: %v", i+1, err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if want := fmt.Sprintf("%d %s %s %s", i+1, tc.method, tc.path, tc.body); string(got) != want {
			t.Fatalf("request %d answered %q, want %q", i+1, got, want)
		}
	}
	_ = nc.Close()
	deadline := time.Now().Add(waitFor)
	for len(a.httpSlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the closed http connection still holds its slot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A server with no connector handler refuses an http open with the
// reason, and attaches nothing.
func TestHTTPOpenWithoutHandlerIsRefused(t *testing.T) {
	edge, _, _, ec := enrolled(t)
	connID := edgeproto.NewConnID()
	edge.openKind(ec, edgeproto.KindHTTP, connID, "")
	if r := expect[edgeproto.OpenResult](t, ec); r.ConnID != connID || r.Error != "this server does not serve MCP connectors" {
		t.Fatalf("http open result %+v, want the refusal", r)
	}
	select {
	case c := <-edge.data:
		_ = c.CloseNow()
		t.Fatal("server attached a data socket for an http open it refused")
	case <-time.After(100 * time.Millisecond):
	}
}

// Full http slots refuse the next http open and leave SSH opens alone.
func TestHTTPConnectionLimit(t *testing.T) {
	edge, _, a, ec := enrolledWith(t, http.NotFoundHandler())
	for range edgeproto.MaxHTTPConnsPerServer {
		a.httpSlots <- struct{}{}
	}
	connID := edgeproto.NewConnID()
	edge.openKind(ec, edgeproto.KindHTTP, connID, "")
	if r := expect[edgeproto.OpenResult](t, ec); r.Error != string(edgeproto.RefusalConnLimit) {
		t.Fatalf("http open beyond the limit: %+v", r)
	}
	connID = edgeproto.NewConnID()
	edge.open(ec, connID, edge.grant(t, sshGrant(t, a, connID)))
	if r := expect[edgeproto.OpenResult](t, ec); r.Error != "" {
		t.Fatalf("ssh open with the http slots full: %+v", r)
	}
	data := edge.nextData(t)
	t.Cleanup(func() { _ = data.CloseNow() })
}
