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
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// v1 and v2 sockets are retired at the v3 cutover. Recovery unlinks
	// them instead of serving a shape their bridges cannot parse.
	legacySocketName   = "coord.sock"
	previousSocketName = "coord2.sock"
	// CoAuthorsName holds the Co-authored-by trailers the agent appends to
	// its commits, one per member other than the owner who has steered the
	// run. It is rewritten whenever that set grows, so an agent reads it
	// again before each commit rather than caching it.
	CoAuthorsName = "co-authors"
)

// wireSocketNames is the current coordination wire only.
var wireSocketNames = []string{coordtransport.SocketName}

// retiredSocketNames are wire versions this server no longer speaks.
var retiredSocketNames = []string{legacySocketName, previousSocketName}

// maxSocketPath is the ceiling on a unix socket path: sun_path holds 108
// bytes including the terminator, so 107 characters are usable. The
// kernel reports an over-long path as EINVAL, which reads like a bug in
// this code rather than a state directory nested too deep.
const maxSocketPath = 107

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
// request is a 4 KiB body plus JSON escaping; the control channel's 96 MiB
// budget belongs to configuration imports and has no business here.
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

// Provision creates the run's coordination directory, writes the harness
// assets into it, and binds its socket. It returns the host directory to
// bind-mount into the container; the mount itself belongs to the harness
// registry, and so does what the files are - this package owns only where
// they live and that they are read-only to the container. Calling it again
// for the same run rebinds the socket, which is what a restarted sidecar
// needs.
func (s *Service) Provision(ctx context.Context, run domain.RunID, files map[string][]byte) (string, error) {
	_ = ctx
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
	for _, name := range slices.Sorted(maps.Keys(files)) {
		// The names come from the harness registry, never from a client or
		// an agent, but this path is handed to a container runtime: a name
		// that is not a plain file in this directory is refused rather than
		// written somewhere else.
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
			return "", fmt.Errorf("coord: %q is not a usable asset name", name)
		}
		path := filepath.Join(dir, name)
		if err := removeFile(path); err != nil {
			return "", fmt.Errorf("coord: replace %s: %w", path, err)
		}
		if err := os.WriteFile(path, files[name], configMode); err != nil {
			return "", fmt.Errorf("coord: write %s: %w", path, err)
		}
		if err := os.Chmod(path, configMode); err != nil {
			return "", fmt.Errorf("coord: set mode on %s: %w", path, err)
		}
	}
	if err := s.listen(run, coordtransport.SocketName); err != nil {
		return "", err
	}
	return dir, nil
}

// WriteCoAuthors replaces the run's co-author list with one trailer per
// line. The file is read-only to the container like the harness config:
// the agent copies these lines into its commits, it does not decide who is
// on them. Writing for a run that was never provisioned is an error, not a
// silent no-op - the caller would otherwise believe the agent was told.
//
// Unlike the harness config this is rewritten while the container is live,
// and the agent is told to read it before every commit, so the replacement
// is a rename over the old name: a reader either gets the whole previous
// list or the whole new one, never a missing path.
func (s *Service) WriteCoAuthors(run domain.RunID, trailers []string) error {
	dir, err := s.runDir(run)
	if err != nil {
		return err
	}
	var body []byte
	if len(trailers) > 0 {
		body = []byte(strings.Join(trailers, "\n") + "\n")
	}
	path := filepath.Join(dir, CoAuthorsName)
	tmp, err := os.CreateTemp(dir, "."+CoAuthorsName+"-*")
	if err != nil {
		return fmt.Errorf("coord: write %s: %w", path, err)
	}
	werr := os.WriteFile(tmp.Name(), body, configMode)
	if werr == nil {
		werr = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	// CreateTemp opens at 0600 and WriteFile keeps an existing file's mode,
	// so the container-readable mode is set explicitly before the rename.
	if werr == nil {
		werr = os.Chmod(tmp.Name(), configMode)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		// A temp file left behind is visible to the agent in the mount
		// beside the list it is told to read, so a cleanup that fails is
		// worth saying out loud even though the write error is what the
		// caller gets.
		if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			slog.Warn("coord: remove co-author temp file", "path", tmp.Name(), "error", rerr)
		}
		return fmt.Errorf("coord: write %s: %w", path, werr)
	}
	return nil
}

// Release stops the run's listeners, removes its coordination directory, and
// retires only that run's inbox. Outbound rows remain durable for peers that
// still need to read them. Idempotent, and safe for a run never provisioned.
func (s *Service) Release(run domain.RunID) error {
	dir, err := s.runDir(run)
	if err != nil {
		return err
	}
	done := s.closeRun(run)
	s.mu.Lock()
	for key, l := range s.listeners {
		if key.run == run {
			_ = l.Close()
			delete(s.listeners, key)
		}
	}
	if waiter := s.inboxWaiters[run]; waiter != nil {
		close(waiter.ch)
		delete(s.inboxWaiters, run)
	}
	delete(s.noticed, run)
	s.mu.Unlock()
	s.radar.forget(run)
	// Existing handlers keep using their scoped buckets and report lock until
	// they return. Waiting here prevents a late handler from recreating state
	// after cleanup and never deletes a lock that another handler can hold.
	<-done
	s.mu.Lock()
	delete(s.buckets, run)
	delete(s.inboxBuckets, run)
	delete(s.requestBuckets, run)
	delete(s.reportLocks, run)
	delete(s.runs, run)
	s.mu.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("coord: remove %s: %w", dir, err)
	}
	if err := s.cfg.Mail.DeleteRunMessages(context.Background(), run); err != nil {
		return fmt.Errorf("coord: retire mailbox: %w", err)
	}
	return nil
}

// recoverListeners rebuilds the authenticated run transport after a restart,
// independently of conflict policy. Active runs and retained terminal containers
// keep their sockets; every other run's directory is garbage collected.
func (s *Service) recoverListeners(ctx context.Context) error {
	entries, err := os.ReadDir(s.cfg.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("coord: read %s: %w", s.cfg.Dir, err)
	}
	active := make(map[domain.RunID]bool)
	retained := make(map[domain.RunID]bool)
	runs, lerr := s.cfg.Store.ListActiveRuns(ctx)
	if lerr != nil {
		return fmt.Errorf("coord: list active runs: %w", lerr)
	}
	for _, r := range runs {
		active[r.ID] = true
	}
	if s.cfg.RetainsContainer != nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			run := domain.RunID(e.Name())
			if !active[run] && s.cfg.RetainsContainer(ctx, run) {
				retained[run] = true
			}
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		run := domain.RunID(e.Name())
		dir := filepath.Join(s.cfg.Dir, e.Name())
		switch {
		case active[run] || retained[run]:
			// A retired version's socket goes first: leaving it bound
			// would answer an old bridge in a shape it cannot read.
			for _, name := range retiredSocketNames {
				if err := removeFile(filepath.Join(dir, name)); err != nil {
					return fmt.Errorf("coord: unlink %s: %w", filepath.Join(dir, name), err)
				}
			}
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

// survivingSockets lists the wire-version sockets present in a run's directory.
// A provisioned directory without a socket recovers on the current version.
func survivingSockets(dir string) []string {
	var names []string
	for _, name := range wireSocketNames {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		names = append(names, coordtransport.SocketName)
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
	// A unix socket path is capped by sun_path, and the kernel reports an
	// over-long one as EINVAL, which reads like a bug in this code rather
	// than a data directory nested too deep. Say what actually happened.
	if len(path) > maxSocketPath {
		return fmt.Errorf("coord: socket path %s is %d bytes, over the %d-byte limit: "+
			"use a shorter state directory", path, len(path), maxSocketPath)
	}
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
	defer func() { _ = conn.Close() }()
	done := make(chan struct{})
	defer close(done)
	connCtx, cancel := context.WithCancel(s.serveCtx)
	defer cancel()
	// Unblock the read when the service closes; a bridge that never sends
	// another line must not pin shutdown.
	go func() {
		select {
		case <-s.serveCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	// A report can be in evidence capture while the client disappears. Unix
	// sockets expose a non-consuming peek that lets the watcher observe that
	// close without racing the NDJSON reader or stealing a pipelined request.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go watchConnection(conn, cancel, stopWatch)

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
		if s.isRunClosing(run) {
			return
		}
		// Charge at the authenticated socket boundary, before parsing the
		// envelope, method, or params. A malformed request still consumes
		// transport budget and cannot be used to bypass the limiter.
		if !s.transportAllowed(run) {
			resp, ok := rateLimitedResponse(line)
			if !ok {
				return
			}
			out, merr := json.Marshal(resp)
			if merr != nil {
				return
			}
			if err = conn.SetWriteDeadline(time.Now().Add(s.cfg.idle)); err != nil {
				return
			}
			if _, err = conn.Write(append(out, '\n')); err != nil {
				return
			}
			continue
		}
		resp := s.handle(connCtx, run, line)
		if connCtx.Err() != nil {
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
		if err = conn.SetWriteDeadline(time.Now().Add(s.cfg.idle)); err != nil {
			return
		}
		if _, err = conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// handle dispatches only explicitly allowlisted run methods. Human control
// methods and caller-selected run identities are never reachable here.
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
	case protocol.MethodCoordAsk:
		p, perr := decodeParams[protocol.CoordAskParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr
			return resp
		}
		result, rpcErr = s.Ask(ctx, run, p)
	case protocol.MethodCoordReply:
		p, perr := decodeParams[protocol.CoordReplyParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr

			return resp
		}
		result, rpcErr = s.Reply(ctx, run, p)
	case protocol.MethodCoordReport:
		p, perr := decodeParams[protocol.CoordReportParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr
			return resp
		}
		result, rpcErr = s.CoordReport(ctx, run, p)
	case protocol.MethodCoordInbox:
		p, perr := decodeParams[protocol.CoordInboxParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr
			return resp
		}
		result, rpcErr = s.Inbox(ctx, run, p)
	case protocol.MethodRunReport:
		p, perr := decodeParams[protocol.RunReportParams](req.Method, req.Params)
		if perr != nil {
			resp.Error = perr
			return resp
		}
		result, rpcErr = s.Report(ctx, run, p)
	default:
		if isDevelopmentMethod(req.Method) {
			result, rpcErr = s.handleDevelopment(ctx, run, req.Method, req.Params)
		} else if isMissionMethod(req.Method) {
			if s.cfg.Disabled {
				resp.Error = unavailable(req.Method)
				return resp
			}
			if s.cfg.Mission == nil {
				resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + req.Method}
				return resp
			}
			var missionErr error
			result, missionErr = s.cfg.Mission.HandleAgent(ctx, run, req.Method, req.Params)
			if missionErr != nil {
				rpcErr = missionRPCError(req.Method, missionErr)
			}
			if rpcErr == nil {
				break
			}
		} else {
			resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + req.Method}
			return resp
		}
	}
	if rpcErr != nil {
		resp.Error = missionRPCError(req.Method, rpcErr)
		return resp
	}
	var raw json.RawMessage
	var err error
	if isDevelopmentMethod(req.Method) {
		raw, err = protocol.MarshalDevResult(result)
	} else {
		raw, err = json.Marshal(result)
	}
	if err != nil {
		resp.Error = &protocol.Error{Code: protocol.CodeInternal, Message: "marshal result: " + err.Error()}
		return resp
	}
	resp.Result = raw
	return resp
}

func (s *Service) handleDevelopment(ctx context.Context, run domain.RunID, method string, params json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Development == nil {
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + method}
	}
	if !s.enterRun(run) {
		return nil, runClosing(method)
	}
	defer s.leaveRun(run)
	if err := ctx.Err(); err != nil {
		return nil, internalError(method, err)
	}
	self, rpcErr := s.resolveRun(ctx, method, run)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if self.Status.Terminal() {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": run has finished"}
	}
	if len(params) > protocol.MaxDevParamsBytes {
		return nil, invalidParams(method, "development request is too large")
	}
	// The backend decodes typed parameters strictly. Guard identity here as well:
	// no spelling accepted by encoding/json may override the socket-bound run.
	var fields map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, invalidParams(method, "expected an object")
		}
	}
	for field := range fields {
		if strings.EqualFold(field, "run_id") {
			return nil, invalidParams(method, "run identity comes from the socket")
		}
	}
	result, err := s.cfg.Development.HandleAgent(ctx, run, method, params)
	return result, missionRPCError(method, err)
}

func isDevelopmentMethod(method string) bool {
	switch method {
	case protocol.MethodDevTerminalList, protocol.MethodDevTerminalStart,
		protocol.MethodDevTerminalOutput, protocol.MethodDevTerminalScreen,
		protocol.MethodDevTerminalScreenshot, protocol.MethodDevTerminalInput,
		protocol.MethodDevTerminalResize, protocol.MethodDevTerminalWait, protocol.MethodDevTerminalStop,
		protocol.MethodDevBrowserStatus, protocol.MethodDevBrowserOpen, protocol.MethodDevBrowserPages,
		protocol.MethodDevBrowserNavigate, protocol.MethodDevBrowserSnapshot, protocol.MethodDevBrowserAction,
		protocol.MethodDevBrowserScreenshot, protocol.MethodDevBrowserViewport, protocol.MethodDevBrowserWait,
		protocol.MethodDevBrowserConsole, protocol.MethodDevBrowserNetwork, protocol.MethodDevBrowserReset,
		protocol.MethodDevBrowserClose, protocol.MethodDevControlStatus, protocol.MethodDevControlAcquire,
		protocol.MethodDevControlRelease, protocol.MethodDevArtifactList, protocol.MethodDevArtifactGet,
		protocol.MethodDevArtifactDelete:
		return true
	default:
		return false
	}
}

func isMissionMethod(method string) bool {
	switch method {
	case protocol.MethodTaskShow, protocol.MethodTaskList, protocol.MethodTaskPropose,
		protocol.MethodTaskRevise, protocol.MethodTaskAccept, protocol.MethodTaskAcceptSubmission,
		protocol.MethodTaskAbandon, protocol.MethodWorkerStart, protocol.MethodWorkerList,
		protocol.MethodWorkerInspect, protocol.MethodWorkerCancel, protocol.MethodWorkerRetry,
		protocol.MethodIntegrationPrepare, protocol.MethodIntegrationShow,
		protocol.MethodIntegrationVerify, protocol.MethodIntegrationRequestDelivery,
		protocol.MethodIntegrationDeliver,
		protocol.MethodMissionQuestionAsk, protocol.MethodMissionClarificationComplete,
		protocol.MethodMissionPlanShow, protocol.MethodMissionPlanSubmit:
		return true
	default:
		return false
	}
}
func rateLimitedResponse(line []byte) (protocol.Response, bool) {
	_, resp, _ := protocol.ParseRequest(line)
	if len(resp.ID) == 0 || bytes.Equal(bytes.TrimSpace(resp.ID), []byte("null")) {
		return protocol.Response{}, false
	}
	resp.Result = nil
	resp.Error = transportRateError()
	return resp, true
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
