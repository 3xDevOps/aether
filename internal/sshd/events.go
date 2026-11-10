package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// serveEvents bridges a bus subscription onto an aether-events subsystem
// channel: one SubscribeRequest line in, an ack, then Event lines out. On
// a buffer drop the channel is closed so the client re-subscribes from its
// last seen cursor.
func (s *Server) serveEvents(ctx context.Context, member domain.MemberID, ch subsystemConn) {
	defer func() {
		ch.exit(0)
		_ = ch.Close()
	}()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 16<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	var req protocol.SubscribeRequest
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		_ = writeJSONLine(ch, protocol.SubscribeResponse{OK: false, Code: protocol.CodeParse, Error: "parse error: " + uerr.Error()})
		return
	}
	if merr := s.checkMember(ctx, member); merr != nil {
		e := rpcError(merr)
		_ = writeJSONLine(ch, protocol.SubscribeResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	access := &streamAccess{s: s, member: member}
	access.load(ctx)
	if ws := domain.WorkspaceID(req.WorkspaceID); ws != "" && ws != events.ServerScope && !access.actor.Reaches(ws) {
		e := rpcError(permissions.ErrNoAccess)
		_ = writeJSONLine(ch, protocol.SubscribeResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	// The stream gets its own cancel so the membership re-check below can
	// end it: a member removed or set back to pending mid-stream would
	// otherwise keep receiving every workspace event until they reconnect.
	streamCtx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	sub, perr := events.SubscribeWire(streamCtx, s.cfg.Bus, req)
	if perr != nil {
		_ = writeJSONLine(ch, protocol.SubscribeResponse{OK: false, Code: perr.Code, Error: perr.Message})
		return
	}
	defer func() { _ = sub.Close() }()
	// ctx is canceled when the session channel closes (see handleSession),
	// when the connection dies, and on server shutdown: that is what ends
	// the subscription. A stdin half-close (EOF below) deliberately does
	// not - per the contract only closing the channel unsubscribes, so
	// piped clients (`echo ... | ssh -s aether-events`) keep streaming.
	stop := context.AfterFunc(streamCtx, func() { _ = sub.Close() })
	defer stop()
	if writeJSONLine(ch, protocol.SubscribeResponse{OK: true}) != nil {
		return
	}
	s.spawn(func() { s.revokeOnPolicyChange(streamCtx, revoke, member, "", true) })

	// Drain (and discard) anything else the client writes; only a real
	// read error - not EOF - tears the subscription down early.
	s.spawn(func() {
		buf := make([]byte, 64)
		for {
			if _, rerr := r.Read(buf); rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					_ = sub.Close()
				}
				return
			}
		}
	})

	// A drop, a write failure, and a closed subscription all end the
	// channel the same way: the client re-subscribes from its cursor.
	_ = events.StreamWire(sub, member, func(ws domain.WorkspaceID) bool { return access.reaches(streamCtx, ws) },
		func(ev protocol.Event) error { return writeJSONLine(ch, ev) })
}

// accessChanged makes every live event stream reload its member's
// workspace access before its next event. Call it after any write that
// changes who reaches a workspace: a grant, a revoke, an owner or a role.
func (s *Server) accessChanged() { s.accessGen.Add(1) }

// streamAccess is one event stream's view of its member's workspace
// access. It reloads when accessChanged was called since the last load,
// and at least every revalidateInterval for writes made outside this
// process, such as aether-server member role. A failed load reaches
// nothing.
type streamAccess struct {
	s      *Server
	member domain.MemberID
	gen    uint64
	at     time.Time
	actor  permissions.Actor
}

func (a *streamAccess) load(ctx context.Context) {
	a.gen, a.at = a.s.accessGen.Load(), time.Now()
	actor, err := resolveActor(ctx, a.s.cfg.Store, a.member)
	if err != nil {
		actor = permissions.Actor{}
	}
	a.actor = actor
}

func (a *streamAccess) reaches(ctx context.Context, ws domain.WorkspaceID) bool {
	if a.s.accessGen.Load() != a.gen || time.Since(a.at) >= a.s.cfg.revalidateInterval {
		a.load(ctx)
	}
	return a.actor.Reaches(ws)
}
