package coord

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// SocketName is the coordination socket inside a run's directory. The
	// name is the wire version's identity: a future version adds a name
	// beside this one instead of replacing it, so a run that survives a
	// restart keeps the socket its container was provisioned against.
	SocketName = "coord.sock"
	// ConfigName is the optional harness config a launch profile points
	// the agent at. Its content belongs to the harness registry; this
	// package owns only where it lives and that it is read-only.
	ConfigName = "mcp.json"
)

// wireSocketNames are the socket names this server serves, one per
// coordination wire version - coord.sock is v1. Recovery rebinds every
// name it finds in a surviving run's directory, so a run keeps every wire
// version its container references.
var wireSocketNames = []string{SocketName}

// Host-side modes. The parent is private to the server; the per-run
// directory and the socket are what a container sees through the bind
// mount, and the agent inside it is not root - hence 0755 and 0666. The
// mount is the only thing that grants access, so the socket's own mode
// carries no authorization.
const (
	rootMode   = 0o700
	runDirMode = 0o755
	configMode = 0o444
	socketMode = 0o666
)

// maxRequestBytes bounds one coordination request line. The largest legal
// request is a 4 KiB body plus JSON escaping; the control channel's 32 MiB
// budget belongs to profile pushes and has no business here.
const maxRequestBytes = 64 << 10

// The agent behind the socket is only semi-trusted, so its connections are
// bounded like everything else it can spend. A bridge dials per tool call
// and redials after EOF, so it holds one connection at a time and these
// limits are far above anything a well-behaved one reaches; they exist so
// a run that loops connect() cannot walk the whole server to its file
// descriptor limit, and so a bridge that connects and then goes silent is
// eventually reaped.
const (
	maxConnsPerRun = 16
	idleTimeout    = 5 * time.Minute
)

// Provision creates the run's coordination directory, writes the optional
// harness config into it, and binds its socket. It returns the host
// directory to bind-mount into the container; the mount itself belongs to
// the harness registry. Calling it again for the same run rebinds the
// socket, which is what a restarted sidecar needs.
func (s *Service) Provision(ctx context.Context, run domain.RunID, config []byte) (string, error) {
	_ = ctx
	if s.cfg.Disabled {
		return "", ErrDisabled
	}
	dir, err := s.runDir(run)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.cfg.Dir, rootMode); err != nil {
		return "", fmt.Errorf("coord: create %s: %w", s.cfg.Dir, err)
	}
	// MkdirAll applies the process umask; the modes here are part of the
	// contract with the container, so set them explicitly.
	if err := os.Chmod(s.cfg.Dir, rootMode); err != nil {
		return "", fmt.Errorf("coord: set mode on %s: %w", s.cfg.Dir, err)
	}
	if err := os.MkdirAll(dir, runDirMode); err != nil {
		return "", fmt.Errorf("coord: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, runDirMode); err != nil {
		return "", fmt.Errorf("coord: set mode on %s: %w", dir, err)
	}
	if config != nil {
		path := filepath.Join(dir, ConfigName)
		if err := removeFile(path); err != nil {
			return "", fmt.Errorf("coord: replace %s: %w", path, err)
		}
		if err := os.WriteFile(path, config, configMode); err != nil {
			return "", fmt.Errorf("coord: write %s: %w", path, err)
		}
		if err := os.Chmod(path, configMode); err != nil {
			return "", fmt.Errorf("coord: set mode on %s: %w", path, err)
		}
	}
	if err := s.listen(run, SocketName); err != nil {
		return "", err
	}
	return dir, nil
}

// Release stops the run's listeners, removes its coordination directory,
// and retires its mailbox: the run is done reading, so its rows have no
// reader left. Idempotent, and safe for a run that was never provisioned.
func (s *Service) Release(run domain.RunID) error {
	dir, err := s.runDir(run)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for key, l := range s.listeners {
		if key.run == run {
			_ = l.Close()
			delete(s.listeners, key)
		}
	}
	delete(s.buckets, run)
	delete(s.inboxBuckets, run)
	delete(s.peers, run)
	delete(s.noticed, run)
	s.mu.Unlock()
	s.radar.forget(run)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("coord: remove %s: %w", dir, err)
	}
	if err := s.cfg.Mail.DeleteRunMessages(context.Background(), run); err != nil {
		return fmt.Errorf("coord: retire mailbox: %w", err)
	}
	return nil
}

// recoverListeners rebuilds the host side after a restart. A run's
// directory is the record that it was provisioned, and the socket names in
// it are the wire versions its container references: while coordination is
// enabled, every one of them is rebound for a run that is still active,
// and the directory of a run that is not is garbage collected. The rebind
// creates a new inode, so a bridge holding the old one redials.
//
// With the kill switch off, old sockets are unlinked and nothing is
// recreated: the directory and config still mounted in a live container
// become inert.
func (s *Service) recoverListeners(ctx context.Context) error {
	entries, err := os.ReadDir(s.cfg.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("coord: read %s: %w", s.cfg.Dir, err)
	}
	active := make(map[domain.RunID]bool)
	if !s.cfg.Disabled {
		runs, lerr := s.cfg.Store.ListActiveRuns(ctx)
		if lerr != nil {
			return fmt.Errorf("coord: list active runs: %w", lerr)
		}
		for _, r := range runs {
			active[r.ID] = true
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		run := domain.RunID(e.Name())
		dir := filepath.Join(s.cfg.Dir, e.Name())
		switch {
		case s.cfg.Disabled:
			for _, name := range wireSocketNames {
				if err := removeFile(filepath.Join(dir, name)); err != nil {
					return fmt.Errorf("coord: unlink %s: %w", filepath.Join(dir, name), err)
				}
			}
		case active[run]:
			for _, name := range survivingSockets(dir) {
				if err := s.listen(run, name); err != nil {
					return err
				}
			}
		default:
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("coord: remove %s: %w", dir, err)
			}
			if err := s.cfg.Mail.DeleteRunMessages(ctx, run); err != nil {
				return fmt.Errorf("coord: retire mailbox: %w", err)
			}
		}
	}
	return nil
}

// survivingSockets lists the wire-version sockets present in a run's
// directory. A provisioned run whose sockets were unlinked - by a kill
// switch cycle - is brought back on the current version.
func survivingSockets(dir string) []string {
	var names []string
	for _, name := range wireSocketNames {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		names = append(names, SocketName)
	}
	return names
}

// listen binds one wire-version socket for a run and starts serving it.
func (s *Service) listen(run domain.RunID, name string) error {
	dir, err := s.runDir(run)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if rerr := removeFile(path); rerr != nil {
		return fmt.Errorf("coord: unlink stale socket %s: %w", path, rerr)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fmt.Errorf("coord: listen on %s: %w", path, err)
	}
	// The socket file outlives the process on purpose: it is the record
	// that this run was provisioned, and at which wire version.
	l.SetUnlinkOnClose(false)
	if err := os.Chmod(path, socketMode); err != nil {
		_ = l.Close()
		return fmt.Errorf("coord: set mode on %s: %w", path, err)
	}

	key := socketKey{run: run, name: name}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
		return ErrClosed
	}
	if prev := s.listeners[key]; prev != nil {
		_ = prev.Close()
	}
	s.listeners[key] = l
	// The Add happens under the same lock as the closed check: a Close
	// that interleaved between them could finish its Wait before this
	// goroutine was counted.
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.accept(l, run, make(chan struct{}, maxConnsPerRun))
	}()
	return nil
}

// accept serves one socket. slots bounds how many connections it will hold
// open at once; the excess is closed immediately rather than queued, so a
// run cannot pin file descriptors the rest of the server needs.
func (s *Service) accept(l *net.UnixListener, run domain.RunID, slots chan struct{}) {
	for {
		conn, err := l.Accept()
		if err != nil {
			if !s.isClosed() && !errors.Is(err, net.ErrClosed) {
				slog.Warn("coord: accept failed", "run", run, "error", err)
			}
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			slog.Warn("coord: connection refused, run is at its concurrency cap",
				"run", run, "cap", maxConnsPerRun)
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer func() {
				<-slots
				s.wg.Done()
			}()
			s.serve(conn, run)
		}()
	}
}

// serve runs the NDJSON JSON-RPC loop on one connection: requests in,
// responses out, in order. The connection is the run's identity - it
// arrived on that run's socket - so nothing on the wire names a sender.
func (s *Service) serve(conn net.Conn, run domain.RunID) {
	defer conn.Close() //nolint:errcheck // read side of a closing connection
	done := make(chan struct{})
	defer close(done)
	// Unblock the read when the service closes; a bridge that never sends
	// another line must not pin shutdown.
	go func() {
		select {
		case <-s.serveCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	capped := &capReader{r: conn}
	r := bufio.NewReaderSize(capped, 16<<10)
	for {
		capped.left = maxRequestBytes
		// A connection that stops speaking is dropped rather than held
		// forever; the bridge redials on its next tool call anyway.
		if err := conn.SetReadDeadline(time.Now().Add(s.cfg.idle)); err != nil {
			return
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		resp := s.handle(s.serveCtx, run, line)
		if s.serveCtx.Err() != nil {
			return
		}
		out, err := json.Marshal(resp)
		if err != nil {
			return
		}
		// The write is bounded like the read: a bridge that pipelines
		// requests without ever reading responses fills the socket buffer,
		// and an unbounded Write would wedge this loop out of the idle
		// reaper's reach.
		if err := conn.SetWriteDeadline(time.Now().Add(s.cfg.idle)); err != nil {
			return
		}
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// handle decodes one request and dispatches it. The method set is closed:
// anything outside the three coordination methods is method-not-found, so
// no control verb is reachable from inside a container.
func (s *Service) handle(ctx context.Context, run domain.RunID, line []byte) protocol.Response {
	req, resp, valid := protocol.ParseRequest(line)
	if !valid {
		return resp
	}

	var (
		result any
		rpcErr *protocol.Error
	)
	switch req.Method {
	case protocol.MethodCoordStatus:
		result, rpcErr = s.Status(ctx, run)
	case protocol.MethodCoordSend:
		p, perr := decodeParams[protocol.CoordSendParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr
			return resp
		}
		result, rpcErr = s.Send(ctx, run, p)
	case protocol.MethodCoordInbox:
		p, perr := decodeParams[protocol.CoordInboxParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr
			return resp
		}
		result, rpcErr = s.Inbox(ctx, run, p)
	default:
		resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + req.Method}
		return resp
	}
	if rpcErr != nil {
		resp.Error = rpcErr
		return resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &protocol.Error{Code: protocol.CodeInternal, Message: "marshal result: " + err.Error()}
		return resp
	}
	resp.Result = raw
	return resp
}

// decodeParams is the coordination socket's spelling of
// protocol.DecodeParams: a bad body is reported under the method it
// arrived for.
func decodeParams[T any](method string, raw json.RawMessage) (T, *protocol.Error) {
	p, err := protocol.DecodeParams[T](raw)
	if err != nil {
		return p, invalidParams(method, err.Error())
	}
	return p, nil
}

// runDir is the run's coordination directory. Run IDs are store-assigned,
// but this path is handed to the container runtime, so a separator in one
// is refused rather than escaping the coordination root.
func (s *Service) runDir(run domain.RunID) (string, error) {
	name := string(run)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("coord: %q is not a usable run directory name", name)
	}
	return filepath.Join(s.cfg.Dir, name), nil
}

// capReader fails the read past left bytes, so one oversized request line
// cannot make the server buffer it.
type capReader struct {
	r    io.Reader
	left int
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("coord: request exceeds %d bytes", maxRequestBytes)
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= n
	return n, err
}
