package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/watch"
)

const maxNoticeBodyRunes = 500

var sessionEndWait = 5 * time.Second

// Watches creates the watches the assistant keeps on the runs it launches
// and writes to. Satisfied by *watch.Service.
type Watches interface {
	Follow(ctx context.Context, member domain.MemberID, run domain.RunID) (protocol.WatchResult, error)
}

// UseWatches has every run the assistant launches or writes to watched
// for its member. Call it before the first session starts.
func (s *Service) UseWatches(w Watches) { s.watches = w }

var targetWords = map[string]string{
	protocol.WatchTargetRun: "Run", protocol.WatchTargetMission: "Swarm", protocol.WatchTargetPR: "Pull request",
}

// Transcribe writes a notice of one of member's assistant watches into
// their assistant conversation and announces it. It returns when ctx ends
// even while the member's session is starting or stopping; the write then
// still happens unless ctx ended before it began.
func (s *Service) Transcribe(ctx context.Context, member domain.MemberID, n watch.AssistantNotice) error {
	subject := fmt.Sprintf("%s %q", targetWords[n.TargetKind], n.Title)
	kind, severity, title := events.AssistantNoticeFinished, "info", subject+": "+n.Body
	switch {
	case n.Condition == protocol.WatchNeedsYou && strings.HasPrefix(n.Key, "input:"):
		kind, severity, title = events.AssistantNoticeNeedsInput, "warning", subject+" needs your input"
	case n.Condition == protocol.WatchNeedsYou:
		kind, severity, title = events.AssistantNoticeNeedsAttention, "warning", subject+" needs your attention"
	case n.Condition == protocol.WatchFinished && n.Key == "failed":
		title = subject + " failed"
	case n.Condition == protocol.WatchFinished:
		title = subject + " finished"
	}
	body := n.Body
	if strings.HasPrefix(title, subject+": ") {
		body = ""
	}
	if body != "" {
		body = truncate(body, maxNoticeBodyRunes) + "\n\n"
	}
	body += "[Stop or change this watch](" + strings.TrimPrefix(n.WatchLink, "/") + ")"
	done := make(chan error, 1)
	go func() {
		done <- s.note(ctx, s.member(member), acphost.Notice{Severity: severity, Title: title, Description: body})
	}()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.cfg.Bus.Publish(pubCtx, events.Event{
		WorkspaceID: cmp.Or(n.WorkspaceID, events.ServerScope), RunID: n.RunID, ActorID: member,
		Payload: events.AssistantNoticePayload{MemberID: member, RunID: n.RunID, WatchID: n.WatchID, Kind: kind, Title: title, Body: n.Body},
	}); err != nil {
		return fmt.Errorf("%w: %w", watch.ErrNotAnnounced, err)
	}
	return nil
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}

// record puts a notice in the member's transcript, unless ctx ended while it
// waited for the member's op lock: in the live session, or else in the item
// log, which no session writes while m.op is held.
func (s *Service) record(ctx context.Context, m *member, n acphost.Notice) error {
	m.op.Lock()
	defer m.op.Unlock()
	return s.recordLocked(ctx, m, n)
}

// recordLocked is record for a caller that holds m.op.
func (s *Service) recordLocked(ctx context.Context, m *member, n acphost.Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.removing {
		return ErrRemoving
	}
	if live := m.live(); live != nil {
		err := live.acp.Record(n)
		if err == nil {
			return nil
		}
		if !errors.Is(err, acphost.ErrClosed) {
			return err
		}
		select {
		case <-live.acp.Done():
		case <-time.After(sessionEndWait):
			return fmt.Errorf("the closing session did not end: %w", err)
		}
	}
	log, err := acphost.OpenLog(s.logPath(m.id))
	if err != nil {
		return err
	}
	appendErr := log.Append(&acphost.Item{Kind: acphost.KindNotice, Notice: &n})
	if err = errors.Join(appendErr, log.Close()); err != nil {
		return err
	}
	m.mu.Lock()
	m.signalLocked()
	m.mu.Unlock()
	return nil
}

// note is record without waiting for m.op while a session is live: a stopped
// session refuses the notice, which then goes through record.
func (s *Service) note(ctx context.Context, m *member, n acphost.Notice) error {
	if live := m.live(); live != nil {
		if err := live.acp.Record(n); !errors.Is(err, acphost.ErrClosed) {
			return err
		}
	}
	return s.record(ctx, m, n)
}
