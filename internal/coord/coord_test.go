package coord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// fakePeers stands in for the conflict radar index. reads counts how often
// the live view was actually recomputed.
type fakePeers struct {
	mu      sync.Mutex
	entries []overlap.Entry
	err     error
	reads   int
}

func (f *fakePeers) Overlaps(context.Context) ([]overlap.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return append([]overlap.Entry(nil), f.entries...), nil
}

func (f *fakePeers) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// hub puts one run in conflict with every other, which is the shape a run
// that touched every tracked file produces.
func (f *fakePeers) hub(a domain.RunID, others []domain.RunID, files ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hub := overlap.Entry{RunID: a}
	f.entries = nil
	for _, b := range others {
		hub.With = append(hub.With, overlap.Peer{RunID: b, Files: files})
		f.entries = append(f.entries, overlap.Entry{RunID: b, With: []overlap.Peer{{RunID: a, Files: files}}})
	}
	f.entries = append(f.entries, hub)
}

// pair puts two runs in conflict over files, from both sides, the way the
// real index reports it.
func (f *fakePeers) pair(a, b domain.RunID, files ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = []overlap.Entry{
		{RunID: a, With: []overlap.Peer{{RunID: b, Files: files}}},
		{RunID: b, With: []overlap.Peer{{RunID: a, Files: files}}},
	}
}

func (f *fakePeers) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = nil
}

// coordHarness is a coordination service over a real store and bus, with
// the radar faked and the clock under test control.
type coordHarness struct {
	t         *testing.T
	dir       string
	db        *store.DB
	bus       *events.InProc
	peers     *fakePeers
	svc       *Service
	workspace domain.WorkspaceID
	runs      []*domain.Run

	clockMu sync.Mutex
	clock   time.Time
}

var testSendSeq atomic.Uint64

type testEvidenceCapture struct {
	workspace domain.WorkspaceID
}

func (c testEvidenceCapture) Capture(_ context.Context, req evidence.Request) (protocol.EvidencePacket, error) {
	return protocol.EvidencePacket{
		Trigger:        protocol.EvidenceReport,
		WorkspaceID:    string(c.workspace),
		RunID:          string(req.RunID),
		CreatorID:      string(req.CreatorID),
		IdempotencyKey: req.IdempotencyKey,
	}, nil
}

func sendParams(to domain.RunID, body string) protocol.CoordSendParams {
	return protocol.CoordSendParams{
		ToRunID: string(to), Body: body,
		IdempotencyKey: fmt.Sprintf("test-send-%d", testSendSeq.Add(1)),
	}
}

func newHarness(t *testing.T, runs int, opts ...func(*Config)) *coordHarness {
	t.Helper()
	ctx := context.Background()
	// Unix socket paths must not grow with the test or subtest name.
	dir, err := os.MkdirTemp("", "ac-")
	if err != nil {
		t.Fatalf("create coordination fixture directory: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			t.Errorf("remove coordination fixture directory: %v", cleanupErr)
		}
	})
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	ws := &domain.Workspace{
		Name:        "proj",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if werr := db.CreateWorkspace(ctx, ws); werr != nil {
		t.Fatalf("create workspace: %v", werr)
	}
	mem := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#e6194b", Role: domain.RoleCollaborator}
	if merr := db.CreateMember(ctx, mem); merr != nil {
		t.Fatalf("create member: %v", merr)
	}

	h := &coordHarness{
		t:         t,
		dir:       dir,
		db:        db,
		bus:       bus,
		peers:     &fakePeers{},
		workspace: ws.ID,
		clock:     time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}
	for i := range runs {
		r := &domain.Run{
			WorkspaceID: ws.ID,
			MemberID:    mem.ID,
			Task:        fmt.Sprintf("task %d", i),
			Harness:     "claude",
			Mode:        domain.LaunchTUI,
			Status:      domain.RunRunning,
		}
		if rerr := db.CreateRun(ctx, r); rerr != nil {
			t.Fatalf("create run %d: %v", i, rerr)
		}
		h.runs = append(h.runs, r)
	}

	cfg := Config{
		Dir:      filepath.Join(dir, "coord"),
		Store:    db,
		Mail:     db,
		Bus:      bus,
		Peers:    h.peers,
		Evidence: testEvidenceCapture{workspace: ws.ID},
		now:      h.now,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.svc = svc
	t.Cleanup(func() { _ = svc.Close() })
	return h
}

func (h *coordHarness) now() time.Time {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	return h.clock
}

func (h *coordHarness) advance(d time.Duration) {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *coordHarness) run(i int) domain.RunID { return h.runs[i].ID }

func (h *coordHarness) start() {
	h.t.Helper()
	if err := h.svc.Start(context.Background()); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
}

// TestKillSwitchRejectsEveryMethodEarly proves the switch is checked
// before anything is touched: no mailbox row, no radar read, no listener.
func TestKillSwitchRejectsEveryMethodEarly(t *testing.T) {
	h := newHarness(t, 2, func(c *Config) { c.Disabled = true })
	h.peers.err = errors.New("the radar must not be consulted")
	ctx := context.Background()
	h.start()

	if _, err := h.svc.Status(ctx, h.run(0)); err == nil || err.Code != protocol.CodeUnavailable {
		t.Fatalf("Status = %v, want CodeUnavailable", err)
	}
	if _, err := h.svc.Send(ctx, h.run(0), sendParams(h.run(1), "hi")); err == nil ||
		err.Code != protocol.CodeUnavailable || err.Message != "coord.send: conflict coordination is disabled" {
		t.Fatalf("Send = %v, want the pinned CodeUnavailable", err)
	}
	if _, err := h.svc.Inbox(ctx, h.run(1), protocol.CoordInboxParams{}); err == nil || err.Code != protocol.CodeUnavailable {
		t.Fatalf("Inbox = %v, want CodeUnavailable", err)
	}
	if _, err := h.svc.Provision(ctx, h.run(0), nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Provision = %v, want ErrDisabled", err)
	}
	if n, err := h.db.CountUnackedRunMessages(ctx, h.run(1)); err != nil || n != 0 {
		t.Fatalf("stored messages = %d (err %v), want none", n, err)
	}
}
