package sshd

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
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	maxRunSSHConns = 16
	// maxRunSSHSessions is OpenSSH's own MaxSessions default.
	maxRunSSHSessions = 10
	maxRunSSHForwards = 64
	maxRunSSHEnv      = 32
	maxRunSSHEnvBytes = 1024

	runSSHStopGrace    = 2 * time.Second
	runSSHStopTimeout  = 30 * time.Second
	runSSHStopRetries  = 5
	runSSHRelayTimeout = 15 * time.Second
)

// A session's shell is the one a run shell tab starts: bash, or sh in an
// image without it. A command goes to the same shell, not a login one.
const (
	runSSHShell   = `if command -v bash >/dev/null 2>&1; then exec bash -l; fi; exec sh -l`
	runSSHCommand = `if command -v bash >/dev/null 2>&1; then exec bash -c "$0"; fi; exec sh -c "$0"`
)

var (
	// What a stock sshd accepts from SendEnv.
	runSSHEnvName = regexp.MustCompile(`^(LANG|LC_[A-Z_]{1,32})$`)
	runSSHTerm    = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

// RunSSHService starts the processes behind a member's SSH connection to a
// run, each an owned execution in the run's live container. Both methods
// apply the same admission and call authorize inside it.
type RunSSHService interface {
	CheckRunExec(context.Context, domain.RunID, control.Principal, func() error) error
	StartRunExec(context.Context, domain.RunID, control.Principal, scheduler.RunExec, func() error) (runtime.ManagedExec, error)
}

// serveRunSSH admits a member to a run exactly as a writable shell tab is
// admitted, then speaks the SSH server protocol on the channel. The outer
// connection already authenticated the member, so the nested one asks for no
// credential; it presents the server's host key so the client can pin it.
func (s *Server) serveRunSSH(ctx context.Context, member domain.MemberID, ch subsystemConn) {
	defer func() { _ = ch.Close() }()
	handshake := time.AfterFunc(s.cfg.handshakeTimeout, func() { _ = ch.Close() })
	defer handshake.Stop()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	refuse := func(cause error) {
		perr := rpcError(cause)
		_ = writeJSONLine(ch, protocol.RunSSHResponse{Code: perr.Code, Error: perr.Message})
	}
	var req protocol.RunSSHRequest
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		refuse(&protocol.Error{Code: protocol.CodeParse, Message: "parse error: " + uerr.Error()})
		return
	}
	svc := s.cfg.Services.RunSSH
	if svc == nil {
		refuse(&protocol.Error{Code: protocol.CodeUnavailable, Message: "ssh into a run is unavailable on this server"})
		return
	}
	run, principal, authority, err := s.developmentAuthority(ctx, member, req.RunID)
	if errors.Is(err, store.ErrNotFound) {
		err = &protocol.Error{Code: protocol.CodeNotFound, Message: "run not found"}
	}
	if err != nil {
		refuse(err)
		return
	}
	authorize := func() error { return authority(ctx) }
	if err = svc.CheckRunExec(ctx, run.ID, principal, authorize); err != nil {
		refuse(err)
		return
	}
	release, first, err := s.claimRunSSH(run.ID, member)
	if err != nil {
		refuse(err)
		return
	}
	defer release()
	if first {
		if _, perr := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, ActorID: member,
			Payload: events.TimelinePayload{Kind: events.TimelineNote, Message: "connected over SSH"},
		}); perr != nil {
			slog.Warn("sshd: record ssh connection", "run", run.ID, "member", member, "error", perr)
		}
	}
	hostKey := string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey())))
	if err = writeJSONLine(ch, protocol.RunSSHResponse{OK: true, HostKey: hostKey}); err != nil {
		return
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(s.hostKey)
	nested, chans, reqs, err := ssh.NewServerConn(devexec.Stream{Reader: r, Writer: ch, Closer: ch}, cfg)
	if err != nil {
		return
	}
	handshake.Stop()
	defer func() { _ = nested.Close() }()
	// Like a terminal attach, a live connection holds a scheduled server
	// update back: restarting would drop it under whoever is using it.
	hold := s.cfg.Runs.HoldShell()
	defer hold()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &runSSHConn{s: s, svc: svc, run: run.ID, principal: principal, authorize: authorize}
	defer c.close()
	s.spawn(func() { c.revalidate(connCtx, nested) })
	// Every global request is refused, tcpip-forward included: no reverse
	// forwarding.
	s.spawn(func() { ssh.DiscardRequests(reqs) })
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			s.spawn(func() { c.session(connCtx, nc) })
		case "direct-tcpip":
			s.spawn(func() { c.forward(connCtx, nc) })
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

// claimRunSSH counts one SSH connection to run against the per-run cap.
// first reports that member had none open, which is when the connection is
// stamped on the timeline.
func (s *Server) claimRunSSH(run domain.RunID, member domain.MemberID) (release func(), first bool, err error) {
	s.runSSHMu.Lock()
	defer s.runSSHMu.Unlock()
	open := 0
	for _, n := range s.runSSHConns[run] {
		open += n
	}
	if open >= maxRunSSHConns {
		return nil, false, &protocol.Error{Code: protocol.CodeConflict,
			Message: fmt.Sprintf("run %s already has %d SSH connections open, the most one run takes", run, maxRunSSHConns)}
	}
	if s.runSSHConns[run] == nil {
		s.runSSHConns[run] = make(map[domain.MemberID]int)
	}
	first = s.runSSHConns[run][member] == 0
	s.runSSHConns[run][member]++
	return func() {
		s.runSSHMu.Lock()
		defer s.runSSHMu.Unlock()
		s.runSSHConns[run][member]--
		if s.runSSHConns[run][member] == 0 {
			delete(s.runSSHConns[run], member)
		}
		if len(s.runSSHConns[run]) == 0 {
			delete(s.runSSHConns, run)
		}
	}, first, nil
}

// runSSHConn is one nested SSH connection into a run.
type runSSHConn struct {
	s         *Server
	svc       RunSSHService
	run       domain.RunID
	principal control.Principal
	authorize func() error

	mu       sync.Mutex
	sessions int
	forwards int

	relayMu sync.Mutex
	relay   *runSSHRelay
	closed  bool
}

// runSSHRelay is the one process per connection that dials the run's
// loopback for every forward. It speaks the SSH channel protocol on its
// stdio, so forwards share it without blocking each other.
type runSSHRelay struct {
	exec runtime.ManagedExec
	conn ssh.Conn
	done chan struct{}
}

func (c *runSSHConn) claim(n *int, limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *n >= limit {
		return false
	}
	*n++
	return true
}

func (c *runSSHConn) unclaim(n *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*n--
}

func (c *runSSHConn) close() {
	c.relayMu.Lock()
	defer c.relayMu.Unlock()
	c.closed = true
	if c.relay != nil {
		_ = c.relay.conn.Close()
		c.stopExec(c.relay.exec)
		c.relay = nil
	}
}

// revalidate ends the connection once the member may no longer open it or
// the run has no container left. The admission is a snapshot; without this
// a member who loses Steer keeps a shell until they disconnect.
func (c *runSSHConn) revalidate(ctx context.Context, nested ssh.Conn) {
	ticker := time.NewTicker(c.s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.svc.CheckRunExec(ctx, c.run, c.principal, c.authorize); runSSHRevoked(err) {
				_ = nested.Close()
				return
			}
		}
	}
}

// runSSHRevoked separates an answer that ends a connection from a check that
// could not complete, such as a store read during a status change. A pause
// ends nothing: the connection's processes are frozen with the container
// and cannot be stopped until it resumes, when they carry on.
func runSSHRevoked(err error) bool {
	if errors.Is(err, scheduler.ErrRunPaused) {
		return false
	}
	return errors.Is(err, permissions.ErrDenied) || errors.Is(err, errMemberRemoved) ||
		errors.Is(err, errMemberPending) || errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, scheduler.ErrNoLiveEnvironment)
}

func (c *runSSHConn) session(ctx context.Context, nc ssh.NewChannel) {
	if !c.claim(&c.sessions, maxRunSSHSessions) {
		_ = nc.Reject(ssh.ResourceShortage, fmt.Sprintf("at most %d sessions per SSH connection", maxRunSSHSessions))
		return
	}
	defer c.unclaim(&c.sessions)
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	// A request left unread would stall every channel of the connection.
	defer func() { go ssh.DiscardRequests(reqs) }()
	defer func() { _ = ch.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		spec scheduler.RunExec
		exec runtime.ManagedExec
		done chan struct{}
	)
	// Keyed by name, so a client repeating a request cannot grow it.
	env := make(map[string]string)
	start := func(req *ssh.Request, argv []string) bool {
		reply(req, true)
		spec.Argv = argv
		for _, name := range slices.Sorted(maps.Keys(env)) {
			spec.Env = append(spec.Env, name+"="+env[name])
		}
		var startErr error
		if exec, startErr = c.svc.StartRunExec(ctx, c.run, c.principal, spec, c.authorize); startErr != nil {
			eol := "\n"
			if spec.TTY {
				eol = "\r\n"
			}
			_, _ = fmt.Fprintf(ch.Stderr(), "aether: %s%s", rpcError(startErr).Message, eol)
			sendExitStatus(ch, 255)
			return false
		}
		done = make(chan struct{})
		go func() {
			defer close(done)
			c.pump(ctx, ch, exec, spec.TTY)
		}()
		return true
	}
	for {
		select {
		case <-done:
			return
		case req, open := <-reqs:
			if !open {
				cancel()
				if done != nil {
					<-done
				}
				return
			}
			switch req.Type {
			case "pty-req":
				var p struct {
					Term          string
					Cols, Rows    uint32
					Width, Height uint32
					Modes         string
				}
				ok := done == nil && ssh.Unmarshal(req.Payload, &p) == nil
				if ok {
					spec.TTY, spec.Cols, spec.Rows = true, runSSHDimension(p.Cols, 80), runSSHDimension(p.Rows, 24)
					if runSSHTerm.MatchString(p.Term) {
						env["TERM"] = p.Term
					}
				}
				reply(req, ok)
			case "env":
				var p struct{ Name, Value string }
				ok := done == nil && ssh.Unmarshal(req.Payload, &p) == nil && runSSHEnvName.MatchString(p.Name) &&
					len(p.Value) <= maxRunSSHEnvBytes && !strings.ContainsRune(p.Value, 0)
				if _, known := env[p.Name]; ok && !known && len(env) >= maxRunSSHEnv {
					ok = false
				}
				if ok {
					env[p.Name] = p.Value
				}
				reply(req, ok)
			case "window-change":
				var p struct {
					Cols, Rows    uint32
					Width, Height uint32
				}
				ok := exec != nil && spec.TTY && ssh.Unmarshal(req.Payload, &p) == nil &&
					exec.Resize(ctx, runSSHDimension(p.Cols, 80), runSSHDimension(p.Rows, 24)) == nil
				reply(req, ok)
			case "shell":
				if done != nil {
					reply(req, false)
					continue
				}
				if !start(req, []string{"/bin/sh", "-c", runSSHShell}) {
					return
				}
			case "exec":
				var p struct{ Command string }
				if done != nil || ssh.Unmarshal(req.Payload, &p) != nil {
					reply(req, false)
					continue
				}
				if !start(req, []string{"/bin/sh", "-c", runSSHCommand, p.Command}) {
					return
				}
			case "subsystem":
				var p struct{ Name string }
				if done != nil || ssh.Unmarshal(req.Payload, &p) != nil || p.Name != "sftp" {
					reply(req, false)
					continue
				}
				spec.TTY = false
				if !start(req, []string{coordtransport.CLIPath, devexec.Command, devexec.SFTP}) {
					return
				}
			default:
				reply(req, false)
			}
		}
	}
}

func runSSHDimension(n, fallback uint32) uint {
	if n == 0 || n > 65535 {
		return uint(fallback)
	}
	return uint(n)
}

// pump wires one owned execution to its session channel and reports its
// exit status once its output has drained. A cancelled ctx stops the
// execution instead: the client is gone or may no longer be here.
func (c *runSSHConn) pump(ctx context.Context, ch ssh.Channel, exec runtime.ManagedExec, tty bool) {
	att := exec.Attachment()
	stop := context.AfterFunc(ctx, func() { c.stopExec(exec) })
	go func() {
		stdin := att.Stdin()
		_, _ = io.Copy(stdin, ch)
		// A terminal has no half-close; its process reads end of input
		// from the keys the client sends.
		if !tty {
			_ = stdin.Close()
		}
	}()
	var output sync.WaitGroup
	output.Go(func() { _, _ = io.Copy(ch, att.Stdout()) })
	output.Go(func() { _, _ = io.Copy(ch.Stderr(), att.Stderr()) })
	output.Wait()
	if !stop() {
		return
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runSSHStopTimeout)
	defer cancel()
	// No status is sent for an execution whose end cannot be read, so the
	// client reports a lost session rather than a success.
	if status, err := exec.Wait(waitCtx); err == nil {
		sendExitStatus(ch, status.Code)
	}
	_ = exec.Detach()
}

// stopExec ends an execution and everything it started, then releases its
// streams. A paused container refuses the stop, and its processes would
// outlive their session once the run resumed, so a stop that fails is tried
// again: not while the run stays paused, and a bounded number of times
// otherwise.
func (c *runSSHConn) stopExec(exec runtime.ManagedExec) {
	err := stopRunSSHExec(exec)
	_ = exec.Detach()
	if err == nil {
		return
	}
	slog.Warn("sshd: stop ssh execution; will retry", "run", c.run, "exec", exec.Identity().ExecID, "error", err)
	go func() {
		ticker := time.NewTicker(c.s.cfg.revalidateInterval)
		defer ticker.Stop()
		shutdown := c.s.authCtx().Done()
		for attempts := 0; attempts < runSSHStopRetries; {
			select {
			case <-shutdown:
				return
			case <-ticker.C:
			}
			if c.s.cfg.Runs.Paused(c.run) {
				continue
			}
			attempts++
			if err = stopRunSSHExec(exec); err == nil {
				return
			}
		}
		slog.Error("sshd: ssh execution left running", "run", c.run, "exec", exec.Identity().ExecID, "error", err)
	}()
}

func stopRunSSHExec(exec runtime.ManagedExec) error {
	ctx, cancel := context.WithTimeout(context.Background(), runSSHStopTimeout)
	defer cancel()
	// A container that is already gone took the execution with it.
	if _, err := exec.Stop(ctx, runSSHStopGrace); err != nil && !errors.Is(err, runtime.ErrExecUnavailable) {
		return err
	}
	return nil
}

// forward serves ssh -L and -D: a direct-tcpip channel to a port on the
// run's own loopback, dialed from inside its network namespace.
func (c *runSSHConn) forward(ctx context.Context, nc ssh.NewChannel) {
	var payload directTCPIPPayload
	if ssh.Unmarshal(nc.ExtraData(), &payload) != nil {
		rejectDirectTCPIP(nc, ssh.Prohibited, "invalid port forwarding payload")
		return
	}
	if payload.DestPort == 0 || payload.DestPort > 65535 {
		rejectDirectTCPIP(nc, ssh.Prohibited, "destination port must be between 1 and 65535")
		return
	}
	if _, err := devexec.LoopbackAddrs(payload.DestHost); err != nil {
		rejectDirectTCPIP(nc, ssh.Prohibited, err.Error())
		return
	}
	if !c.claim(&c.forwards, maxRunSSHForwards) {
		rejectDirectTCPIP(nc, ssh.ResourceShortage, fmt.Sprintf("at most %d forwarded connections per SSH connection", maxRunSSHForwards))
		return
	}
	defer c.unclaim(&c.forwards)
	if err := c.svc.CheckRunExec(ctx, c.run, c.principal, c.authorize); err != nil {
		rejectDirectTCPIP(nc, ssh.Prohibited, rpcError(err).Message)
		return
	}
	relay, err := c.relayConn(ctx)
	if err != nil {
		rejectDirectTCPIP(nc, ssh.ConnectionFailed, rpcError(err).Message)
		return
	}
	upstream, upstreamReqs, err := relay.OpenChannel("direct-tcpip", nc.ExtraData())
	if err != nil {
		reason, message := ssh.ConnectionFailed, err.Error()
		var refused *ssh.OpenChannelError
		if errors.As(err, &refused) {
			reason, message = refused.Reason, refused.Message
		}
		rejectDirectTCPIP(nc, reason, message)
		return
	}
	go ssh.DiscardRequests(upstreamReqs)
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.s.spawn(func() {
		ssh.DiscardRequests(reqs)
		cancel()
	})
	proxyDirectTCPIP(ctx, upstream, ch)
}

func (c *runSSHConn) relayConn(ctx context.Context) (ssh.Conn, error) {
	c.relayMu.Lock()
	defer c.relayMu.Unlock()
	if c.closed {
		return nil, errors.New("the SSH connection is closing")
	}
	if c.relay != nil {
		select {
		case <-c.relay.done:
			c.stopExec(c.relay.exec)
			c.relay = nil
		default:
			return c.relay.conn, nil
		}
	}
	exec, err := c.svc.StartRunExec(ctx, c.run, c.principal,
		scheduler.RunExec{Argv: []string{coordtransport.CLIPath, devexec.Command, devexec.Forward}}, c.authorize)
	if err != nil {
		return nil, err
	}
	att := exec.Attachment()
	handshake := time.AfterFunc(runSSHRelayTimeout, func() { _ = att.Close() })
	conn, chans, reqs, err := ssh.NewClientConn(devexec.Stream{Reader: att.Stdout(), Writer: att.Stdin(), Closer: att}, "", &ssh.ClientConfig{
		// The transport is the stdio of an execution this server started,
		// so the relay's throwaway key identifies nothing further.
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
	})
	handshake.Stop()
	if err != nil {
		c.stopExec(exec)
		return nil, fmt.Errorf("start the port forwarding relay: %w", err)
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		for nc := range chans {
			_ = nc.Reject(ssh.Prohibited, "the relay opens no channels")
		}
	}()
	relay := &runSSHRelay{exec: exec, conn: conn, done: make(chan struct{})}
	go func() {
		_ = conn.Wait()
		close(relay.done)
	}()
	c.relay = relay
	return conn, nil
}
