package sshd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type sessionState struct {
	mu         sync.Mutex
	hasPTY     bool
	cols, rows uint
	resize     chan [2]uint
}

func (st *sessionState) setPTY(cols, rows uint) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.hasPTY = true
	st.cols, st.rows = cols, rows
}

func (st *sessionState) geometry() (cols, rows uint, hasPTY bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cols, st.rows, st.hasPTY
}

// handleSession serves one session channel: one git exec or one aether
// subsystem. The handler context is canceled when the channel closes, so
// handlers see teardown even when not blocked on channel I/O.
func (s *Server) handleSession(ctx context.Context, member domain.MemberID, nc ssh.NewChannel, abortConn func()) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := &sessionState{resize: make(chan [2]uint, 16)}
	started := false

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term          string
				Cols, Rows    uint32
				Width, Height uint32
				Modes         string
			}
			ok := ssh.Unmarshal(req.Payload, &p) == nil
			if ok {
				st.setPTY(uint(p.Cols), uint(p.Rows))
			}
			reply(req, ok)
		case "window-change":
			var p struct {
				Cols, Rows    uint32
				Width, Height uint32
			}
			ok := ssh.Unmarshal(req.Payload, &p) == nil
			if ok {
				st.setPTY(uint(p.Cols), uint(p.Rows))
				select {
				case st.resize <- [2]uint{uint(p.Cols), uint(p.Rows)}:
				default:
				}
			}
			reply(req, ok)
		case "exec":
			var p struct{ Command string }
			if started || ssh.Unmarshal(req.Payload, &p) != nil {
				reply(req, false)
				continue
			}
			op, wsID, ok := parseGitCommand(p.Command)
			if !ok {
				reply(req, false)
				continue
			}
			started = true
			reply(req, true)
			s.spawn(func() { s.runGitCommand(ctx, member, ch, op, wsID) })
		case "subsystem":
			var p struct{ Name string }
			if started || ssh.Unmarshal(req.Payload, &p) != nil {
				reply(req, false)
				continue
			}
			var handler func()
			switch p.Name {
			case protocol.SubsystemControl:
				handler = func() { s.serveControl(ctx, member, ch, abortConn) }
			case protocol.SubsystemEvents:
				handler = func() { s.serveEvents(ctx, member, sshConn{Channel: ch, abort: abortConn}) }
			case protocol.SubsystemAttach:
				handler = func() { s.serveAttach(ctx, member, st, sshConn{Channel: ch, abort: abortConn}) }
			case protocol.SubsystemACP:
				handler = func() { s.serveACP(ctx, member, sshConn{Channel: ch, abort: abortConn}) }
			case protocol.SubsystemTerminal:
				handler = func() { s.serveTerminal(ctx, member, st, sshConn{Channel: ch, abort: abortConn}) }
			case protocol.SubsystemDevBrowser:
				handler = func() { s.serveDevelopmentBrowser(ctx, member, sshConn{Channel: ch, abort: abortConn}) }
			case protocol.SubsystemDevArtifact:
				handler = func() { s.serveDevelopmentArtifact(ctx, member, sshConn{Channel: ch, abort: abortConn}) }
			case protocol.SubsystemSync:
				handler = func() { s.serveSync(ctx, member, ch) }
			}
			if handler == nil {
				reply(req, false)
				continue
			}
			started = true
			reply(req, true)
			s.spawn(handler)
		default:
			reply(req, false)
		}
	}
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

// parseGitCommand accepts hyphenated and two-word spellings; the path may
// carry single quotes, a leading slash and a .git suffix.
func parseGitCommand(cmd string) (op, wsID string, ok bool) {
	fields := strings.Fields(cmd)
	var path string
	switch {
	case len(fields) == 2 && (fields[0] == "git-upload-pack" || fields[0] == "git-receive-pack"):
		op = strings.TrimPrefix(fields[0], "git-")
		path = fields[1]
	case len(fields) == 3 && fields[0] == "git" && (fields[1] == "upload-pack" || fields[1] == "receive-pack"):
		op = fields[1]
		path = fields[2]
	default:
		return "", "", false
	}
	path = strings.Trim(path, "'")
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" || strings.ContainsAny(path, "/'\\") {
		return "", "", false
	}
	return op, path, true
}

func (s *Server) runGitCommand(ctx context.Context, member domain.MemberID, ch ssh.Channel, op, wsID string) {
	defer func() { _ = ch.Close() }()
	if err := s.checkMember(ctx, member); err != nil {
		_, _ = fmt.Fprintf(ch.Stderr(), "aether: %v\n", err)
		sendExitStatus(ch, 128)
		return
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(wsID))
	if err == nil {
		lock := s.workspaceLock(ws.ID)
		lock.RLock()
		defer lock.RUnlock()
		// Resolve before allocating a gate, then revalidate under it so a
		// completed deletion cannot be followed by lazy repo creation.
		ws, err = s.cfg.Store.GetWorkspace(ctx, ws.ID)
	}
	if err != nil {
		_, _ = fmt.Fprintf(ch.Stderr(), "aether: workspace %q: %v\n", wsID, err)
		sendExitStatus(ch, 128)
		return
	}
	// receive-pack writes to the workspace repository, so it is the Push
	// capability; upload-pack is a read and stays open to every member.
	var code int
	if op == "upload-pack" {
		code, err = s.cfg.Git.UploadPack(ctx, ws.ID, ch, ch, ch.Stderr())
	} else {
		if perr := s.checkPush(ctx, member); perr != nil {
			_, _ = fmt.Fprintf(ch.Stderr(), "aether: %v\n", perr)
			sendExitStatus(ch, 128)
			return
		}
		code, err = s.cfg.Git.ReceivePack(ctx, ws.ID, ch, ch, ch.Stderr())
	}
	if err != nil {
		_, _ = fmt.Fprintf(ch.Stderr(), "aether: git %s: %v\n", op, err)
		if code == 0 {
			code = 128
		}
	}
	sendExitStatus(ch, code)
}

func sendExitStatus(ch ssh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
}

// subsystemConn is the SSH session channel, or an in-memory pipe for the
// in-process client. Wire contract: one header line in, one ack line out,
// then raw bytes or framed terminal records, plus exit status.
type subsystemConn interface {
	io.ReadWriteCloser
	exit(status int)
}

const sshChannelCloseTimeout = time.Second

type sshConn struct {
	ssh.Channel
	abort func()
}

// Status and close share the transport's packet writer with every channel,
// so abort the transport only if they stop progressing.
func (c sshConn) exit(status int) {
	timer := time.AfterFunc(sshChannelCloseTimeout, c.abort)
	defer timer.Stop()
	sendExitStatus(c.Channel, status)
}

func (c sshConn) Close() error {
	timer := time.AfterFunc(sshChannelCloseTimeout, c.abort)
	defer timer.Stop()
	return c.Channel.Close()
}
