package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// attachAckGrace is how long serveAttach waits for the PTY host to either
// fail fast or produce output before acknowledging the attach anyway.
const attachAckGrace = 200 * time.Millisecond

// serveAttach wires an aether-attach subsystem channel to the PTY host:
// one header line in, an ack, then a raw byte pipe. Geometry precedence is
// pty-req > header > 80x24; an attach without pty-req is forced read-only.
func (s *Server) serveAttach(ctx context.Context, member domain.MemberID, st *sessionState, ch ssh.Channel) {
	defer func() { _ = ch.Close() }()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	var req protocol.AttachRequest
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: protocol.CodeParse, Error: "parse error: " + uerr.Error()})
		return
	}
	if req.RunID == "" {
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: protocol.CodeInvalidParams, Error: "run_id is required"})
		return
	}
	if merr := s.checkMember(ctx, member); merr != nil {
		e := rpcError(merr)
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(req.RunID))
	if err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}

	cols, rows, hasPTY := st.geometry()
	if !hasPTY {
		cols, rows = req.Cols, req.Rows
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	readOnly := req.ReadOnly || !hasPTY

	conn := newAttachConn(ch, r, protocol.AttachResponse{OK: true, Cols: cols, Rows: rows})
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.cfg.PTY.Attach(ctx, run.ID, member, cols, rows, readOnly, conn, st.resize)
	}()

	var attachErr error
	returned := false
	select {
	case attachErr = <-errCh:
		returned = true
	case <-conn.first:
	case <-time.After(attachAckGrace):
		// The grace timer can race a failing Attach; prefer the error so
		// the client gets {"ok":false} instead of a bogus ack.
		select {
		case attachErr = <-errCh:
			returned = true
		default:
		}
	}
	if returned && attachErr != nil {
		if !conn.okSent() {
			e := rpcError(attachErr)
			_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		} else {
			sendExitStatus(ch, 1)
		}
		return
	}
	conn.sendOK()

	s.publishPresence(run, member, events.PresenceWatching)
	if !returned {
		attachErr = <-errCh
	}
	s.publishPresence(run, member, events.PresenceOnline)
	if attachErr == nil {
		// Session end: server closes with exit-status 0.
		sendExitStatus(ch, 0)
	} else {
		// Attach failed after the ack was already on the wire; a nonzero
		// exit-status is the remaining signal that distinguishes the
		// failure from a clean session end.
		sendExitStatus(ch, 1)
	}
}

func (s *Server) publishPresence(run *domain.Run, member domain.MemberID, state events.PresenceState) {
	_, _ = s.cfg.Bus.Publish(context.Background(), events.Event{
		SessionID: run.SessionID,
		RunID:     run.ID,
		ActorID:   member,
		Payload:   events.PresencePayload{State: state},
	})
}

// attachConn is the io.ReadWriter handed to PTYAttacher.Attach. It delays
// the {"ok":true} acknowledgment until just before the first PTY byte so
// negotiation output and stream bytes can never interleave.
type attachConn struct {
	ch    ssh.Channel
	r     *bufio.Reader
	ok    []byte
	mu    sync.Mutex
	sent  bool
	first chan struct{}
}

func newAttachConn(ch ssh.Channel, r *bufio.Reader, ok protocol.AttachResponse) *attachConn {
	line, _ := json.Marshal(ok)
	return &attachConn{ch: ch, r: r, ok: append(line, '\n'), first: make(chan struct{})}
}

func (c *attachConn) sendOK() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent {
		return
	}
	c.sent = true
	_, _ = c.ch.Write(c.ok)
	close(c.first)
}

func (c *attachConn) okSent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}

func (c *attachConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *attachConn) Write(p []byte) (int, error) {
	c.sendOK()
	return c.ch.Write(p)
}
