package ptyhost

import (
	"io"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TapOutput opens a read-only stream of the run's raw PTY output for
// in-process consumers (the adapter manager). The tap is a regular
// read-only session client: it starts with the scrollback replay (so a
// consumer attaching just after StartSession misses nothing), never
// affects terminal geometry, and is bounded by the same slow-client
// policy as remote attachments - a tap that stops reading is detached
// (its reader fails) rather than ever blocking the session pump. The
// reader returns io.EOF when the session ends or is stopped.
func (h *Host) TapOutput(run domain.RunID) (io.ReadCloser, error) {
	s := h.lookup(run)
	if s == nil {
		return nil, ErrNoSession
	}
	pr, pw := io.Pipe()
	c := newClient(tapConn{pw}, true, 0, 0)
	if err := s.addClient(c); err != nil {
		return nil, err
	}
	go func() {
		err := c.writeLoop()
		s.removeClient(c)
		_ = pw.CloseWithError(err) // nil means a clean end: reader gets io.EOF
	}()
	return &tapReader{pr: pr, detach: func() { s.removeClient(c) }}, nil
}

// tapConn adapts the pipe's write end to the io.ReadWriter a client
// expects. Read is never called: taps have no keystroke side.
type tapConn struct {
	pw *io.PipeWriter
}

func (t tapConn) Write(p []byte) (int, error) { return t.pw.Write(p) }
func (t tapConn) Read([]byte) (int, error)    { return 0, io.EOF }

type tapReader struct {
	pr     *io.PipeReader
	detach func()
	once   sync.Once
}

func (t *tapReader) Read(p []byte) (int, error) { return t.pr.Read(p) }

// Close detaches the tap from the session. Closing the read side first
// unblocks a write loop mid-Write; detach then removes the client.
func (t *tapReader) Close() error {
	t.once.Do(func() {
		_ = t.pr.Close()
		t.detach()
	})
	return nil
}
