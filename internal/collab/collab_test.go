package collab

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

type collabClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *collabClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *collabClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type collabStore struct {
	mu         sync.Mutex
	workspaces map[domain.WorkspaceID]*domain.Workspace
	members    map[domain.MemberID]*domain.Member
	runs       map[domain.RunID]*domain.Run
	messages   map[string]*store.RoomMessage
	next       int
}

func newCollabStore() *collabStore {
	return &collabStore{workspaces: map[domain.WorkspaceID]*domain.Workspace{}, members: map[domain.MemberID]*domain.Member{}, runs: map[domain.RunID]*domain.Run{}, messages: map[string]*store.RoomMessage{}}
}

func cloneRoomMessage(m *store.RoomMessage) *store.RoomMessage {
	if m == nil {
		return nil
	}
	copy := *m
	copy.Attachments = append([]string(nil), m.Attachments...)
	if m.Anchor != nil {
		a := *m.Anchor
		copy.Anchor = &a
	}
	if m.DeliverAfter != nil {
		v := *m.DeliverAfter
		copy.DeliverAfter = &v
	}
	if m.DecidedAt != nil {
		v := *m.DecidedAt
		copy.DecidedAt = &v
	}
	if m.DeliveredAt != nil {
		v := *m.DeliveredAt
		copy.DeliveredAt = &v
	}
	if m.Failure != nil {
		f := *m.Failure
		copy.Failure = &f
	}
	return &copy
}

func (s *collabStore) CreateRoomMessage(_ context.Context, m *store.RoomMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, prior := range s.messages {
		if prior.ActorID == m.ActorID && prior.RunID == m.RunID && prior.IdempotencyKey == m.IdempotencyKey {
			*m = *cloneRoomMessage(prior)
			return nil
		}
	}
	s.next++
	m.ID = "msg-" + string(rune('0'+s.next))
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Unix(int64(s.next), 0).UTC()
	}
	m.UpdatedAt = m.CreatedAt
	m.State = store.RoomMessageQueued
	if m.Kind != store.RoomMessageSteerRequest {
		m.State = store.RoomMessageSent
		if m.DeliveredAt == nil {
			delivered := m.CreatedAt
			m.DeliveredAt = &delivered
		}
	}
	s.messages[m.ID] = cloneRoomMessage(m)
	return nil
}
func (s *collabStore) GetRoomMessage(_ context.Context, id string) (*store.RoomMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.messages[id]
	if m == nil {
		return nil, store.ErrNotFound
	}
	return cloneRoomMessage(m), nil
}
func (s *collabStore) GetRoomMessageByIdempotency(_ context.Context, actor domain.MemberID, run domain.RunID, key string) (*store.RoomMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m.ActorID == actor && m.RunID == run && m.IdempotencyKey == key {
			return cloneRoomMessage(m), nil
		}
	}
	return nil, store.ErrNotFound
}
func (s *collabStore) ListRoomMessages(_ context.Context, workspace domain.WorkspaceID, run domain.RunID, before string, limit int) (*store.RoomMessagePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []*store.RoomMessage
	for _, m := range s.messages {
		if m.WorkspaceID == workspace && (run == "" || m.RunID == run) {
			items = append(items, cloneRoomMessage(m))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if before != "" {
		for i, m := range items {
			if m.ID == before {
				items = items[i+1:]
				break
			}
		}
	}
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	page := &store.RoomMessagePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextBefore = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}
func (s *collabStore) ClaimRoomMessage(_ context.Context, id string, now time.Time, force bool, by string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.messages[id]
	if m == nil {
		return false, store.ErrNotFound
	}
	if m.State != store.RoomMessageQueued {
		return false, nil
	}
	if !force && m.DeliverAfter != nil && m.DeliverAfter.After(now) {
		return false, nil
	}
	m.State = store.RoomMessageUncertain
	if force {
		m.DecidedBy = domain.MemberID(by)
		v := now
		m.DecidedAt = &v
	}
	return true, nil
}
func (s *collabStore) CancelQueuedSteerRequests(_ context.Context, run domain.RunID, by domain.MemberID, at time.Time) ([]*store.RoomMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cancelled []*store.RoomMessage
	for _, m := range s.messages {
		if m.RunID != run || m.Kind != store.RoomMessageSteerRequest || m.State != store.RoomMessageQueued {
			continue
		}
		m.State = store.RoomMessageCancelled
		m.DecidedBy = by
		v := at
		m.DecidedAt = &v
		cancelled = append(cancelled, cloneRoomMessage(m))
	}
	return cancelled, nil
}
func (s *collabStore) TransitionRoomMessage(_ context.Context, id string, state store.RoomMessageState, deliveredAt *time.Time, failure *store.RoomMessageFailure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.messages[id]
	if m == nil {
		return store.ErrNotFound
	}
	if m.State != store.RoomMessageQueued && m.State != store.RoomMessageUncertain {
		return store.ErrConflict
	}
	m.State = state
	m.DeliveredAt = deliveredAt
	m.Failure = failure
	return nil
}
func (s *collabStore) DecideRoomMessage(_ context.Context, id string, from, to store.RoomMessageState, by string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.messages[id]
	if m == nil {
		return false, store.ErrNotFound
	}
	if m.State != from {
		return false, nil
	}
	m.State = to
	m.DecidedBy = domain.MemberID(by)
	m.DecidedAt = &at
	return true, nil
}
func (s *collabStore) CreateEvidencePacket(context.Context, *store.EvidencePacket) error { return nil }
func (s *collabStore) GetEvidencePacket(context.Context, string) (*store.EvidencePacket, error) {
	return nil, store.ErrNotFound
}
func (s *collabStore) ListEvidencePackets(context.Context, domain.WorkspaceID, domain.RunID, string, int) (*store.EvidencePacketPage, error) {
	return &store.EvidencePacketPage{}, nil
}
func (s *collabStore) ListExpiredEvidencePackets(context.Context, time.Time, int) ([]*store.EvidencePacket, error) {
	return nil, nil
}
func (s *collabStore) DeleteEvidencePacket(context.Context, string) error { return nil }
func (s *collabStore) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return nil, store.ErrNotFound
	}
	copy := *r
	return &copy, nil
}
func (s *collabStore) GetMember(_ context.Context, id domain.MemberID) (*domain.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[id]
	if m == nil {
		return nil, store.ErrNotFound
	}
	copy := *m
	return &copy, nil
}
func (s *collabStore) GetWorkspace(_ context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.workspaces[id]
	if w == nil {
		return nil, store.ErrNotFound
	}
	copy := *w
	return &copy, nil
}
func (s *collabStore) ListWorkspaces(context.Context) ([]*domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*domain.Workspace, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		copy := *w
		out = append(out, &copy)
	}
	return out, nil
}
func (s *collabStore) SetRunProtected(_ context.Context, id domain.RunID, protected bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return store.ErrNotFound
	}
	r.Protected = protected
	return nil
}

var _ store.CollaborationStore = (*collabStore)(nil)
var _ Runs = (*collabStore)(nil)
var _ Workspaces = (*collabStore)(nil)
var _ ProtectedStore = (*collabStore)(nil)

type claimBarrierStore struct {
	*collabStore
	entered chan struct{}
	release chan struct{}
}

func (s *claimBarrierStore) ClaimRoomMessage(ctx context.Context, id string, now time.Time, force bool, by string) (bool, error) {
	close(s.entered)
	<-s.release
	return s.collabStore.ClaimRoomMessage(ctx, id, now, force, by)
}

type collabPTY struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *collabPTY) Inject(_ context.Context, _ domain.RunID, _ domain.MemberID, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.err
}
func (p *collabPTY) Calls() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

func setupCollab(t *testing.T) (*collabStore, *collabClock, *domain.Workspace, *domain.Member, *domain.Member, *domain.Run) {
	t.Helper()
	s := newCollabStore()
	clock := &collabClock{now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	ws := &domain.Workspace{ID: "ws", Name: "workspace"}
	owner := &domain.Member{ID: "owner", DisplayName: "Owner", Role: domain.RoleCollaborator}
	other := &domain.Member{ID: "other", DisplayName: "Other", Role: domain.RoleCollaborator}
	run := &domain.Run{ID: "run", WorkspaceID: ws.ID, MemberID: owner.ID, Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	s.workspaces[ws.ID], s.members[owner.ID], s.members[other.ID], s.runs[run.ID] = ws, owner, other, run
	return s, clock, ws, owner, other, run
}

func newCollabService(t *testing.T, st *collabStore, clock *collabClock, pty *collabPTY, ctl interface {
	Validate(string, string, uint64) error
}) *Service {
	t.Helper()
	_ = ctl
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type failingWorkspaces struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (w *failingWorkspaces) ListWorkspaces(context.Context) ([]*domain.Workspace, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	return nil, w.err
}

func (w *failingWorkspaces) Calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

func TestOverdueWorkerExposesSweepFailures(t *testing.T) {
	st, clock, _, _, _, _ := setupCollab(t)
	workspaceErr := errors.New("workspace list failed")
	workspaces := &failingWorkspaces{err: workspaceErr}
	service, err := New(Config{
		Store: st, Runs: st, Workspaces: workspaces, Now: clock.Now,
		WorkerInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		if closeErr := service.Close(); closeErr != nil {
			t.Errorf("close service: %v", closeErr)
		}
	})
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for service.OverdueDeliveryError() == nil {
		select {
		case <-deadline:
			t.Fatal("overdue worker did not expose initial failure")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	for workspaces.Calls() < 2 {
		select {
		case <-deadline:
			t.Fatal("overdue worker did not retry periodic sweep")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got := service.OverdueDeliveryError(); !errors.Is(got, workspaceErr) {
		t.Fatalf("overdue worker error = %v, want wrapped %v", got, workspaceErr)
	}
}

type failingClaimStore struct {
	*collabStore
	err    error
	failID string
}

func (s *failingClaimStore) ClaimRoomMessage(ctx context.Context, id string, now time.Time, force bool, by string) (bool, error) {
	if s.failID == "" || id == s.failID {
		return false, s.err
	}
	return s.collabStore.ClaimRoomMessage(ctx, id, now, force, by)
}

func TestDeliverDueReturnsDeliveryFailure(t *testing.T) {
	st, clock, ws, _, other, run := setupCollab(t)
	source := newCollabService(t, st, clock, &collabPTY{}, nil)
	queued, err := source.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "delivery failure",
		IdempotencyKey: "delivery-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(SteeringDelay)
	deliveryErr := errors.New("claim failed")
	service, err := New(Config{
		Store: &failingClaimStore{collabStore: st, err: deliveryErr},
		Runs:  st, Workspaces: st, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeliverDue(context.Background(), 10); err == nil ||
		!strings.Contains(err.Error(), "deliver overdue message "+fmt.Sprintf("%q", queued.Message.ID)) ||
		!errors.Is(err, deliveryErr) {
		t.Fatalf("DeliverDue error = %v, want contextual delivery failure", err)
	}
}

func TestDeliverDueContinuesAfterOneMessageFailure(t *testing.T) {
	st, clock, ws, _, other, run := setupCollab(t)
	source := newCollabService(t, st, clock, &collabPTY{}, nil)
	earlier, err := source.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "deliver later",
		IdempotencyKey: "deliver-later",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Millisecond)
	later, err := source.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "fail first",
		IdempotencyKey: "fail-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(SteeringDelay)

	deliveryErr := errors.New("first claim failed")
	pty := &collabPTY{}
	service, err := New(Config{
		Store: &failingClaimStore{collabStore: st, err: deliveryErr, failID: later.Message.ID},
		Runs:  st, Workspaces: st, Inject: pty.Inject, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	count, sweepErr := service.DeliverDue(context.Background(), 10)
	if count != 1 || sweepErr == nil ||
		!strings.Contains(sweepErr.Error(), "deliver overdue message "+fmt.Sprintf("%q", later.Message.ID)) ||
		!errors.Is(sweepErr, deliveryErr) {
		t.Fatalf("DeliverDue = (%d, %v), want one success and contextual failure", count, sweepErr)
	}
	failed, err := st.GetRoomMessage(context.Background(), later.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != store.RoomMessageQueued {
		t.Fatalf("failed message state = %q, want queued", failed.State)
	}
	delivered, err := st.GetRoomMessage(context.Background(), earlier.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.State != store.RoomMessageSent {
		t.Fatalf("later message state = %q, want sent", delivered.State)
	}
	if pty.Calls() != 1 {
		t.Fatalf("successful later delivery calls = %d, want 1", pty.Calls())
	}
}

type failingCollabBus struct {
	events.Bus
	err       error
	successes int
}

func (b *failingCollabBus) Publish(ctx context.Context, e events.Event) (events.Event, error) {
	if b.err != nil {
		return events.Event{}, b.err
	}
	b.successes++
	return b.Bus.Publish(ctx, e)
}

func TestDeniedRetryRepublishesAfterPublicationFailure(t *testing.T) {
	st, clock, ws, owner, other, run := setupCollab(t)
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := bus.Close(); closeErr != nil {
			t.Errorf("close bus: %v", closeErr)
		}
	})
	flakyBus := &failingCollabBus{Bus: bus}
	controlService := control.New(control.Config{Now: clock.Now})
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "moderator", false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		Store: st, Runs: st, Workspaces: st, Bus: flakyBus, Control: controlService, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "deny me",
		IdempotencyKey: "deny-republish",
	})
	if err != nil {
		t.Fatal(err)
	}

	publicationErr := errors.New("denial event unavailable")
	flakyBus.err = publicationErr
	_, denyErr := service.Deny(context.Background(), result.Message.ID, owner.ID, lease.SessionID, lease.Generation)
	if denyErr == nil || !errors.Is(denyErr, publicationErr) {
		t.Fatalf("initial denial error = %v, want publication failure", denyErr)
	}
	denied, err := st.GetRoomMessage(context.Background(), result.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if denied.State != store.RoomMessageDenied {
		t.Fatalf("denied state = %q, want denied", denied.State)
	}

	flakyBus.err = nil
	retry, err := service.Deny(context.Background(), result.Message.ID, owner.ID, lease.SessionID, lease.Generation)
	if err != nil {
		t.Fatalf("denial retry: %v", err)
	}
	if retry.State != store.RoomMessageDenied {
		t.Fatalf("denial retry state = %q, want denied", retry.State)
	}
	if got := flakyBus.Successes(); got != 2 {
		t.Fatalf("successful room publications = %d, want initial post and denial retry", got)
	}
}

func (b *failingCollabBus) Successes() int { return b.successes }

func TestCollaborationPublicationFailuresSurface(t *testing.T) {
	t.Run("room message", func(t *testing.T) {
		st, clock, ws, _, other, run := setupCollab(t)
		bus, err := events.NewInProc(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if closeErr := bus.Close(); closeErr != nil {
				t.Errorf("close bus: %v", closeErr)
			}
		})
		publicationErr := errors.New("room event unavailable")
		service, err := New(Config{
			Store: st, Runs: st, Workspaces: st,
			Bus: &failingCollabBus{Bus: bus, err: publicationErr}, Now: clock.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, postErr := service.Post(context.Background(), MessageInput{
			WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
			Kind: store.RoomMessageComment, Body: "persisted before publish",
			IdempotencyKey: "publication-failure",
		}); postErr == nil || !errors.Is(postErr, publicationErr) ||
			!strings.Contains(postErr.Error(), "publish room message") {
			t.Fatalf("Post publication error = %v, want contextual failure", postErr)
		}
		msg, err := st.GetRoomMessageByIdempotency(context.Background(), other.ID, run.ID, "publication-failure")
		if err != nil {
			t.Fatal(err)
		}
		if msg.State != store.RoomMessageSent {
			t.Fatalf("persisted message state = %q, want sent", msg.State)
		}
	})

	t.Run("protection", func(t *testing.T) {
		st, clock, _, owner, _, run := setupCollab(t)
		bus, err := events.NewInProc(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if closeErr := bus.Close(); closeErr != nil {
				t.Errorf("close bus: %v", closeErr)
			}
		})
		publicationErr := errors.New("protection event unavailable")
		service, err := New(Config{
			Store: st, Runs: st, Workspaces: st,
			Bus: &failingCollabBus{Bus: bus, err: publicationErr}, Now: clock.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Protect(context.Background(), run.ID, owner.ID); err == nil ||
			!errors.Is(err, publicationErr) ||
			!strings.Contains(err.Error(), "publish protection event") {
			t.Fatalf("Protect publication error = %v, want contextual failure", err)
		}
		if !run.Protected {
			t.Fatal("protection mutation was lost when publication failed")
		}
	})
}
func TestSteeringDelayAndControllerImmediateSend(t *testing.T) {

	st, clock, ws, owner, other, run := setupCollab(t)
	pty := &collabPTY{}
	controlService := control.New(control.Config{Now: clock.Now})
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Control: controlService, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID, Kind: store.RoomMessageSteerRequest, Body: "later", IdempotencyKey: "delay"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.DeliverAfter == nil || !result.Message.DeliverAfter.Equal(clock.Now().Add(SteeringDelay)) {
		t.Fatalf("deliver_after=%v, want +45s", result.Message.DeliverAfter)
	}
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "session", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := service.Post(context.Background(), MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID, Kind: store.RoomMessageSteerRequest, Body: "borrowed", IdempotencyKey: "borrowed", ControllerSessionID: lease.SessionID, ControllerGeneration: lease.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if bob.Message.DeliverAfter == nil || !bob.Message.DeliverAfter.Equal(clock.Now().Add(SteeringDelay)) {
		t.Fatalf("bob deliver_after=%v, want +45s", bob.Message.DeliverAfter)
	}
	if pty.Calls() != 0 {
		t.Fatalf("bob inject calls=%d, want 0", pty.Calls())
	}
	alice, err := service.Post(context.Background(), MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: owner.ID, Kind: store.RoomMessageSteerRequest, Body: "now", IdempotencyKey: "now", ControllerSessionID: lease.SessionID, ControllerGeneration: lease.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if alice.Message.DeliverAfter != nil {
		t.Fatalf("alice deliver_after=%v, want immediate", alice.Message.DeliverAfter)
	}
	if pty.Calls() != 1 {
		t.Fatalf("inject calls=%d, want 1", pty.Calls())
	}
}

func TestRestartOverdueDeliveryAndApproveDenyWinner(t *testing.T) {
	st, clock, ws, owner, other, run := setupCollab(t)
	pty := &collabPTY{}
	controlService := control.New(control.Config{Now: clock.Now})
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "controller", false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Control: controlService, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID, Kind: store.RoomMessageSteerRequest, Body: "queued", IdempotencyKey: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(SteeringDelay)
	service, err = New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Control: controlService, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if n, deliveryErr := service.DeliverDue(context.Background(), 10); deliveryErr != nil || n != 1 {
		t.Fatalf("overdue delivery=(%d,%v), want (1,nil)", n, deliveryErr)
	}
	result, err = service.Post(context.Background(), MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID, Kind: store.RoomMessageSteerRequest, Body: "approve", IdempotencyKey: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveNow(context.Background(), result.Message.ID, owner.ID, lease.SessionID, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deny(context.Background(), result.Message.ID, owner.ID, lease.SessionID, lease.Generation); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRoomMessage(context.Background(), result.Message.ID)
	if got.State != store.RoomMessageSent && got.State != store.RoomMessageNotSent && got.State != store.RoomMessageUncertain {
		t.Fatalf("approved state=%q, want settled", got.State)
	}
}

func TestOverdueWorkerWaitsForSchedulerRecovery(t *testing.T) {
	st, clock, ws, _, other, run := setupCollab(t)
	source := newCollabService(t, st, clock, &collabPTY{}, nil)
	if _, err := source.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "wait for recovery",
		IdempotencyKey: "recovery-gate",
	}); err != nil {
		t.Fatal(err)
	}
	clock.Add(SteeringDelay)

	ready := make(chan struct{})
	delivered := make(chan struct{}, 1)
	worker, err := New(Config{
		Store: st, Runs: st, Workspaces: st, Now: clock.Now,
		Ready: ready, WorkerInterval: time.Hour,
		Inject: func(context.Context, domain.RunID, domain.MemberID, string) error {
			delivered <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-delivered:
		t.Fatal("overdue steer delivered before scheduler recovery")
	case <-time.After(50 * time.Millisecond):
	}
	close(ready)
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("overdue steer not delivered after scheduler recovery")
	}
}

func TestApproveClaimAdmissionFencesTakeover(t *testing.T) {
	st, clock, ws, owner, other, run := setupCollab(t)
	ctl := control.New(control.Config{Now: clock.Now})
	lease, _, err := ctl.Acquire(string(run.ID), string(owner.ID), "moderator", false)
	if err != nil {
		t.Fatal(err)
	}
	barrier := &claimBarrierStore{
		collabStore: st,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	service, err := New(Config{Store: barrier, Runs: barrier, Workspaces: barrier, Control: ctl, Inject: (&collabPTY{}).Inject, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "approve after takeover",
		IdempotencyKey: "approve-admission-barrier",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved := make(chan error, 1)
	go func() {
		_, approveErr := service.ApproveNow(context.Background(), result.Message.ID, owner.ID, lease.SessionID, lease.Generation)
		approved <- approveErr
	}()
	<-barrier.entered

	fenceStarted := make(chan struct{})
	fenced := make(chan struct{})
	go func() {
		close(fenceStarted)
		ctl.Fence(string(run.ID))
		close(fenced)
	}()
	<-fenceStarted
	select {
	case <-fenced:
		t.Fatal("takeover fence crossed before the claim admission released")
	case <-time.After(20 * time.Millisecond):
	}
	close(barrier.release)
	select {
	case <-fenced:
	case <-time.After(time.Second):
		t.Fatal("takeover fence remained blocked after claim release")
	}
	select {
	case err := <-approved:
		if err != nil {
			t.Fatalf("approved steer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("approved steer remained blocked after claim release")
	}
}

func TestModerationRequiresCurrentControllerLease(t *testing.T) {
	st, clock, ws, owner, other, run := setupCollab(t)
	controlService := control.New(control.Config{Now: clock.Now})
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "owner-tab", false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Control: controlService, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	post := func(key string) *store.RoomMessage {
		t.Helper()
		result, postErr := service.Post(context.Background(), MessageInput{
			WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
			Kind: store.RoomMessageSteerRequest, Body: key, IdempotencyKey: key,
		})
		if postErr != nil {
			t.Fatal(postErr)
		}
		return result.Message
	}

	selfApprove := post("self-approve")
	if _, decisionErr := service.ApproveNow(context.Background(), selfApprove.ID, other.ID, lease.SessionID, lease.Generation); !errors.Is(decisionErr, ErrUnauthorizedDecision) {
		t.Fatalf("self approval error = %v, want unauthorized decision", decisionErr)
	}
	selfDeny := post("self-deny")
	if _, decisionErr := service.Deny(context.Background(), selfDeny.ID, other.ID, lease.SessionID, lease.Generation); !errors.Is(decisionErr, ErrUnauthorizedDecision) {
		t.Fatalf("self denial error = %v, want unauthorized decision", decisionErr)
	}

	stale := post("stale-generation")
	if _, decisionErr := service.Deny(context.Background(), stale.ID, owner.ID, lease.SessionID, lease.Generation+1); !errors.Is(decisionErr, ErrUnauthorizedDecision) {
		t.Fatalf("stale denial error = %v, want unauthorized decision", decisionErr)
	}

	currentApprove := post("current-approve")
	if _, approveErr := service.ApproveNow(context.Background(), currentApprove.ID, owner.ID, lease.SessionID, lease.Generation); approveErr != nil {
		t.Fatalf("current controller approval: %v", approveErr)
	}
	currentDeny := post("current-deny")
	if _, denyErr := service.Deny(context.Background(), currentDeny.ID, owner.ID, lease.SessionID, lease.Generation); denyErr != nil {
		t.Fatalf("current controller denial: %v", denyErr)
	}
	got, err := st.GetRoomMessage(context.Background(), currentDeny.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.RoomMessageDenied {
		t.Fatalf("current denial state = %q, want denied", got.State)
	}
}
func TestProtectCancelsQueuedAndFences(t *testing.T) {
	st, clock, ws, owner, other, run := setupCollab(t)
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Control: control.New(control.Config{Now: clock.Now}), Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctl := service.cfg.Control
	lease, _, err := ctl.Acquire(string(run.ID), string(owner.ID), "session", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID, Kind: store.RoomMessageSteerRequest, Body: "queued", IdempotencyKey: "protect"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Protect(context.Background(), run.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRoomMessage(context.Background(), result.Message.ID)
	if got.State != store.RoomMessageCancelled {
		t.Fatalf("state=%q, want cancelled", got.State)
	}
	if _, ok := ctl.Status(string(run.ID)); ok {
		t.Fatal("controller survived protect")
	}
	if err := ctl.Validate(string(run.ID), lease.SessionID, lease.Generation); !errors.Is(err, control.ErrStale) {
		t.Fatalf("validate after fence=%v", err)
	}
}

func TestProtectedRunRejectsOwnerAndAdminSteering(t *testing.T) {
	st, clock, ws, owner, admin, run := setupCollab(t)
	run.Protected = true
	admin.Role = domain.RoleAdmin
	pty := &collabPTY{}
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []*domain.Member{owner, admin} {
		_, postErr := service.Post(context.Background(), MessageInput{
			WorkspaceID: ws.ID, RunID: run.ID, ActorID: actor.ID,
			Kind: store.RoomMessageSteerRequest, Body: "must be refused",
			IdempotencyKey: "protected-" + string(actor.ID),
		})
		if postErr == nil || !errors.Is(postErr, permissions.ErrDenied) {
			t.Fatalf("protected steer by %s = %v, want permission denial", actor.Role, postErr)
		}
	}
	if got := pty.Calls(); got != 0 {
		t.Fatalf("protected steer PTY calls = %d, want 0", got)
	}
	run.Protected = false
	queued, err := service.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: owner.ID,
		Kind: store.RoomMessageSteerRequest, Body: "protect before delivery",
		IdempotencyKey: "protected-delivery",
	})
	if err != nil {
		t.Fatal("queue before protection: ", err)
	}
	run.Protected = true
	clock.Add(SteeringDelay)
	if _, deliveryErr := service.DeliverDue(context.Background(), 10); deliveryErr != nil {
		t.Fatal("deliver after protection: ", deliveryErr)
	}
	got, err := st.GetRoomMessage(context.Background(), queued.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.RoomMessageNotSent {
		t.Fatalf("protected queued state = %q, want not_sent", got.State)
	}
}

func TestRoomIdempotencyAndReceiptClassification(t *testing.T) {
	st, clock, ws, _, other, run := setupCollab(t)
	pty := &collabPTY{}
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	in := MessageInput{WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID, Kind: store.RoomMessageComment, Body: "same", IdempotencyKey: "same"}
	first, err := service.Post(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Post(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Message.ID != second.Message.ID {
		t.Fatalf("idempotency IDs %q and %q differ", first.Message.ID, second.Message.ID)
	}
	if got := ClassifyReceipt(nil); got != ReceiptSent {
		t.Fatalf("nil receipt=%q", got)
	}
	if got := ClassifyReceipt(ptyhost.ErrNoSession); got != ReceiptNotSent {
		t.Fatalf("no session receipt=%q", got)
	}
	if got := ClassifyReceipt(errors.New("write failed")); got != ReceiptUncertain {
		t.Fatalf("write error receipt=%q", got)
	}
}

func TestSteerRetryKeepsOneRoomRowAndDelivery(t *testing.T) {
	st, clock, ws, owner, _, run := setupCollab(t)
	pty := &collabPTY{}
	controlService := control.New(control.Config{Now: clock.Now})
	service, err := New(Config{
		Store: st, Runs: st, Workspaces: st, Control: controlService, Inject: pty.Inject, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "inject-retry", false)
	if err != nil {
		t.Fatal(err)
	}
	in := MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: owner.ID,
		Kind: store.RoomMessageSteerRequest, Body: "continue", IdempotencyKey: "inject-retry",
		ControllerSessionID: lease.SessionID, ControllerGeneration: lease.Generation,
	}
	if _, postErr := service.Post(context.Background(), in); postErr != nil {
		t.Fatal("first inject: ", postErr)
	}
	retry, err := service.Post(context.Background(), in)
	if err != nil {
		t.Fatal("retry inject: ", err)
	}
	if retry.Receipt != ReceiptSent || pty.Calls() != 1 {
		t.Fatalf("retry receipt=%q, PTY calls=%d, want sent and one delivery", retry.Receipt, pty.Calls())
	}
	page, err := st.ListRoomMessages(context.Background(), ws.ID, run.ID, "", 10)
	if err != nil {
		t.Fatal("list room messages: ", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != retry.Message.ID {
		t.Fatalf("room rows=%+v, want one row %q", page.Items, retry.Message.ID)
	}
}
func TestDueDeliveryRechecksAuthorPermission(t *testing.T) {
	st, clock, ws, _, other, run := setupCollab(t)
	pty := &collabPTY{}
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Inject: pty.Inject, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "still allowed?", IdempotencyKey: "revoked-due",
	})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.members[other.ID].Role = domain.RoleViewer
	st.mu.Unlock()
	clock.Add(SteeringDelay)
	if _, deliveryErr := service.DeliverDue(context.Background(), 10); deliveryErr != nil {
		t.Fatal(deliveryErr)
	}
	got, err := st.GetRoomMessage(context.Background(), result.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.RoomMessageNotSent {
		t.Fatalf("revoked due state=%q, want not_sent", got.State)
	}
	if pty.Calls() != 0 {
		t.Fatalf("revoked due PTY calls=%d, want 0", pty.Calls())
	}
}

func TestApprovedDeliveryUsesRequestActorAndAttachments(t *testing.T) {
	st, clock, ws, owner, other, run := setupCollab(t)
	controlService := control.New(control.Config{Now: clock.Now})
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "moderator", false)
	if err != nil {
		t.Fatal(err)
	}
	var injectedActor domain.MemberID
	var injectedMessage string
	service, err := New(Config{
		Store: st, Runs: st, Workspaces: st, Control: controlService, Now: clock.Now,
		Inject: func(_ context.Context, gotRun domain.RunID, actor domain.MemberID, message string) error {
			if gotRun != run.ID {
				t.Fatalf("injected run=%q, want %q", gotRun, run.ID)
			}
			injectedActor, injectedMessage = actor, message
			return nil
		},
		Attachments: func(context.Context, domain.WorkspaceID, domain.RunID, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(context.Background(), MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "inspect this",
		Attachments:    []string{"home/.aether/image-1.png", "home/.aether/image-2.png"},
		IdempotencyKey: "approved-attachments",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveNow(context.Background(), result.Message.ID, owner.ID, lease.SessionID, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if injectedActor != other.ID {
		t.Fatalf("injected actor=%q, want request actor %q", injectedActor, other.ID)
	}
	for _, ref := range []string{"home/.aether/image-1.png", "home/.aether/image-2.png"} {
		if !strings.Contains(injectedMessage, ref) {
			t.Fatalf("injected message %q missing attachment %q", injectedMessage, ref)
		}
	}
	if !strings.Contains(injectedMessage, "--- AETHER ATTACHMENTS ---") ||
		!strings.Contains(injectedMessage, "--- END AETHER ATTACHMENTS ---") {
		t.Fatalf("injected message lacks attachment framing: %q", injectedMessage)
	}
}

func TestStatusCountsQueuedSteerBeyondFirstPage(t *testing.T) {
	st, clock, ws, _, other, run := setupCollab(t)
	service, err := New(Config{Store: st, Runs: st, Workspaces: st, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	queued := &store.RoomMessage{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
		Kind: store.RoomMessageSteerRequest, Body: "queued",
		IdempotencyKey: "queued-status",
	}
	if createErr := st.CreateRoomMessage(context.Background(), queued); createErr != nil {
		t.Fatal(createErr)
	}
	for i := range 101 {
		comment := &store.RoomMessage{
			WorkspaceID: ws.ID, RunID: run.ID, ActorID: other.ID,
			Kind: store.RoomMessageComment, Body: "newer",
			IdempotencyKey: fmt.Sprintf("comment-status-%d", i),
		}
		if createErr := st.CreateRoomMessage(context.Background(), comment); createErr != nil {
			t.Fatal(createErr)
		}
	}
	status, err := service.Status(context.Background(), ws.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.QueuedSteers != 1 {
		t.Fatalf("queued steers = %d, want 1", status.QueuedSteers)
	}
}
