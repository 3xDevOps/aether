package relay

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// HeaderForwardedFor is the header a reverse proxy in front of the edge
// sets to the client address: nginx's
// `proxy_set_header X-Forwarded-For $remote_addr`.
const HeaderForwardedFor = "X-Forwarded-For"

// Forwarded serves next behind a reverse proxy on this host that
// terminates TLS. A request whose peer is a loopback address, the proxy,
// takes its client address from the right-most HeaderForwardedFor entry,
// the address the proxy itself accepted the connection from. Entries to
// its left are whatever the client sent, and are ignored. Such a request
// is refused when that entry is missing or not a bare IP address: falling
// back to the peer would put every client in the loopback address's
// limits. A request from any other peer keeps its own address and the
// header is not read. The address replaces the request's RemoteAddr, so
// every limit per address counts it, and the request holds one of its
// address block's open-connection slots (Listener) while it is served;
// a relayed connection is served until it closes.
func (r *Relay) Forwarded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		peer, err := netip.ParseAddrPort(req.RemoteAddr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unreadable peer address "+req.RemoteAddr)
			return
		}
		client := peer.Addr().Unmap()
		if client.IsLoopback() {
			if client, err = forwardedFor(req.Header); err != nil {
				r.countRefusal("no forwarded client address")
				slog.Warn("relay: request from the proxy refused", "route", req.URL.Path, "error", err)
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		key := edgeproto.RateLimitKey(client)
		if !r.takeAddrSlot(key) {
			r.countRefusal("too many connections from one address")
			writeError(w, http.StatusTooManyRequests, "too many open connections from your address")
			return
		}
		defer r.releaseAddrSlot(key)
		fwd := req.WithContext(req.Context())
		fwd.RemoteAddr = netip.AddrPortFrom(client, 0).String()
		next.ServeHTTP(w, fwd)
	})
}

// forwardedFor returns the right-most address of every HeaderForwardedFor
// line together.
func forwardedFor(h http.Header) (netip.Addr, error) {
	const fix = "; configure the proxy with `proxy_set_header " + HeaderForwardedFor + " $remote_addr`"
	lines := h.Values(HeaderForwardedFor)
	if len(lines) == 0 {
		return netip.Addr{}, errors.New("the proxy in front of this edge sent no " + HeaderForwardedFor + " header" + fix)
	}
	last := lines[len(lines)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	last = strings.TrimSpace(last)
	addr, err := netip.ParseAddr(last)
	if err != nil || addr.Zone() != "" {
		const maxShown = 64
		if len(last) > maxShown {
			last = last[:maxShown] + "..."
		}
		return netip.Addr{}, fmt.Errorf("the right-most %s entry from the proxy in front of this edge, %q, is not an IP address%s",
			HeaderForwardedFor, last, fix)
	}
	return addr.Unmap(), nil
}
