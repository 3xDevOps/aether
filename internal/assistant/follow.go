package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

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
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	ListApprovals(ctx context.Context, workspace domain.WorkspaceID, decision string) ([]*store.Approval, error)
}

// RunState is what the scheduler knows about a live run that its row does not.
type RunState interface {
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	ACPPending(run domain.RunID) []acphost.Request
}

// Notifier sends a push notification about run to member's devices. A
// non-nil current is asked again just before sending, and a false answer
// drops the notification.
type Notifier func(ctx context.Context, member domain.MemberID, title, body string, run domain.RunID, current func(context.Context) bool) error

// closeDrain bounds how long Close waits for queued notices to be delivered.
var closeDrain = 5 * time.Second

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
	deliver func(n notice)

	// mu guards queued and serializes the reads that decide what to announce.
	mu sync.Mutex
	// queued holds the key of each notice handed to delivery and not yet
	// finished, by member and run, so a state is queued once.
	queued map[[2]string]string
}

// read is what run id needs of its followers now, or nil.
func (f *follower) read(ctx context.Context, id domain.RunID) (*notice, error) {
	run, err := f.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read followed run: %w", err)
	}
	approvals, err := f.store.ListApprovals(ctx, run.WorkspaceID, store.ApprovalRequested)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	var approval *store.Approval
	for _, a := range approvals {
		if a.RunID == id {
			approval = a
			break
		}
	}
	return classify(run, f.runs.PendingInputs(id), f.runs.ACPPending(id), approval), nil
}

// check hands the state of run to delivery for each member who follows it
// and has not been told. With finishedOnly, other states are left for the
// events that report them.
func (f *follower) check(ctx context.Context, id domain.RunID, finishedOnly bool) {
	for _, n := range f.due(ctx, id, finishedOnly) {
		f.deliver(n)
	}
}

func (f *follower) due(ctx context.Context, id domain.RunID, finishedOnly bool) []notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	follows, err := f.store.ListAssistantFollows(ctx, id)
	if err != nil {
		slog.Warn("assistant: list follows", "run", id, "error", err)
		return nil
	}
	if len(follows) == 0 {
		return nil
	}
	n, err := f.read(ctx, id)
	if err != nil {
		slog.Warn("assistant: read the followed run", "run", id, "error", err)
		return nil
	}
	if n == nil || finishedOnly && n.kind != events.AssistantNoticeFinished {
		return nil
	}
	var due []notice
	for _, follow := range follows {
		slot := [2]string{string(follow.MemberID), string(id)}
		switch {
		case f.queued[slot] == n.key:
		case follow.NotifiedKey == n.key:
			f.end(ctx, n, follow.MemberID)
		default:
			f.queued[slot] = n.key
			mine := *n
			mine.member = follow.MemberID
			due = append(due, mine)
		}
	}
	return due
}

// end drops the follow of a run that finished and was announced.
func (f *follower) end(ctx context.Context, n *notice, member domain.MemberID) {
	if n.kind != events.AssistantNoticeFinished {
		return
	}
	if err := f.store.DeleteAssistantFollow(ctx, member, n.run.ID); err != nil {
		slog.Warn("assistant: end the follow", "run", n.run.ID, "member", member, "error", err)
	}
}

// finish records that n was announced, so it is not announced again, and
// lets a failed one be queued again.
func (f *follower) finish(n notice, announced bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if announced {
		if err := f.store.SetAssistantFollowNotified(ctx, n.member, n.run.ID, n.key); err != nil {
			slog.Warn("assistant: record the announced state", "run", n.run.ID, "member", n.member, "error", err)
		} else {
			f.end(ctx, &n, n.member)
		}
	}
	f.mu.Lock()
	delete(f.queued, [2]string{string(n.member), string(n.run.ID)})
	f.mu.Unlock()
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

// StartFollowing has the service follow the runs its assistants launch or
// message: it announces each when it finishes or needs its member. ctx bounds
// only the setup; the watcher runs until Close. It does nothing without a bus.
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
	followed, err := s.cfg.Follows.ListFollowedRuns(ctx)
	if err != nil {
		_ = sub.Close()
		return fmt.Errorf("assistant: list the followed runs: %w", err)
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
		for _, id := range followed {
			s.follower.check(s.ctx, id, false)
		}
	})
	return nil
}

// UseNotifier has the service also push notices to its member's devices.
func (s *Service) UseNotifier(notify Notifier) { s.notify = notify }

// follow records that member's assistant launched or messaged run. A run
// that finished in the meantime is announced at once.
func (s *Service) follow(ctx context.Context, member domain.MemberID, run domain.RunID) {
	if err := s.cfg.Follows.PutAssistantFollow(ctx, member, run); err != nil {
		slog.Warn("assistant: follow the run", "run", run, "member", member, "error", err)
		return
	}
	if s.ctx.Err() == nil {
		s.wg.Go(func() { s.follower.check(s.ctx, run, true) })
	}
}

// enqueue queues n behind the member's earlier notices. Each member has a
// delivery goroutine of their own while they have notices, so one whose
// session is slow to start holds back no one else's.
func (s *Service) enqueue(n notice) {
	s.dmu.Lock()
	s.queues[n.member] = append(s.queues[n.member], n)
	start := !s.delivering[n.member]
	s.delivering[n.member] = true
	s.dmu.Unlock()
	if start {
		s.wg.Go(func() { s.drain(n.member) })
	}
}

func (s *Service) drain(id domain.MemberID) {
	for {
		s.dmu.Lock()
		queue := s.queues[id]
		if len(queue) == 0 {
			delete(s.queues, id)
			delete(s.delivering, id)
			s.dmu.Unlock()
			return
		}
		n := queue[0]
		s.queues[id] = queue[1:]
		s.dmu.Unlock()
		s.follower.finish(n, s.deliver(n))
	}
}

// drainNotices waits, for a bounded time, until every queued notice was
// delivered.
func (s *Service) drainNotices() {
	deadline := time.Now().Add(closeDrain)
	for time.Now().Before(deadline) {
		s.dmu.Lock()
		idle := len(s.delivering) == 0
		s.dmu.Unlock()
		if idle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// deliver announces n and reports whether it reached the member's
// transcript, which is what makes it announced.
func (s *Service) deliver(n notice) bool {
	if _, err := s.cfg.Follows.GetMember(s.ctx, n.member); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("assistant: read the member of a notice", "member", n.member, "error", err)
		}
		return false
	}
	if err := s.transcribe(s.member(n.member), n); err != nil {
		if !errors.Is(err, ErrRemoving) {
			slog.Warn("assistant: write the notice to the transcript", "member", n.member, "run", n.run.ID, "error", err)
		}
		return false
	}
	if _, err := s.cfg.Bus.Publish(s.ctx, events.Event{
		WorkspaceID: n.run.WorkspaceID, RunID: n.run.ID, ActorID: n.member,
		Payload: events.AssistantNoticePayload{MemberID: n.member, RunID: n.run.ID, Kind: n.kind, Title: n.title, Body: n.body},
	}); err != nil {
		slog.Warn("assistant: publish the notice", "member", n.member, "run", n.run.ID, "error", err)
	}
	// The owner of a run is already pushed what it needs; only its end is new.
	if s.notify != nil && (n.kind == events.AssistantNoticeFinished || n.run.MemberID != n.member) {
		s.wg.Go(func() {
			var current func(context.Context) bool
			if n.kind != events.AssistantNoticeFinished {
				current = func(ctx context.Context) bool {
					now, err := s.follower.read(ctx, n.run.ID)
					return err == nil && now != nil && now.key == n.key
				}
			}
			if err := s.notify(s.ctx, n.member, n.title, n.body, n.run.ID, current); err != nil {
				slog.Warn("assistant: push the notice", "member", n.member, "run", n.run.ID, "error", err)
			}
		})
	}
	return true
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
