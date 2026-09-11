package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Local is an in-process client of one member's control channel and
// subsystems: the transport behind the server-hosted dashboard. Each
// stream runs the handler an SSH session channel would run, over an
// in-memory pipe that speaks the same header, ack, bytes and exit-status
// contract, so replay, revocation and steer checks have one
// implementation. The member is whoever the caller identified; Local
// trusts it the way handleConn trusts a completed handshake.
type Local struct {
	s      *Server
	member domain.MemberID
}

// Local returns the in-process client acting as member.
func (s *Server) Local(member domain.MemberID) *Local {
	return &Local{s: s, member: member}
}

// Call performs one control-channel method call.
func (l *Local) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	return l.s.dispatch(ctx, l.member, method, params)
}

// Events opens the events subsystem: the returned reader carries one
// event line per protocol.Event once the subscription is acknowledged. A
// refused subscription comes back as *protocol.Error with the server's
// code.
func (l *Local) Events(ctx context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error) {
	var ack protocol.SubscribeResponse
	stream, err := l.open(ctx, req, &ack, nil, func(ctx context.Context, ch subsystemConn) {
		l.s.serveEvents(ctx, l.member, ch)
	})
	if err != nil {
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, &protocol.Error{Code: ack.Code, Message: ack.Error}
	}
	return stream, nil
}

// Attach opens the attach subsystem for req and returns the resizable
// terminal alongside the server's ack. A refused ack is returned with the
// error so callers can forward its code. The requested geometry stands in
// for the pty-req an SSH client sends, so a header without one attaches
// exactly as the CLI does.
func (l *Local) Attach(ctx context.Context, req protocol.AttachRequest) (*LocalTerminal, protocol.AttachResponse, error) {
	var ack protocol.AttachResponse
	st := &sessionState{resize: make(chan [2]uint, 16)}
	st.setPTY(req.Cols, req.Rows)
	geometry := make(chan [2]uint, 1)
	stream, err := l.open(ctx, req, &ack, geometry, func(ctx context.Context, ch subsystemConn) {
		l.s.serveAttach(ctx, l.member, st, ch)
	})
	if err != nil {
		return nil, ack, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, ack, fmt.Errorf("sshd: attach: %s", ack.Error)
	}
	return &LocalTerminal{localStream: stream, st: st, geometry: geometry}, ack, nil
}

// Terminal opens the member's persistent environment terminal and returns
// its acknowledged PTY stream, geometry handled as in Attach.
func (l *Local) Terminal(ctx context.Context, req protocol.TerminalRequest) (*LocalTerminal, protocol.TerminalResponse, error) {
	var ack protocol.TerminalResponse
	st := &sessionState{resize: make(chan [2]uint, 16)}
	st.setPTY(req.Cols, req.Rows)
	geometry := make(chan [2]uint, 1)
	stream, err := l.open(ctx, req, &ack, geometry, func(ctx context.Context, ch subsystemConn) {
		l.s.serveTerminal(ctx, l.member, st, ch)
	})
	if err != nil {
		return nil, ack, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, ack, fmt.Errorf("sshd: terminal: %s", ack.Error)
	}
	return &LocalTerminal{localStream: stream, st: st, geometry: geometry}, ack, nil
}

// open starts serve on the server end of a fresh pipe, writes the header
// line, and reads the ack line into ack; the returned stream carries the
// bytes after it. geometry receives the session resizes the handler
// reports, which an SSH client would read as window-change requests; nil
// for a stream that has no PTY. The handler is tracked like a channel
// handler: the server's Close ends it by closing its end of the pipe and
// waits for it.
func (l *Local) open(ctx context.Context, header, ack any, geometry chan [2]uint, serve func(context.Context, subsystemConn)) (*localStream, error) {
	line, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	server, client := net.Pipe()
	ch := &pipeConn{Conn: server, sizes: geometry}
	if !l.s.trackConn(server) || !l.s.beginHandler() {
		_ = server.Close()
		_ = client.Close()
		return nil, errors.New("sshd: server closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer l.s.wg.Done()
		defer l.s.untrackConn(server)
		serve(ctx, ch)
	}()
	stream := &localStream{conn: client, server: ch, cancel: cancel}
	if _, werr := client.Write(append(line, '\n')); werr != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("sshd: write header: %w", werr)
	}
	br := bufio.NewReader(client)
	ackLine, err := protocol.ReadLine(br)
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("sshd: read ack: %w", err)
	}
	if uerr := json.Unmarshal(ackLine, ack); uerr != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("sshd: decode ack: %w", uerr)
	}
	stream.r = br
	return stream, nil
}

// pipeConn is the server end of an in-process subsystem stream. The exit
// status is recorded before the handler closes the pipe, so the client
// end reads it once it sees EOF.
type pipeConn struct {
	net.Conn
	status atomic.Int32
	// sizes carries the session geometry the handler reports, the way an
	// SSH channel carries a window-change request. One slot, latest wins:
	// only the size the session is now means anything.
	sizes chan [2]uint
}

func (c *pipeConn) exit(status int) { c.status.Store(int32(status)) }

func (c *pipeConn) geometry(cols, rows uint) {
	if c.sizes == nil {
		return
	}
	// Only one goroutine reports a session's geometry at a time, so
	// dropping whatever is unread and leaving the newest size cannot lose
	// the last word.
	select {
	case <-c.sizes:
	default:
	}
	select {
	case c.sizes <- [2]uint{cols, rows}:
	default:
	}
}

// localStream is the client end: the bytes after the ack, and the exit
// status the handler ended with, surfaced as *protocol.RemoteExitError
// after EOF the way an SSH session channel's exit-status is.
type localStream struct {
	r      *bufio.Reader
	conn   net.Conn
	server *pipeConn
	cancel context.CancelFunc
	once   sync.Once
}

func (t *localStream) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if errors.Is(err, io.EOF) {
		if status := t.server.status.Load(); status != 0 {
			return n, &protocol.RemoteExitError{Status: int(status)}
		}
	}
	return n, err
}

func (t *localStream) Write(p []byte) (int, error) { return t.conn.Write(p) }

// Close ends the stream: the handler's context is canceled, which is what
// unsubscribes an events stream, and the pipe is closed under it.
func (t *localStream) Close() error {
	t.once.Do(func() {
		t.cancel()
		_ = t.conn.Close()
	})
	return nil
}

// LocalTerminal is an in-process PTY attach; Resize is the SSH
// window-change request, and Geometry is the same request arriving the
// other way.
type LocalTerminal struct {
	*localStream
	st       *sessionState
	geometry chan [2]uint
}

// Geometry reports the sizes the session's PTY takes while this attach is
// open, so a client that follows the session can redraw at them.
func (t *LocalTerminal) Geometry() <-chan [2]uint { return t.geometry }

// Resize adjusts the PTY to cols by rows.
func (t *LocalTerminal) Resize(cols, rows uint) error {
	t.st.setPTY(cols, rows)
	select {
	case t.st.resize <- [2]uint{cols, rows}:
	default:
	}
	return nil
}
