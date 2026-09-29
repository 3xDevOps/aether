package relay

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"unicode"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// HeaderForwardedFor is the header a reverse proxy in front of the edge
// sets to the client address: nginx's
// `proxy_set_header X-Forwarded-For $remote_addr`.
const HeaderForwardedFor = "X-Forwarded-For"

// Forwarded serves next behind a reverse proxy that terminates TLS.
//
// With no proxies, the proxy is on this host: a request whose peer is a
// loopback address takes its client address from the right-most
// HeaderForwardedFor entry, the address the proxy itself accepted the
// connection from, and a request from any other peer keeps its own
// address without the header being read.
//
// With proxies, the proxy is any peer in those networks, such as a proxy
// in another container. Its request takes the right-most entry that is
// not in proxies, so a chain of proxies in them passes the client address
// along. A request from a peer outside proxies is refused: serving it
// with its own address would let anyone who reaches the listener past
// the proxy skip the proxy.
//
// Entries to the left of the one taken are whatever the client sent, and
// are ignored. A request from a proxy is refused when the entry it needs
// is missing or not a bare IP address: falling back to the peer would put
// every client in the proxy's address's limits. The address replaces the
// request's RemoteAddr, so every limit per address counts it, and the
// request holds one of its address block's open-connection slots
// (Listener) while it is served; a relayed connection is served until it
// closes.
func (r *Relay) Forwarded(proxies []netip.Prefix, next http.Handler) http.Handler {
	isProxy := netip.Addr.IsLoopback
	if len(proxies) > 0 {
		isProxy = func(a netip.Addr) bool { return inAny(proxies, a) }
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		peer, err := netip.ParseAddrPort(req.RemoteAddr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unreadable peer address "+req.RemoteAddr)
			return
		}
		client := peer.Addr().Unmap()
		switch {
		case isProxy(client):
			if client, err = forwardedFor(req.Header, proxies); err != nil {
				r.countRefusal("no forwarded client address")
				slog.Warn("relay: request from the proxy refused", "route", req.URL.Path, "error", err)
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		case len(proxies) > 0:
			r.countRefusal("not from a trusted proxy")
			slog.Warn("relay: request from outside the trusted proxies refused", "route", req.URL.Path, "peer", client)
			writeError(w, http.StatusForbidden, fmt.Sprintf(
				"this edge serves requests only through its reverse proxy, and %s is not in the edge's --trusted-proxies", client))
			return
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

func inAny(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// forwardedFor reads the entries of every HeaderForwardedFor line together
// from the right, skipping those in proxies, and returns the first other
// one; the left-most when every entry is in proxies.
func forwardedFor(h http.Header, proxies []netip.Prefix) (netip.Addr, error) {
	const fix = "; configure the proxy with `proxy_set_header " + HeaderForwardedFor + " $remote_addr`"
	lines := h.Values(HeaderForwardedFor)
	if len(lines) == 0 {
		return netip.Addr{}, errors.New("the proxy in front of this edge sent no " + HeaderForwardedFor + " header" + fix)
	}
	entries := strings.Split(strings.Join(lines, ","), ",")
	var addr netip.Addr
	for i := len(entries) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(entries[i])
		if a, err := netip.ParseAddr(entry); err == nil && a.Zone() == "" {
			if addr = a.Unmap(); !inAny(proxies, addr) {
				break
			}
			continue
		}
		// The entry goes into the response and the log: a header must not
		// fill either, or forge a log line.
		entry = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, entry)
		if r := []rune(entry); len(r) > 64 {
			entry = string(r[:64]) + "..."
		}
		if i == len(entries)-1 {
			return netip.Addr{}, fmt.Errorf("the right-most %s entry from the proxy in front of this edge, %q, is not an IP address%s",
				HeaderForwardedFor, entry, fix)
		}
		return netip.Addr{}, fmt.Errorf("%s entry %d from the right, %q, is not an IP address; the edge read past the entries to its right because they are in --trusted-proxies, "+
			"so it took %s for a proxy: list only the proxies' own addresses in --trusted-proxies, and have each proxy append the address it accepted the connection from",
			HeaderForwardedFor, len(entries)-i, entry, addr)
	}
	return addr, nil
}
