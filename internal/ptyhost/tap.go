package ptyhost

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TapOutput opens a read-only stream of the run's raw PTY output for
// in-process consumers (the adapter manager). The tap starts with the replay
// of the current PTY session's output, so a consumer attaching just after
// StartSession misses nothing and never sees again what an earlier process
// of the run, before a restart or a move to another account, printed. On a
// live session it is a regular read-only client: it never affects terminal
// geometry, and a tap that stops reading is detached (its reader fails)
// rather than ever blocking the session pump. On a session whose process
// already ended or was stopped, it replays that session's output from the
// transcript and ends. The reader returns io.EOF when the session ends or is
// stopped.
func (h *Host) TapOutput(run domain.RunID) (io.ReadCloser, error) {
	key := RunSession(run)
	s := h.lookup(key)
	if s == nil {
		return nil, ErrNoSession
	}
	pr, pw := io.Pipe()
	c := newClient(tapConn{pw}, AttachClient{ReadOnly: true})
	c.sessionReplay = true
	err := s.addClient(c)
	if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrNoSession) {
		return h.endedSessionOutput(key, s.transcriptStart)
	}
	if err != nil {
		return nil, err
	}
	go func() {
		err := c.writeLoop()
		s.removeClient(c)
		_ = pw.CloseWithError(err) // nil means a clean end: reader gets io.EOF
	}()
	return &tapReader{pr: pr, detach: func() { s.removeClient(c) }}, nil
}

// endedSessionOutput replays, from the transcript, the output of the session
// whose transcript opened at incarnation start.
func (h *Host) endedSessionOutput(key SessionKey, start int64) (io.ReadCloser, error) {
	path := h.transcriptPath(key)
	lifecycle := acquireTranscriptLifecycle(path)
	lifecycle.entry.mu.Lock()
	defer func() {
		lifecycle.entry.mu.Unlock()
		lifecycle.release()
	}()
	segments, err := collectCastHeaders(path)
	if err != nil {
		return nil, err
	}
	first := slices.IndexFunc(segments, func(segment castSegment) bool { return segment.incarnation >= start })
	if first < 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	return openCastReplay(segments[first:], nil)
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
