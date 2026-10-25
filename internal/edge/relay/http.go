package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strings"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// connectorCSP sandboxes every forwarded response. The relay origin also
// serves the join page, whose script reads secrets from the URL fragment:
// a page a server serves here runs in an opaque origin, with no script,
// and in no frame.
const connectorCSP = "sandbox allow-forms; default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'"

// responseHeaders are the only response headers forwarded. Anything else,
// such as Set-Cookie or Service-Worker-Allowed, is dropped.
var responseHeaders = map[string]bool{
	"Content-Type": true, "Content-Length": true, "Cache-Control": true, "Location": true,
	"Www-Authenticate": true, "X-Accel-Buffering": true,
}

// forwardHeaders trims h, the request headers ReverseProxy passes on: it
// drops Cookie, any X-Forwarded-* the client sent, and HeaderClientAddr,
// which it sets to addr. ReverseProxy drops hop-by-hop headers but puts
// back Connection, Upgrade and Te; those go too, so no upgrade tunnels
// past responseFilter. Accept-Encoding goes because Content-Encoding does
// not come back.
func forwardHeaders(h http.Header, addr netip.AddrPort) {
	for k := range h {
		if strings.HasPrefix(k, "X-Forwarded-") {
			delete(h, k)
		}
	}
	for _, k := range []string{"Cookie", "Connection", "Upgrade", "Te", "Accept-Encoding", edgeproto.HeaderClientAddr} {
		h.Del(k)
	}
	if addr.IsValid() {
		h.Set(edgeproto.HeaderClientAddr, addr.String())
	}
}

// errHTTPLimit refuses an http connection past MaxHTTPConnsPerServer.
var errHTTPLimit = fmt.Errorf("this server already has %d MCP connector requests in progress through the edge; retry shortly",
	edgeproto.MaxHTTPConnsPerServer)

// noMCPEndpoint refuses an http connection to a server whose protocol
// version predates edgeproto.KindHTTP.
type noMCPEndpoint struct{ name, id, agentVersion string }

func (e noMCPEndpoint) Error() string {
	return fmt.Sprintf("server %s (%s) runs aether-server %s, which has no MCP endpoint; upgrade it: sudo aether-server update",
		e.name, e.id, e.agentVersion)
}

// admitHTTPLocked returns why reg takes no more http connections, if it
// does not.
func (r *Relay) admitHTTPLocked(reg *registration) error {
	if reg.version < edgeproto.VersionHTTP {
		return noMCPEndpoint{name: reg.name, id: reg.id, agentVersion: reg.agentVersion}
	}
	n := 0
	for _, o := range r.conns {
		if o.serverID == reg.id && o.kind == edgeproto.KindHTTP {
			n++
		}
	}
	if n >= edgeproto.MaxHTTPConnsPerServer {
		return errHTTPLimit
	}
	return nil
}

// serveHTTP forwards one request on an MCP path to the server the path
// names, with its path and query unchanged, and streams the answer back.
// The edge vouches for nothing on it: the server authenticates the
// request.
func (r *Relay) serveHTTP(w http.ResponseWriter, req *http.Request) {
	serverID := req.PathValue("server_id")
	if !edgeproto.ValidServerID(serverID) {
		r.refuseHTTP(w, req, serverID, edgeproto.RefusalUnknownServer)
		return
	}
	body, ok := r.ReadMCPBody(w, req)
	if !ok {
		return
	}
	r.forward(w, req, serverID, body)
}

// ReadMCPBody reads a relayed request's body whole, before a connection
// is taken, so a slow client holds none of the server's few. It answers
// the request itself and returns false when the body is too large or
// cannot be read.
func (r *Relay) ReadMCPBody(w http.ResponseWriter, req *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, edgeproto.MaxHTTPBodySize))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		r.countRefusal("MCP request too large")
		http.Error(w, fmt.Sprintf("request body is over the edge's limit of %d bytes", edgeproto.MaxHTTPBodySize),
			http.StatusRequestEntityTooLarge)
		return nil, false
	case err != nil:
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// ForwardUniversal forwards req, a request on a universal connector path
// whose body ReadMCPBody read, to serverID, as serveHTTP forwards one on
// the server's own path. A connected server whose protocol predates
// edgeproto.VersionUniversalMCP is refused with 501.
func (r *Relay) ForwardUniversal(w http.ResponseWriter, req *http.Request, serverID string, body []byte) {
	r.mu.Lock()
	reg := r.servers[serverID]
	var old error
	if reg != nil && reg.version < edgeproto.VersionUniversalMCP {
		old = noUniversalEndpoint{name: reg.name, id: reg.id, agentVersion: reg.agentVersion, origin: r.origin}
	}
	r.mu.Unlock()
	if old != nil {
		r.countRefusal("no universal MCP endpoint")
		http.Error(w, old.Error(), http.StatusNotImplemented)
		return
	}
	r.forward(w, req, serverID, body)
}

// noUniversalEndpoint refuses a universal connector request to a server
// whose protocol version predates edgeproto.VersionUniversalMCP.
type noUniversalEndpoint struct{ name, id, agentVersion, origin string }

func (e noUniversalEndpoint) Error() string {
	return fmt.Sprintf("server %s (%s) runs aether-server %s, which does not serve the connector at %s%s; upgrade it: sudo aether-server update, "+
		"or use its own connector URL %s%s", e.name, e.id, e.agentVersion, e.origin, edgeproto.PathUniversalMCP, e.origin, edgeproto.MCPPath(e.id))
}

// Version is the protocol version negotiated with the claimed server
// serverID, 0 when it holds no control channel.
func (r *Relay) Version(serverID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := r.servers[serverID]
	if reg == nil || !reg.claimed || reg.ctx.Err() != nil {
		return 0
	}
	return reg.version
}

func (r *Relay) forward(w http.ResponseWriter, req *http.Request, serverID string, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	addr := clientAddr(req.RemoteAddr)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// The client's Host does not pass: the request names the server.
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "http", serverID, ""
			// Sent with its length, the body carries none of the client's
			// trailers: net/http writes them only on a chunked body.
			pr.Out.TransferEncoding = nil
			forwardHeaders(pr.Out.Header, addr)
		},
		Transport:     r.http,
		FlushInterval: -1,
		ErrorHandler: func(_ http.ResponseWriter, out *http.Request, err error) {
			r.refuseHTTP(w, out, serverID, err)
		},
	}
	proxy.ServeHTTP(&responseFilter{ResponseWriter: w}, req)
}

// refuseHTTP answers a request that reached no server, or whose server's
// answer failed before it started, in plain text.
func (r *Relay) refuseHTTP(w http.ResponseWriter, req *http.Request, serverID string, err error) {
	if req.Context().Err() != nil {
		return
	}
	var old noMCPEndpoint
	var ref edgeproto.Refusal
	var sref serverRefusal
	status, text := http.StatusBadGateway, ""
	switch {
	case errors.As(err, &old):
		r.countRefusal("no MCP endpoint")
		status, text = http.StatusNotImplemented, old.Error()
	case errors.Is(err, errHTTPLimit):
		r.countRefusal("MCP connection limit")
		status, text = http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, edgeproto.RefusalNotConnected):
		ctx, cancel := context.WithTimeout(req.Context(), directoryTimeout)
		if kerr := r.dir.Known(ctx, serverID); kerr != nil {
			err = kerr
		}
		cancel()
		status, text = r.refusal(err)
	case errors.As(err, &ref), errors.As(err, &sref):
		status, text = r.refusal(err)
	default:
		r.countRefusal("MCP request failed")
		text = "the server's answer failed: " + err.Error()
	}
	http.Error(w, text, status)
}

// dialHTTP opens an http connection to the server addr names,
// "<server id>:80", for the pool.
func (r *Relay) dialHTTP(ctx context.Context, _, addr string) (net.Conn, error) {
	serverID, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	c, server, err := r.open(ctx, edgeproto.KindHTTP, serverID, edgeproto.Account{}, edgeproto.Device{}, netip.AddrPort{}, "")
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	c.spliced = true
	r.mu.Unlock()
	context.AfterFunc(c.ctx, func() { _ = server.Close() })
	return &httpConn{Conn: server, r: r, c: c}, nil
}

// httpConn is the server end of an http connection, as the pool holds it.
// Its bytes count against the egress budget and are paced once the
// budget is spent, as a splice's are.
type httpConn struct {
	net.Conn
	r *Relay
	c *relayConn
}

func (h *httpConn) Read(p []byte) (int, error) {
	throttled := h.r.throttled()
	if throttled {
		p = p[:min(len(p), throttledChunk)]
	}
	n, err := h.Conn.Read(p)
	if n > 0 {
		if throttled && !h.r.throttle(h.c, n) {
			return 0, context.Cause(h.c.ctx)
		}
		h.r.count(n)
	}
	return n, err
}

func (h *httpConn) Write(p []byte) (int, error) {
	month := h.r.count(len(p))
	n, err := h.Conn.Write(p)
	if n < len(p) {
		h.r.giveBack(len(p)-n, month)
	}
	return n, err
}

// Close ends c, which closes the data socket.
func (h *httpConn) Close() error {
	h.c.cancel(errSpliceDone)
	h.r.drop(h.c)
	return nil
}

// responseFilter writes a server's answer with only responseHeaders and
// the CSP stamp. It drops 1xx responses and trailers, which ReverseProxy
// writes with whatever headers the server sent.
type responseFilter struct {
	http.ResponseWriter
	wrote bool
}

// Header is a scratch map once the header is written, so nothing set
// after it becomes a trailer.
func (f *responseFilter) Header() http.Header {
	if f.wrote {
		return http.Header{}
	}
	return f.ResponseWriter.Header()
}

func (f *responseFilter) WriteHeader(code int) {
	if f.wrote || code < http.StatusOK {
		return
	}
	f.wrote = true
	h := f.ResponseWriter.Header()
	for k := range h {
		if !responseHeaders[k] {
			delete(h, k)
		}
	}
	h.Set("Content-Security-Policy", connectorCSP)
	f.ResponseWriter.WriteHeader(code)
}

func (f *responseFilter) Write(p []byte) (int, error) {
	f.WriteHeader(http.StatusOK)
	return f.ResponseWriter.Write(p)
}

func (f *responseFilter) Unwrap() http.ResponseWriter { return f.ResponseWriter }
