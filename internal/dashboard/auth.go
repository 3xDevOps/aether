package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// DefaultTokenTTL is how long a minted token lives when the caller
	// asks for no particular lifetime.
	DefaultTokenTTL = 12 * time.Hour
	// MaxTokenTTL caps a requested lifetime. Tokens live in memory only,
	// so a restart revokes everything regardless.
	MaxTokenTTL = 24 * time.Hour
)

var errUnknownToken = errors.New("dashboard: unknown token")

// Tokens is the gateway's bearer-token table: the bridge from an
// SSH-authenticated member to an HTTP request. A token carries exactly
// its member's authority - every call it makes runs the same role and
// capability checks the SSH transport applies - so it is stored hashed
// and never persisted.
type Tokens struct {
	origin string

	mu      sync.Mutex
	entries map[[sha256.Size]byte]tokenEntry
}

type tokenEntry struct {
	member  domain.MemberID
	expires time.Time
}

func newTokens(origin string) *Tokens {
	return &Tokens{origin: origin, entries: make(map[[sha256.Size]byte]tokenEntry)}
}

// Mint issues a token acting as member for ttl, defaulting to
// DefaultTokenTTL and capped at MaxTokenTTL.
func (t *Tokens) Mint(member domain.MemberID, ttl time.Duration) (string, time.Time, error) {
	if member == "" {
		return "", time.Time{}, errors.New("dashboard: mint requires a member")
	}
	switch {
	case ttl <= 0:
		ttl = DefaultTokenTTL
	case ttl > MaxTokenTTL:
		ttl = MaxTokenTTL
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(ttl)
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.entries {
		if time.Now().After(e.expires) {
			delete(t.entries, k)
		}
	}
	t.entries[sha256.Sum256([]byte(token))] = tokenEntry{member: member, expires: expires}
	return token, expires, nil
}

// Revoke drops a token, refusing tokens minted for another member.
func (t *Tokens) Revoke(member domain.MemberID, token string) error {
	key := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok || e.member != member {
		return errUnknownToken
	}
	delete(t.entries, key)
	return nil
}

// URL renders the direct-exposure URL carrying token; empty when the
// gateway is loopback-only or bound to a wildcard address, where only the
// client knows the host it reached the server by.
func (t *Tokens) URL(token string) string {
	if t.origin == "" {
		return ""
	}
	return t.origin + "/?token=" + token
}

// resolve maps a presented token to its member.
func (t *Tokens) resolve(token string) (domain.MemberID, bool) {
	if token == "" {
		return "", false
	}
	key := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expires) {
		delete(t.entries, key)
		return "", false
	}
	return e.member, true
}

// directOrigin renders the browser-facing origin of a direct-exposure
// bind address. A wildcard or portless address has no single origin the
// server can name, so it has none.
func directOrigin(addr string) string {
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

// authenticate resolves the request's bearer token to a member, writing
// the 401 itself when there is none. It returns the token as well, because
// a long-lived WebSocket has to keep re-checking it. Browsers cannot set
// headers on a WebSocket handshake, so those requests may carry the token
// in the query string instead.
func (g *Gateway) authenticate(w http.ResponseWriter, r *http.Request, allowQuery bool) (domain.MemberID, string, bool) {
	token := ""
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		token = strings.TrimSpace(h[7:])
	}
	if token == "" && allowQuery {
		token = r.URL.Query().Get("token")
	}
	member, ok := g.tokens.resolve(token)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: "a valid dashboard token is required; mint one with `aether dash`",
		})
		return "", "", false
	}
	return member, token, true
}

// watchAuthorization tears a live socket down when the authority behind it
// goes away. The handshake checks are a snapshot, and a socket outlives
// them: without this a stream opened a moment before a revoke keeps
// mirroring, a removed member keeps receiving the deployment's events, and
// a write attach keeps typing into someone else's agent after an admin
// protected the run or restricted the session. A token is documented to
// act with exactly its member's *current* authority, so the same gates the
// handshake applied are re-applied here for as long as the socket lives -
// the pattern the sync bridge already uses for a live overlay.
//
// write names the run whose steer capability must hold; it is empty for
// read-only mirrors and event streams. Canceling ctx is what ends the
// handler; closing the socket is what tells the browser why. A downgrade
// is a close: nothing tries to hot-swap a write attach to read-only.
func (g *Gateway) watchAuthorization(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, token string, member domain.MemberID, write domain.RunID) {
	t := time.NewTicker(g.cfg.revalidate)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lost := g.authorizationLost(ctx, token, member, write)
			if ctx.Err() != nil {
				// The watch was cancelled mid-check - a handler re-arming
				// it, not an authority change - so nothing may be closed
				// on the aborted check's word.
				return
			}
			if lost == "" {
				continue
			}
			_ = conn.Close(websocket.StatusPolicyViolation, lost)
			cancel()
			return
		}
	}
}

// authorizationLost re-runs the handshake's gates and names the first one
// that no longer holds, or returns empty while the socket may live on.
// Store reads only: this runs every few seconds per live socket.
func (g *Gateway) authorizationLost(ctx context.Context, token string, member domain.MemberID, write domain.RunID) string {
	if _, ok := g.tokens.resolve(token); !ok {
		return "dashboard token revoked or expired"
	}
	if authorityDenied(g.cfg.RPC.CheckMember(ctx, member)) {
		return "membership withdrawn"
	}
	if write != "" {
		if authorityDenied(g.cfg.RPC.CheckSteer(ctx, member, write)) {
			return "steer permission withdrawn"
		}
	}
	return ""
}

// authorityDenied reports whether a revalidation answer is a definitive
// loss of authority. A transient store failure or a cancelled check maps
// to CodeInternal and must not tear every live socket down over one bad
// tick; the next tick re-checks.
func authorityDenied(perr *protocol.Error) bool {
	return perr != nil && (perr.Code == protocol.CodeDenied || perr.Code == protocol.CodeNotFound)
}
