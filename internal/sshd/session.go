package sshd

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// sessionState is the per-session-channel state shared between the
// request loop and a subsystem handler.
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

// handleSession serves one "session" channel: exactly one git exec or one
// aether subsystem, plus pty-req / window-change bookkeeping for attach.
// The context handed to handlers is canceled when the channel closes (the
// request loop ends), so subsystem handlers observe channel teardown even
// when they are not blocked on channel I/O.
func (s *Server) handleSession(ctx context.Context, member domain.MemberID, nc ssh.NewChannel) {
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
				handler = func() { s.serveControl(ctx, member, ch) }
			case protocol.SubsystemEvents:
				handler = func() { s.serveEvents(ctx, member, ch) }
			case protocol.SubsystemAttach:
				handler = func() { s.serveAttach(ctx, member, st, ch) }
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
			// shell and everything else are rejected.
			reply(req, false)
		}
	}
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

// parseGitCommand recognizes the two git transport commands, in both the
// hyphenated and two-word spellings, and extracts the workspace ID from
// the path (optional single quotes, leading slash, and .git suffix).
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

// runGitCommand validates the caller and workspace, then streams the pack
// protocol through the git transport seam.
func (s *Server) runGitCommand(ctx context.Context, member domain.MemberID, ch ssh.Channel, op, wsID string) {
	defer func() { _ = ch.Close() }()
	if err := s.checkMember(ctx, member); err != nil {
		_, _ = fmt.Fprintf(ch.Stderr(), "aether: %v\n", err)
		sendExitStatus(ch, 128)
		return
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(wsID))
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
