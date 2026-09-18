package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// interactiveControlQueueSize bounds control replies for one attach. A
// stalled client can therefore only retain a small amount of work before its
// own attach is canceled.
const interactiveControlQueueSize = 16

var errInteractiveControlQueueFull = errors.New("sshd: interactive control queue full")

// attachConn is the ordered io.ReadWriter handed to PTY host attachments.
type attachConn struct {
	ch                subsystemConn
	r                 *bufio.Reader
	ack               any
	framed            bool
	interactive       bool
	beforeAck         func() error
	inputHandler      func(protocol.DashAttachControl) ([]byte, error)
	controlHandler    func(protocol.DashAttachControl)
	errorHandler      func(error)
	inputErrorHandler func(protocol.DashAttachControl, error)
	pending           []byte
	pendingControl    protocol.DashAttachControl
	pendingInputError bool
	replayDone        chan struct{}
	controlQueue      chan protocol.DashAttachControl
	controlCtx        context.Context
	controlCancel     context.CancelCauseFunc
	replayOnce        sync.Once
	mu                sync.Mutex
	sent              bool
	first             chan struct{}
	writeErr          error
}

func newAttachConn(ch subsystemConn, r *bufio.Reader, ack any, framed bool, beforeAck func() error) *attachConn {
	switch response := ack.(type) {
	case *protocol.AttachResponse:
		response.Framed = framed
	case *protocol.TerminalResponse:
		response.Framed = framed
	}
	return &attachConn{
		ch: ch, r: r, ack: ack, framed: framed, beforeAck: beforeAck,
		first: make(chan struct{}),
	}
}

// SetGeometry takes the session's PTY size from the host. Before the ack
// goes out it is what the ack reports; afterwards framed clients receive a
// geometry record in the same stream as output. Raw clients receive no
// geometry bytes.
func (c *attachConn) SetGeometry(cols, rows uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sent {
		switch ack := c.ack.(type) {
		case *protocol.AttachResponse:
			ack.Cols, ack.Rows = cols, rows
		case *protocol.TerminalResponse:
			ack.Cols, ack.Rows = cols, rows
		}
		return
	}
	if c.framed && c.writeErr == nil {
		if err := protocol.WriteTerminalGeometry(c.ch, cols, rows); err != nil {
			c.writeErr = err
			slog.Warn("sshd: write terminal geometry", "error", err)
			c.ch.exit(1)
			_ = c.ch.Close()
		}
	}
}

// SetResume records how the session answered a resume. It lands in the
// ack, so it must arrive before WriteReplay sends it - Host.Attach calls
// it straight after the client joins, which is where both are decided.
func (c *attachConn) SetResume(cursor uint64, resumed bool, resumeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent {
		return
	}
	if ack, ok := c.ack.(*protocol.AttachResponse); ok {
		ack.Cursor, ack.Resumed, ack.ResumeID = cursor, resumed, resumeID
	}
}

func (c *attachConn) sendOK() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendOKLocked()
}

func (c *attachConn) sendOKLocked() {
	if c.sent {
		return
	}
	if c.beforeAck != nil {
		beforeAck := c.beforeAck
		c.beforeAck = nil
		if err := beforeAck(); err != nil {
			c.writeErr = err
			return
		}
	}
	c.sent = true
	c.writeErr = writeJSONLine(c.ch, c.ack)
	close(c.first)
}

func (c *attachConn) setReplayLocked(n int) {
	switch ack := c.ack.(type) {
	case *protocol.AttachResponse:
		ack.Replay = n
	case *protocol.TerminalResponse:
		ack.Replay = n
	}
}

func (c *attachConn) WriteReplay(replay io.Reader, bytes int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setReplayLocked(bytes)
	c.sendOKLocked()
	if c.writeErr != nil {
		return c.writeErr
	}
	if err := writeTerminalReplay(c.ch, replay, bytes, c.framed); err != nil {
		c.writeErr = err
		return err
	}
	if c.replayDone != nil {
		c.replayOnce.Do(func() { close(c.replayDone) })
	}
	return nil
}

func (c *attachConn) startControlWriter(ctx context.Context, cancel context.CancelCauseFunc) {
	if !c.interactive || !c.framed {
		return
	}
	c.replayDone = make(chan struct{})
	c.controlQueue = make(chan protocol.DashAttachControl, interactiveControlQueueSize)
	c.controlCtx = ctx
	c.controlCancel = cancel
}

func (c *attachConn) controlWriter() {
	ctx := c.controlCtx
	select {
	case <-c.first:
	case <-ctx.Done():
		return
	}
	select {
	case <-c.replayDone:
	case <-ctx.Done():
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case control := <-c.controlQueue:
			c.mu.Lock()
			var err error
			if c.writeErr != nil {
				err = c.writeErr
			} else {
				err = protocol.WriteTerminalControl(c.ch, control)
			}
			c.mu.Unlock()
			if err != nil {
				c.controlCancel(err)
				return
			}
		}
	}
}

func writeTerminalReplay(w io.Writer, replay io.Reader, bytes int, framed bool) error {
	if !framed {
		written, err := io.CopyN(w, replay, int64(bytes))
		if err == nil && written != int64(bytes) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	buf := make([]byte, 32<<10)
	remaining := bytes
	for remaining > 0 {
		read := len(buf)
		if read > remaining {
			read = remaining
		}
		if _, err := io.ReadFull(replay, buf[:read]); err != nil {
			return err
		}
		if _, err := protocol.WriteTerminalOutput(w, buf[:read]); err != nil {
			return err
		}
		remaining -= read
	}
	return nil
}

func (c *attachConn) okSent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}
func (c *attachConn) okWritten() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent && c.writeErr == nil
}

// sendControl queues one control record for the attach-lifetime writer. The
// bounded channel is independent of the output mutex, so enqueueing never
// waits on transport I/O.
func (c *attachConn) sendControl(control protocol.DashAttachControl) error {
	if !c.framed || !c.interactive {
		return nil
	}
	if c.controlQueue == nil || c.controlCtx == nil {
		return io.ErrClosedPipe
	}
	select {
	case <-c.controlCtx.Done():
		return context.Cause(c.controlCtx)
	default:
	}
	select {
	case <-c.controlCtx.Done():
		return context.Cause(c.controlCtx)
	case c.controlQueue <- control:
		return nil
	default:
		c.controlCancel(errInteractiveControlQueueFull)
		return errInteractiveControlQueueFull
	}
}

func (c *attachConn) Read(p []byte) (int, error) {
	if !c.interactive {
		return c.r.Read(p)
	}
	for len(c.pending) == 0 {
		line, err := protocol.ReadLine(c.r)
		if err != nil {
			return 0, err
		}
		var control protocol.DashAttachControl
		if err := json.Unmarshal(line, &control); err != nil {
			if c.errorHandler != nil {
				c.errorHandler(err)
			}
			continue
		}
		switch control.Type {
		case protocol.DashAttachInput:
			if c.inputHandler == nil {
				continue
			}
			data, err := c.inputHandler(control)
			if err != nil {
				if c.inputErrorHandler != nil {
					c.inputErrorHandler(control, err)
				} else if c.errorHandler != nil {
					c.errorHandler(err)
				}
				continue
			}
			c.pending = data
			c.pendingControl = control
			c.pendingInputError = false
		case protocol.DashAttachControlFrame:
			if c.controlHandler != nil {
				c.controlHandler(control)
			}
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *attachConn) pendingInput() (protocol.DashAttachControl, bool) {
	if c.pendingControl.ControlGeneration == 0 {
		return protocol.DashAttachControl{}, false
	}
	return c.pendingControl, true
}

func (c *attachConn) reportPendingInputError(err error) {
	if c.pendingInputError {
		return
	}
	c.pendingInputError = true
	if c.inputErrorHandler != nil {
		c.inputErrorHandler(c.pendingControl, err)
	}
}

func (c *attachConn) Close() error { return c.ch.Close() }

func (c *attachConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendOKLocked()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if c.framed {
		return protocol.WriteTerminalOutput(c.ch, p)
	}
	return c.ch.Write(p)
}

// revokeOnCurrentControlChange keeps one bounded monitor for an interactive
// attach's local lease. Unlike the initial raw watcher, the interactive
// transport can acquire more than once; each tick follows the lease currently
// held by this attach and fences only that exact session and generation.
func (s *Server) revokeOnCurrentControlChange(ctx context.Context, revoke context.CancelCauseFunc, run string, currentLease func() (*attachControlLease, func())) {
	if s.cfg.Control == nil {
		return
	}
	ticker := time.NewTicker(s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lease, _ := currentLease()
			if lease == nil {
				continue
			}
			if err := s.cfg.Control.Validate(run, lease.sessionID, lease.generation); err == nil {
				continue
			}
			current, fence := currentLease()
			if current == nil ||
				current.sessionID != lease.sessionID ||
				current.generation != lease.generation {
				continue
			}
			if fence != nil {
				fence()
			} else {
				revoke(errAttachControlRevoked)
				return
			}
		}
	}
}
