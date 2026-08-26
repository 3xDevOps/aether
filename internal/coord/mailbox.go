package coord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Send rate per run: a token bucket that lets a burst of replies through
// and then throttles to one message per refill interval. It is the whole
// defence against a runaway agent loop; nothing is pair-scoped.
const (
	sendBurst  = 5
	sendRefill = 5 * time.Second
)

// Inbox read rate per run: every coord.inbox opens a write transaction on
// the one SQLite file the whole server shares, and the bridge's
// client-side serialization is not a bound a compromised run has to
// respect. The burst covers a bridge draining and acknowledging a full
// inbox; the refill sits far above any real tool-call pace.
const (
	inboxBurst  = 10
	inboxRefill = time.Second
)

// maxPeers is how many distinct runs one run may ever open a conversation
// with.
//
// The overlap edge that authorizes a send is computed from the two runs'
// own diff snapshots, so both sides of it are agent-controlled: a run that
// touches every tracked file has a file set that is a superset of every
// other run's, and the radar then reports it as overlapping with all of
// them. Proving an edge honest is not possible from here, so the spray it
// would enable is bounded directly instead. A real conflict pulls in one
// or two peers and a wide refactor a handful; a run reaching for its ninth
// correspondent is not coordinating.
//
// The count is per process, like the rate-limit bucket: a restart clears
// it, and it bounds one run's reach, not the feature's total traffic.
const maxPeers = 8

// Status answers coord.status for run: who it is, exactly the peers it
// may message right now, and how many messages are waiting for it.
func (s *Service) Status(ctx context.Context, run domain.RunID) (protocol.CoordStatusResult, *protocol.Error) {
	if s.cfg.Disabled {
		return protocol.CoordStatusResult{}, unavailable(protocol.MethodCoordStatus)
	}
	self, rpcErr := s.resolveRun(ctx, protocol.MethodCoordStatus, run)
	if rpcErr != nil {
		return protocol.CoordStatusResult{}, rpcErr
	}
	set, err := s.radar.authorizedSet(ctx, run)
	if err != nil {
		return protocol.CoordStatusResult{}, internalError(protocol.MethodCoordStatus, err)
	}
	peers := make([]protocol.CoordPeer, 0, len(set))
	for _, p := range set {
		peer := protocol.CoordPeer{RunID: string(p.run), Files: p.files, State: p.state}
		if !p.expiry.IsZero() {
			peer.ExpiresAt = p.expiry.UTC().Format(time.RFC3339)
		}
		// A peer the store no longer knows is still an authorized target
		// until its grace expires; it just carries no attribution.
		if r, gerr := s.cfg.Store.GetRun(ctx, p.run); gerr == nil {
			peer.MemberID, peer.Task = string(r.MemberID), r.Task
		}
		peers = append(peers, peer)
	}
	unread, err := s.cfg.Mail.CountUnackedRunMessages(ctx, run)
	if err != nil {
		return protocol.CoordStatusResult{}, internalError(protocol.MethodCoordStatus, err)
	}
	return protocol.CoordStatusResult{
		WireVersion: protocol.CoordWireVersion,
		RunID:       string(self.ID),
		WorkspaceID: string(self.WorkspaceID),
		MemberID:    string(self.MemberID),
		Task:        self.Task,
		Peers:       peers,
		Unread:      unread,
	}, nil
}

// Send answers coord.send from run. Every guard the spec pins is checked
// here, in cost order: the free parameter checks, then the rate limit
// (so a runaway agent is throttled before it reaches the store), then
// identity, authorization, and the inbox depth cap.
func (s *Service) Send(ctx context.Context, from domain.RunID, p protocol.CoordSendParams) (protocol.CoordSendResult, *protocol.Error) {
	const method = protocol.MethodCoordSend
	if s.cfg.Disabled {
		return protocol.CoordSendResult{}, unavailable(method)
	}
	to := domain.RunID(p.ToRunID)
	switch {
	case to == "":
		return protocol.CoordSendResult{}, invalidParams(method, "to_run_id is required")
	case to == from:
		return protocol.CoordSendResult{}, invalidParams(method, "a run cannot message itself")
	case p.Body == "":
		return protocol.CoordSendResult{}, invalidParams(method, "body is required")
	case len(p.Body) > protocol.CoordMaxBodyBytes:
		return protocol.CoordSendResult{}, invalidParams(method,
			fmt.Sprintf("body exceeds %d bytes", protocol.CoordMaxBodyBytes))
	}
	if !s.allow(from) {
		return protocol.CoordSendResult{}, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: fmt.Sprintf("%s: rate limit exceeded (burst %d, 1 message per %ds)",
				method, sendBurst, int(sendRefill.Seconds())),
		}
	}
	sender, rpcErr := s.resolveRun(ctx, method, from)
	if rpcErr != nil {
		return protocol.CoordSendResult{}, rpcErr
	}
	target, rpcErr := s.resolveRun(ctx, method, to)
	if rpcErr != nil {
		return protocol.CoordSendResult{}, rpcErr
	}
	if target.Status.Terminal() {
		return protocol.CoordSendResult{}, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: fmt.Sprintf("%s: run %s has finished", method, to),
		}
	}
	peer, err := s.radar.authorized(ctx, from, to)
	if err != nil {
		return protocol.CoordSendResult{}, internalError(method, err)
	}
	if peer.state == "" {
		// The overlap edge is the whole authorization model: without it a
		// compromised run could spray instructions at every live peer.
		return protocol.CoordSendResult{}, &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: fmt.Sprintf("%s: run %s is not an authorized peer of run %s", method, to, from),
		}
	}
	// The edge is agent-derived, so widening it is within a hostile run's
	// reach; how many peers that buys is not.
	if !s.allowPeer(from, to) {
		return protocol.CoordSendResult{}, &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: fmt.Sprintf("%s: run %s has reached its limit of %d coordination peers", method, from, maxPeers),
		}
	}

	msg := &store.RunMessage{WorkspaceID: sender.WorkspaceID, FromRun: from, ToRun: to, Body: p.Body}
	if err := s.cfg.Mail.AppendRunMessage(ctx, msg, protocol.CoordMaxUnread); err != nil {
		if errors.Is(err, store.ErrInboxFull) {
			return protocol.CoordSendResult{}, &protocol.Error{
				Code: protocol.CodeConflict,
				Message: fmt.Sprintf("%s: run %s inbox is full (%d unacknowledged messages)",
					method, to, protocol.CoordMaxUnread),
			}
		}
		return protocol.CoordSendResult{}, internalError(method, err)
	}
	s.stamp(ctx, sender, target, p.Body)
	return protocol.CoordSendResult{MessageID: msg.ID}, nil
}

// Inbox answers coord.inbox for run: acknowledge the previous batch, then
// return the next one. Delivery is at-least-once, so a response the agent
// never saw costs a duplicate rather than a lost "I'll wait".
func (s *Service) Inbox(ctx context.Context, run domain.RunID, p protocol.CoordInboxParams) (protocol.CoordInboxResult, *protocol.Error) {
	const method = protocol.MethodCoordInbox
	if s.cfg.Disabled {
		return protocol.CoordInboxResult{}, unavailable(method)
	}
	if !s.allowInbox(run) {
		return protocol.CoordInboxResult{}, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: fmt.Sprintf("%s: rate limit exceeded (burst %d, 1 read per %ds)",
				method, inboxBurst, int(inboxRefill.Seconds())),
		}
	}
	msgs, token, err := s.cfg.Mail.DeliverRunMessages(ctx, run, p.AckToken, protocol.CoordMaxUnread)
	if err != nil {
		return protocol.CoordInboxResult{}, internalError(method, err)
	}
	out := make([]protocol.CoordMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, protocol.CoordMessage{
			ID:        m.ID,
			FromRunID: string(m.FromRun),
			Body:      m.Body,
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return protocol.CoordInboxResult{Messages: out, AckToken: token}, nil
}

// resolveRun loads a run, mapping the unknown case to CodeNotFound.
func (s *Service) resolveRun(ctx context.Context, method string, run domain.RunID) (*domain.Run, *protocol.Error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &protocol.Error{
			Code:    protocol.CodeNotFound,
			Message: fmt.Sprintf("%s: unknown run %s", method, run),
		}
	}
	if err != nil {
		return nil, internalError(method, err)
	}
	return r, nil
}

// stamp records the message in the workspace timeline, attributed to the
// sending run's owner. Radar peers are always runs of the same workspace,
// so one note covers both sides of the exchange. Every coordination
// message is auditable; a publish failure never fails the send, which is
// already durable.
func (s *Service) stamp(ctx context.Context, sender, target *domain.Run, body string) {
	s.stampNote(ctx, sender, sender.WorkspaceID, sender.ID,
		fmt.Sprintf("coordination message to run %s: %s", target.ID, body))
}

// stampNote publishes one timeline note for a coordination message,
// attributed to the sending run's owner.
func (s *Service) stampNote(ctx context.Context, sender *domain.Run, workspace domain.WorkspaceID, run domain.RunID, message string) {
	_, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     sender.MemberID,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: message,
		},
	})
	if err != nil {
		slog.Warn("coord: timeline stamp failed", "run", sender.ID, "workspace", workspace, "error", err)
	}
}

// bucket is one run's allowance for a rate-limited call.
type bucket struct {
	tokens float64
	last   time.Time
}

// allow spends one send token for run, refilling first.
func (s *Service) allow(run domain.RunID) bool {
	return s.spend(s.buckets, run, sendBurst, sendRefill)
}

// allowInbox spends one inbox-read token for run, refilling first.
func (s *Service) allowInbox(run domain.RunID) bool {
	return s.spend(s.inboxBuckets, run, inboxBurst, inboxRefill)
}

// spend takes one token from run's bucket in m, refilling first.
func (s *Service) spend(m map[domain.RunID]*bucket, run domain.RunID, burst float64, refill time.Duration) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	b := m[run]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		m[run] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()/refill.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// allowPeer reserves a conversation slot for from -> to, reporting whether
// the run may still open one. A peer it has already messaged always may.
func (s *Service) allowPeer(from, to domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	opened := s.peers[from]
	if opened == nil {
		opened = make(map[domain.RunID]bool, 1)
		s.peers[from] = opened
	}
	if opened[to] {
		return true
	}
	if len(opened) >= maxPeers {
		return false
	}
	opened[to] = true
	return true
}

func invalidParams(method, msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: method + ": " + msg}
}

func internalError(method string, err error) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: method + ": " + err.Error()}
}
