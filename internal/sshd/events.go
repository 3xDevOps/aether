package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// serveEvents bridges a bus subscription onto an aether-events subsystem
// channel: one SubscribeRequest line in, an ack, then Event lines out. On
// a buffer drop the channel is closed so the client re-subscribes from its
// last seen cursor.
func (s *Server) serveEvents(ctx context.Context, member domain.MemberID, ch ssh.Channel) {
	defer func() {
		sendExitStatus(ch, 0)
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
	sub, perr := events.SubscribeWire(ctx, s.cfg.Bus, req)
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
	stop := context.AfterFunc(ctx, func() { _ = sub.Close() })
	defer stop()
	if writeJSONLine(ch, protocol.SubscribeResponse{OK: true}) != nil {
		return
	}

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
	_ = events.StreamWire(sub, func(ev protocol.Event) error { return writeJSONLine(ch, ev) })
}
