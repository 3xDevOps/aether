package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type schedulerEvidenceCapture struct {
	mu              sync.Mutex
	workspace       domain.WorkspaceID
	failures        int
	reqs            []evidence.Request
	packets         map[string]protocol.EvidencePacket
	durable         map[string]bool
	beforeCalls     int
	cleanupObserved bool
	purgeCalls      int
}

func newSchedulerEvidenceCapture(workspace domain.WorkspaceID) *schedulerEvidenceCapture {
	return &schedulerEvidenceCapture{
		workspace: workspace,
		packets:   make(map[string]protocol.EvidencePacket),
		durable:   make(map[string]bool),
	}
}

func (f *schedulerEvidenceCapture) Capture(_ context.Context, req evidence.Request) (protocol.EvidencePacket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if f.failures > 0 {
		f.failures--
		return protocol.EvidencePacket{}, errors.New("evidence capture unavailable")
	}
	if packet, ok := f.packets[req.IdempotencyKey]; ok {
		return packet, nil
	}
	origin := protocol.EvidenceOrigin{Kind: protocol.EvidenceOriginKind(req.Origin.Kind), ID: req.Origin.ID}
	creator := ""
	if origin.Kind == protocol.EvidenceOriginHuman {
		creator = origin.ID
	}
	packet := protocol.EvidencePacket{
		ID:            fmt.Sprintf("packet-%d", len(f.packets)+1),
		WorkspaceID:   string(f.workspace),
		RunID:         string(req.RunID),
		Origin:        origin,
		CreatorID:     creator,
		Trigger:       protocol.EvidenceFinish,
		EventBoundary: uint64(len(f.packets) + 1),
	}
	f.packets[req.IdempotencyKey] = packet
	f.durable[req.IdempotencyKey] = true
	return packet, nil
}

func (f *schedulerEvidenceCapture) CaptureBeforeCleanup(ctx context.Context, req evidence.Request, cleanup func(context.Context) error) (protocol.EvidencePacket, error) {
	packet, err := f.Capture(ctx, req)
	if err != nil {
		return packet, err
	}
	f.mu.Lock()
	f.beforeCalls++
	f.cleanupObserved = f.durable[req.IdempotencyKey]
	f.mu.Unlock()
	if cleanup != nil {
		if err := cleanup(ctx); err != nil {
			return packet, err
		}
	}
	return packet, nil
}

func (f *schedulerEvidenceCapture) PurgeRun(ctx context.Context, _ domain.WorkspaceID, _ domain.RunID, cleanup func(context.Context) error) error {
	f.mu.Lock()
	f.purgeCalls++
	f.mu.Unlock()
	if cleanup == nil {
		return nil
	}
	return cleanup(ctx)
}

func (f *schedulerEvidenceCapture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *schedulerEvidenceCapture) packetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.packets)
}

func (f *schedulerEvidenceCapture) lastRequest() evidence.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

type schedulerRoomPageStore struct {
	store.Store
	pages map[string]*store.RoomMessagePage
	calls []string
}

func (s *schedulerRoomPageStore) ListRoomMessages(_ context.Context, _ domain.WorkspaceID, _ domain.RunID, before string, _ int) (*store.RoomMessagePage, error) {
	s.calls = append(s.calls, before)
	return s.pages[before], nil
}

type publicationErrorStore struct {
	store.Store
	row          *store.EvidencePublication
	publishedErr error
	failureErr   error
}

func (s *publicationErrorStore) ListPendingEvidencePublications(context.Context, time.Time, string, int) ([]*store.EvidencePublication, string, error) {
	return []*store.EvidencePublication{s.row}, "", nil
}

func (s *publicationErrorStore) MarkEvidencePublicationPublished(context.Context, string, time.Time) error {
	return s.publishedErr
}

func (s *publicationErrorStore) MarkEvidencePublicationFailure(context.Context, string, int, time.Time, string) error {
	return s.failureErr
}

type failingEvidenceBus struct {
	events.Bus
	err error
}

func (b failingEvidenceBus) Publish(context.Context, events.Event) (events.Event, error) {
	return events.Event{}, b.err
}

type recordingSlogHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *recordingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingSlogHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	h.messages = append(h.messages, record.Message)
	h.mu.Unlock()
	return nil
}

func (h *recordingSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingSlogHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingSlogHandler) hasMessage(want string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, message := range h.messages {
		if message == want {
			return true
		}
	}
	return false
}

type failingRetainedCloseStore struct {
	store.Store
	err error
}

func (s *failingRetainedCloseStore) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, startedAt, finishedAt *time.Time) error {
	if reason == retainedCloseReason {
		return s.err
	}
	return s.Store.UpdateRunStatus(ctx, id, status, reason, startedAt, finishedAt)
}

func TestFinalizeLogsRetainedSidecarFailure(t *testing.T) {
	e := newTestEnv(t, nil)
	destroyErr := errors.New("test: destroy unavailable")
	e.sched.cfg.Runtime = &destroyFailureRuntime{Runtime: e.rt, destroyErr: destroyErr}
	run, container := e.launchFake(t, "retained sidecar diagnostics")
	e.sched.cfg.StateDir = filepath.Join(e.cfg.StateDir, "missing")

	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	container.exitNow(1)
	e.waitStoreStatus(t, run.ID, domain.RunFailed)
	waitFor(t, "retained sidecar failure log", func() bool {
		return handler.hasMessage("scheduler: persist retained sidecar after destroy failure")
	})
	e.sched.mu.Lock()
	retained := e.sched.runs[run.ID] != nil && e.sched.runs[run.ID].retained
	e.sched.mu.Unlock()
	if !retained {
		t.Fatal("destroy failure did not retain the run owner")
	}
	if !handler.hasMessage("scheduler: destroy container") {
		t.Fatal("primary destroy failure was not logged")
	}
}

func TestCloseRunLogsRetainedTransitionFailure(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.failures = 1
	e.sched.UseEvidence(capture)
	run, container := e.launchFake(t, "retained transition diagnostics")
	transitionErr := errors.New("test: retained close transition unavailable")
	e.sched.cfg.Store = &failingRetainedCloseStore{Store: e.db, err: transitionErr}

	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged)
	if err == nil || !strings.Contains(err.Error(), "evidence capture unavailable") {
		t.Fatalf("CloseRun error = %v, want primary evidence failure", err)
	}
	if !handler.hasMessage("scheduler: retain run after evidence capture failure") {
		t.Fatal("primary evidence capture failure was not logged")
	}
	if !handler.hasMessage("scheduler: retained close transition after evidence capture failure") {
		t.Fatal("retained close transition failure was not logged")
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if row.Reason != "closed" {
		t.Fatalf("row reason after failed evidence capture = %q, want closed", row.Reason)
	}
	if got := container.currentState(); got != "paused" {
		t.Fatalf("container state after failed evidence capture = %q, want paused", got)
	}
}

func TestEvidencePublicationStateErrorsAreLogged(t *testing.T) {
	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "publication state",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	packet := &store.EvidencePacket{
		WorkspaceID: e.ws.ID, RunID: run.ID,
		Origin:  store.EvidenceOrigin{Kind: store.EvidenceOriginServer},
		Trigger: store.EvidenceFinish, IdempotencyKey: "publication-state-errors",
	}
	if err := e.db.CreateEvidencePacket(t.Context(), packet); err != nil {
		t.Fatalf("CreateEvidencePacket: %v", err)
	}
	outbox := &publicationErrorStore{
		Store: e.db, row: &store.EvidencePublication{PacketID: packet.ID, Attempts: 2},
		publishedErr: errors.New("published state unavailable"),
	}
	e.sched.cfg.Store = outbox
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e.sched.cfg.Bus = e.bus
	e.sched.drainEvidencePublications(t.Context())
	if !handler.hasMessage("scheduler: mark evidence publication published") {
		t.Fatal("published-state update failure was not logged")
	}

	outbox.publishedErr = nil
	outbox.failureErr = errors.New("failure state unavailable")
	e.sched.cfg.Bus = failingEvidenceBus{Bus: e.bus, err: errors.New("bus unavailable")}
	e.sched.drainEvidencePublications(t.Context())
	if !handler.hasMessage("scheduler: mark evidence publication failure") {
		t.Fatal("failure-state update failure was not logged")
	}
}

func TestFinishEvidenceCapturePublishesAfterDurableCapture(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run, container := e.launchFake(t, "capture finished work")
	sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Run: run.ID, Types: []events.Type{events.TypeEvidencePacket}},
	})
	if err != nil {
		t.Fatalf("Subscribe evidence: %v", err)
	}
	defer func() { _ = sub.Close() }()
	if err := e.db.CreateRoomMessage(t.Context(), &store.RoomMessage{
		WorkspaceID: e.ws.ID, RunID: run.ID, ActorID: e.member.ID,
		Kind: store.RoomMessageQuestion, Body: "Which API should be preferred?",
		Anchor:         &store.RoomAnchor{Kind: "diff", Path: "main.go", StartLine: 4, EndLine: 4},
		IdempotencyKey: "question-1",
	}); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	container.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	waitFor(t, "finish evidence", func() bool { return capture.packetCount() == 1 })
	select {
	case event := <-sub.Events():
		packet, ok := event.Payload.(events.EvidencePacketPayload)
		if !ok || packet.PacketID == "" || packet.Origin.Kind != string(store.EvidenceOriginServer) ||
			packet.Origin.ID != "" || event.ActorID != "" {
			t.Fatalf("evidence event payload = %#v actor=%q", event.Payload, event.ActorID)
		}
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for evidence event")
	}
	request := capture.lastRequest()
	if request.Origin.Kind != store.EvidenceOriginServer || request.Origin.ID != "" {
		t.Fatalf("finish origin = %+v", request.Origin)
	}
	if request.Trigger != store.EvidenceFinish || request.IdempotencyKey != "finish:"+string(run.ID)+":tip:completed" {
		t.Fatalf("finish request = %+v", request)
	}
	if len(request.RelatedRoomMessageIDs) != 1 || len(request.UnresolvedFacts) != 1 {
		t.Fatalf("room evidence = ids %v facts %v", request.RelatedRoomMessageIDs, request.UnresolvedFacts)
	}
	if len(request.SourceFacts) != 1 || request.SourceFacts[0].Name != "room" ||
		!request.SourceFacts[0].Available || request.SourceFacts[0].Truncated {
		t.Fatalf("room source fact = %+v", request.SourceFacts)
	}
	waitFor(t, "container cleanup", func() bool { return e.rt.byName(string(run.ID)) == nil })
}
func TestRoomEvidencePaginatesAndExcludesAnsweredQuestions(t *testing.T) {
	e := newTestEnv(t, nil)
	answered := &store.RoomMessage{
		ID: "question-answered", WorkspaceID: e.ws.ID, RunID: "run-room",
		Kind: store.RoomMessageQuestion, State: store.RoomMessageSent,
		Body: "answered question", Anchor: &store.RoomAnchor{Kind: "diff", Path: "a.go"},
	}
	open := &store.RoomMessage{
		ID: "question-open", WorkspaceID: e.ws.ID, RunID: "run-room",
		Kind: store.RoomMessageQuestion, State: store.RoomMessageSent,
		Body: "open question", Anchor: &store.RoomAnchor{Kind: "diff", Path: "b.go"},
	}
	reply := &store.RoomMessage{
		ID: "reply-answered", WorkspaceID: e.ws.ID, RunID: "run-room",
		Kind: store.RoomMessageReply, State: store.RoomMessageSent,
		CorrelationID: answered.ID,
	}
	older := &store.RoomMessage{
		ID: "question-older", WorkspaceID: e.ws.ID, RunID: "run-room",
		Kind: store.RoomMessageQuestion, State: store.RoomMessageSent,
		Body: "older open question",
	}
	paging := &schedulerRoomPageStore{
		Store: e.db,
		pages: map[string]*store.RoomMessagePage{
			"":      {Items: []*store.RoomMessage{answered, open}, NextBefore: "older"},
			"older": {Items: []*store.RoomMessage{reply, older}},
		},
	}
	e.sched.cfg.Store = paging

	ids, facts, source := e.sched.roomEvidenceWithFact(t.Context(), e.ws.ID, "run-room")
	if len(paging.calls) != 2 || paging.calls[0] != "" || paging.calls[1] != "older" {
		t.Fatalf("room page cursors = %v", paging.calls)
	}
	if !source.Available || source.Truncated || source.Name != "room" {
		t.Fatalf("room source = %+v", source)
	}
	if len(ids) != 2 || ids[0] != answered.ID || ids[1] != open.ID {
		t.Fatalf("room related ids = %v", ids)
	}
	if len(facts) != 2 {
		t.Fatalf("room unresolved facts = %v", facts)
	}
	for _, fact := range facts {
		if strings.Contains(fact, answered.ID) {
			t.Fatalf("answered question reported unresolved: %v", facts)
		}
	}
	if !strings.Contains(facts[0], open.ID) && !strings.Contains(facts[1], open.ID) {
		t.Fatalf("open question missing from facts: %v", facts)
	}
	if !strings.Contains(facts[0], older.ID) && !strings.Contains(facts[1], older.ID) {
		t.Fatalf("older question missing from facts: %v", facts)
	}
}
func TestRoomEvidenceMarksOmittedOlderPagesTruncated(t *testing.T) {
	e := newTestEnv(t, nil)
	pages := map[string]*store.RoomMessagePage{"": {NextBefore: "page-1"}}
	for i := 1; i <= schedulerEvidenceRoomMaxPages; i++ {
		before := fmt.Sprintf("page-%d", i)
		next := ""
		if i < schedulerEvidenceRoomMaxPages {
			next = fmt.Sprintf("page-%d", i+1)
		}
		pages[before] = &store.RoomMessagePage{NextBefore: next}
	}
	paging := &schedulerRoomPageStore{Store: e.db, pages: pages}
	e.sched.cfg.Store = paging

	_, _, source := e.sched.roomEvidenceWithFact(t.Context(), e.ws.ID, "run-room")
	if !source.Available || !source.Truncated || source.Reason == "" {
		t.Fatalf("truncated room source = %+v", source)
	}
	if len(paging.calls) != schedulerEvidenceRoomMaxPages {
		t.Fatalf("room pages fetched = %d, want %d", len(paging.calls), schedulerEvidenceRoomMaxPages)
	}
}

func TestFailedFinishCaptureRetainsResources(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.failures = 1
	e.sched.UseEvidence(capture)
	run, container := e.launchFake(t, "retain on evidence failure")
	container.exitNow(3)
	e.waitStoreStatus(t, run.ID, domain.RunFailed)
	waitFor(t, "retained evidence sidecar", func() bool {
		sc, err := e.sched.readSidecar(run.ID)
		return err == nil && sc.Retained
	})
	if got := capture.requestCount(); got != 1 {
		t.Fatalf("finish capture requests = %d, want 1", got)
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("container destroyed after required evidence capture failed")
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.EvidenceIdentity != "tip" {
		t.Fatalf("retained sidecar = %+v, %v", sc, err)
	}
}

func TestRetainedEvidenceRetryThenCleanup(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.failures = 1
	e.sched.UseEvidence(capture)
	run, container := e.launchFake(t, "retry retained evidence")
	container.exitNow(1)
	e.waitStoreStatus(t, run.ID, domain.RunFailed)
	waitFor(t, "retained evidence sidecar", func() bool {
		sc, err := e.sched.readSidecar(run.ID)
		return err == nil && sc.Retained
	})
	e.sched.sweepRetained(t.Context())
	waitFor(t, "retained container cleanup", func() bool { return e.rt.byName(string(run.ID)) == nil })
	waitFor(t, "retained sidecar cleanup", func() bool {
		_, err := e.sched.readSidecar(run.ID)
		return errors.Is(err, os.ErrNotExist)
	})
	if capture.packetCount() != 1 {
		t.Fatalf("packets after retry = %d, want one", capture.packetCount())
	}
}

func TestFinishEvidenceCrashRecoveryIsIdempotent(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "idempotent finish evidence")
	if err := e.sched.captureFinishEvidence(t.Context(), run.ID, domain.RunCompleted, "tip"); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.captureFinishEvidence(t.Context(), run.ID, domain.RunCompleted, "tip"); err != nil {
		t.Fatal(err)
	}
	if capture.packetCount() != 1 || capture.requestCount() != 2 {
		t.Fatalf("capture idempotency packets=%d requests=%d", capture.packetCount(), capture.requestCount())
	}
}

func TestDeleteRunRoutesEvidencePurgeBeforeSources(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "delete with evidence",
		Harness:     "claude",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunFailed,
		CreatedAt:   time.Now().UTC(),
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.purgeCalls != 1 {
		t.Fatalf("evidence purge calls = %d, want 1", capture.purgeCalls)
	}
}

func TestExplicitCloseCapturesFinishEvidence(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "explicit close evidence")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	if capture.packetCount() != 1 {
		t.Fatalf("close packets = %d, want one", capture.packetCount())
	}
	request := capture.lastRequest()
	if request.IdempotencyKey != "finish:"+string(run.ID)+":tip:merged" {
		t.Fatalf("close request = %+v", request)
	}
}

func TestAutomaticCheckoutCleanupCapturesBeforeRemoval(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
		cfg.CheckoutTTL = time.Hour
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "cleanup evidence")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	row.FinishedAt = &old
	if err = e.db.UpdateRun(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCheckouts(t.Context())
	if capture.beforeCalls != 1 || !capture.cleanupObserved {
		t.Fatalf("before-cleanup capture = calls %d durable=%v", capture.beforeCalls, capture.cleanupObserved)
	}
	fresh, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Worktree != "" {
		t.Fatalf("worktree after automatic cleanup = %q", fresh.Worktree)
	}
}

func TestRoomEvidenceMarksTruncationWhenCursorProves401stMessage(t *testing.T) {
	e := newTestEnv(t, nil)
	pages := make(map[string]*store.RoomMessagePage, schedulerEvidenceRoomMaxPages)
	before := ""
	for page := 1; page <= schedulerEvidenceRoomMaxPages; page++ {
		items := make([]*store.RoomMessage, schedulerEvidenceRoomLimit)
		for i := range items {
			items[i] = &store.RoomMessage{
				ID:          fmt.Sprintf("room-%03d", (page-1)*schedulerEvidenceRoomLimit+i),
				WorkspaceID: e.ws.ID, RunID: "run-room",
				Kind: store.RoomMessageComment, State: store.RoomMessageSent,
			}
		}
		next := fmt.Sprintf("page-%d", page+1)
		pages[before] = &store.RoomMessagePage{Items: items, NextBefore: next}
		before = next
	}
	paging := &schedulerRoomPageStore{Store: e.db, pages: pages}
	e.sched.cfg.Store = paging

	_, _, source := e.sched.roomEvidenceWithFact(t.Context(), e.ws.ID, "run-room")
	if !source.Available || !source.Truncated || source.Reason != "room history truncated" {
		t.Fatalf("401-message room source = %+v, want exact truncation", source)
	}
	if len(paging.calls) != schedulerEvidenceRoomMaxPages {
		t.Fatalf("room pages fetched = %d, want %d", len(paging.calls), schedulerEvidenceRoomMaxPages)
	}
}
