package ompbroker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SocketName is each session's socket in its directory under Front's.
const SocketName = "broker.sock"

// Authorize names the account whose logins session may use now, or says
// why it may not.
type Authorize func(ctx context.Context, session string) (Account, error)

type sessionKey struct{}

// Front serves each session a socket of its own and passes its requests to
// its account's broker with the broker's token. It checks the session on
// every request, and again every Recheck while a request stays open, so a
// revoked share or a closed session cuts off the snapshot stream too.
type Front struct {
	brokers   *Manager
	authorize Authorize
	dir       string
	recheck   time.Duration
	server    *http.Server

	mu        sync.Mutex
	listeners map[string]net.Listener
}

func NewFront(brokers *Manager, dir string, authorize Authorize) *Front {
	f := &Front{brokers: brokers, authorize: authorize, dir: dir, recheck: 10 * time.Second, listeners: map[string]net.Listener{}}
	f.server = &http.Server{
		Handler:           f,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, sessionKey{}, filepath.Base(filepath.Dir(c.LocalAddr().String())))
		},
	}
	return f
}

// Listen serves session's socket, which it returns, until Close.
func (f *Front) Listen(session string) (string, error) {
	if session == "" || session != filepath.Base(session) || strings.HasPrefix(session, ".") {
		return "", fmt.Errorf("omp broker: invalid session %q", session)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := filepath.Join(f.dir, session, SocketName)
	if f.listeners[session] != nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("omp broker: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("omp broker: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", fmt.Errorf("omp broker: %w", err)
	}
	f.listeners[session] = ln
	go func() {
		if err := f.server.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("omp broker: serve a session's socket", "session", session, "error", err)
		}
	}()
	return path, nil
}

// Restore listens again on every session socket a previous server left.
func (f *Front) Restore() error {
	entries, err := os.ReadDir(f.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("omp broker: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() {
			if _, err := f.Listen(e.Name()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Close stops serving session and removes its socket.
func (f *Front) Close(session string) {
	f.mu.Lock()
	ln := f.listeners[session]
	delete(f.listeners, session)
	f.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if err := os.RemoveAll(filepath.Join(f.dir, session)); err != nil {
		slog.Warn("omp broker: remove a session's socket", "session", session, "error", err)
	}
}

func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	session, _ := r.Context().Value(sessionKey{}).(string)
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	account, err := f.authorize(r.Context(), session)
	if err != nil {
		http.Error(w, "aether: "+err.Error(), http.StatusForbidden)
		return
	}
	if account.Shared && !sharedRoute(r) {
		http.Error(w, "aether: a session on another member's account may only read, refresh and report usage of its logins", http.StatusForbidden)
		return
	}
	b, err := f.brokers.ensure(r.Context(), account)
	if err != nil {
		http.Error(w, "aether: "+err.Error(), http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go f.watch(ctx, cancel, session, account.ID)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(b.url)
			pr.Out.Header.Set("Authorization", "Bearer "+b.token)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				f.brokers.forget(account.ID)
			}
			http.Error(w, "aether: omp broker for "+account.ID+": "+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

// sharedRoute reports whether a session on another member's account may
// make r: read the logins, refresh one, and report usage, but not store,
// disable or block the owner's logins.
func sharedRoute(r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet:
		return r.URL.Path == "/v1/snapshot" || r.URL.Path == "/v1/snapshot/stream"
	case r.Method == http.MethodPost && r.URL.Path == "/v1/usage/observed":
		return true
	case r.Method == http.MethodPost:
		id, ok := strings.CutPrefix(r.URL.Path, "/v1/credential/")
		id, ok2 := strings.CutSuffix(id, "/refresh")
		_, numErr := strconv.ParseUint(id, 10, 64)
		return ok && ok2 && numErr == nil
	}
	return false
}

// watch ends a request once its session may no longer use account.
func (f *Front) watch(ctx context.Context, cancel context.CancelFunc, session, account string) {
	t := time.NewTicker(f.recheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a, err := f.authorize(ctx, session); err != nil || a.ID != account {
				cancel()
				return
			}
		}
	}
}
