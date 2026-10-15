package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// maxNoticeBodyRunes bounds what an agent's question, reason or action adds
// to a notice.
const maxNoticeBodyRunes = 500

// FollowStore is what the follow watcher reads and writes.
type FollowStore interface {
	store.AssistantFollowStore
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	ListApprovals(ctx context.Context, workspace domain.WorkspaceID, decision string) ([]*store.Approval, error)
}

// RunState is what the scheduler knows about a live run that its row does not.
type RunState interface {
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	ACPPending(run domain.RunID) []acphost.Request
}

// Notifier sends a push notification about run to member's devices.
type Notifier func(ctx context.Context, member domain.MemberID, title, body string, run domain.RunID) error

// notice is one announcement to a member about a followed run.
type notice struct {
	member   domain.MemberID
	run      *domain.Run
	kind     string
	severity string
	title    string
	body     string
	// key identifies the state announced, so it is announced once.
	key string
}

type follower struct {
	store   FollowStore
	runs    RunState
	deliver func(ctx context.Context, n notice)

	// mu serializes checks, so one state is announced once however many
	// events and records ask for it.
	mu sync.Mutex
}

// check announces the state of run to the members who follow it, unless it
// was announced already. A run that finished is no longer followed. With
// finishedOnly, other states are left for the events that report them.
func (f *follower) check(ctx context.Context, id domain.RunID, finishedOnly bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	follows, err := f.store.ListAssistantFollows(ctx, id)
	if err != nil {
		slog.Warn("assistant: list follows", "run", id, "error", err)
		return
	}
	if len(follows) == 0 {
		return
	}
	run, err := f.store.GetRun(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("assistant: read followed run", "run", id, "error", err)
		}
		return
	}
	approvals, err := f.store.ListApprovals(ctx, run.WorkspaceID, store.ApprovalRequested)
	if err != nil {
		slog.Warn("assistant: list approvals", "run", id, "error", err)
		return
	}
	var approval *store.Approval
	for _, a := range approvals {
		if a.RunID == id {
			approval = a
			break
		}
	}
	n := classify(run, f.runs.PendingInputs(id), f.runs.ACPPending(id), approval)
	if n == nil || finishedOnly && n.kind != events.AssistantNoticeFinished {
		return
	}
	for _, follow := range follows {
		if follow.NotifiedKey != n.key {
			if err := f.store.SetAssistantFollowNotified(ctx, follow.MemberID, id, n.key); err != nil {
				slog.Warn("assistant: record the announced state", "run", id, "member", follow.MemberID, "error", err)
				continue
			}
			mine := *n
			mine.member = follow.MemberID
			f.deliver(ctx, mine)
		}
		if n.kind == events.AssistantNoticeFinished {
			if err := f.store.DeleteAssistantFollow(ctx, follow.MemberID, id); err != nil {
				slog.Warn("assistant: end the follow", "run", id, "member", follow.MemberID, "error", err)
			}
		}
	}
}

// classify is what run now needs of its follower, or nil while it is queued
// or working.
func classify(run *domain.Run, inputs []domain.RunInputRequest, pending []acphost.Request, approval *store.Approval) *notice {
	n := &notice{run: run, severity: "warning"}
	label := fmt.Sprintf("Run %q", push.RunTitle(run))
	switch {
	case run.Status.Terminal():
		n.kind, n.severity, n.key = events.AssistantNoticeFinished, "info", "finished:"+string(run.Status)
		n.title = label + map[domain.RunStatus]string{
			domain.RunCompleted: " finished", domain.RunMerged: " was merged", domain.RunFailed: " failed",
			domain.RunAbandoned: " was stopped", domain.RunInterrupted: " was interrupted",
		}[run.Status]
		n.body = run.Reason
	case len(inputs) > 0:
		n.kind, n.title = events.AssistantNoticeNeedsInput, label+" needs your input"
		n.key = "input:" + inputs[0].SessionID + "|" + inputs[0].Kind + "|" + inputs[0].ID
		n.body = inputBody(inputs[0], pending)
	case approval != nil:
		n.kind, n.title = events.AssistantNoticeNeedsApproval, label+" needs your approval"
		n.key, n.body = "approval:"+approval.ID, approval.Action
	case run.Status == domain.RunNeedsAttention:
		n.kind, n.title = events.AssistantNoticeNeedsAttention, label+" needs your attention"
		changed := ""
		if run.StatusChangedAt != nil {
			changed = strconv.FormatInt(run.StatusChangedAt.UnixNano(), 10)
		}
		n.key = "attention:" + run.Reason + "|" + changed
		if n.body = push.AttentionBody(run); n.body == "" {
			n.body = run.Reason
		}
	default:
		return nil
	}
	n.body = truncate(n.body, maxNoticeBodyRunes)
	return n
}

func inputBody(input domain.RunInputRequest, pending []acphost.Request) string {
	for _, r := range pending {
		if r.ID != input.ID {
			continue
		}
		switch {
		case r.Kind == acphost.RequestPermission:
			return "Permission: " + r.Title
		case len(r.Schema) > 0:
			return "Form: " + r.Title
		}
		return "Question: " + r.Title
	}
	if input.Kind == "permission" {
		return "Permission: answer in the run"
	}
	return "Question: answer in the run"
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}

// StartFollowing has the service follow the runs its assistants launch or message:
// it announces each when it finishes or needs its member. ctx bounds only the
// setup; the watcher runs until Close. It does nothing without a bus.
func (s *Service) StartFollowing(ctx context.Context) error {
	if s.follower == nil {
		return nil
	}
	sub, err := s.cfg.Bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{
		events.TypeRunStatus, events.TypeRunInput, events.TypeApproval,
	}}})
	if err != nil {
		return fmt.Errorf("assistant: subscribe to run events: %w", err)
	}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()
	s.wg.Go(func() {
		for e := range sub.Events() {
			if e.RunID != "" {
				s.follower.check(s.ctx, e.RunID, false)
			}
		}
	})
	s.wg.Go(func() {
		for {
			select {
			case <-s.ctx.Done():
				return
			case n := <-s.notices:
				s.deliver(n)
			}
		}
	})
	return nil
}

// UseNotifier has the service also push each notice to its member's devices.
func (s *Service) UseNotifier(notify Notifier) { s.notify = notify }

// follow records that member's assistant launched or messaged run. A run
// that already finished is announced at once.
func (s *Service) follow(ctx context.Context, member domain.MemberID, run domain.RunID) {
	if err := s.cfg.Follows.PutAssistantFollow(ctx, member, run); err != nil {
		slog.Warn("assistant: follow the run", "run", run, "member", member, "error", err)
		return
	}
	if s.ctx.Err() == nil {
		s.wg.Go(func() { s.follower.check(s.ctx, run, true) })
	}
}

// enqueue hands n to the delivery goroutine, which runs in order so notices
// reach the transcript as they arose, whatever the member's session is doing.
func (s *Service) enqueue(ctx context.Context, n notice) {
	select {
	case s.notices <- n:
	case <-ctx.Done():
	}
}

func (s *Service) deliver(n notice) {
	m := s.member(n.member)
	if err := s.transcribe(m, n); err != nil {
		slog.Warn("assistant: write the notice to the transcript", "member", n.member, "run", n.run.ID, "error", err)
	}
	if _, err := s.cfg.Bus.Publish(s.ctx, events.Event{
		WorkspaceID: n.run.WorkspaceID, RunID: n.run.ID, ActorID: n.member,
		Payload: events.AssistantNoticePayload{MemberID: n.member, RunID: n.run.ID, Kind: n.kind, Title: n.title, Body: n.body},
	}); err != nil {
		slog.Warn("assistant: publish the notice", "member", n.member, "run", n.run.ID, "error", err)
	}
	if s.notify != nil {
		s.wg.Go(func() {
			if err := s.notify(s.ctx, n.member, n.title, n.body, n.run.ID); err != nil {
				slog.Warn("assistant: push the notice", "member", n.member, "run", n.run.ID, "error", err)
			}
		})
	}
}

// transcribe puts n in the member's transcript: in the live session, or else
// in the item log, which no session writes while the member's op lock is held.
func (s *Service) transcribe(m *member, n notice) error {
	m.op.Lock()
	defer m.op.Unlock()
	if m.removing {
		return ErrRemoving
	}
	if live := m.live(); live != nil {
		live.acp.Notice(n.severity, n.title, n.body)
		return nil
	}
	log, err := acphost.OpenLog(s.logPath(m.id))
	if err != nil {
		return err
	}
	appendErr := log.Append(&acphost.Item{Kind: acphost.KindNotice, Notice: &acphost.Notice{Severity: n.severity, Title: n.title, Description: n.body}})
	if err = errors.Join(appendErr, log.Close()); err != nil {
		return err
	}
	m.mu.Lock()
	m.signalLocked()
	m.mu.Unlock()
	return nil
}
