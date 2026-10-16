package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type memStore struct {
	mu   sync.Mutex
	rows map[domain.MemberID]*store.AssistantSession
	keys map[string]bool
	auto map[domain.MemberID]bool
	// hidden holds the members who turned view sharing off.
	hidden map[domain.MemberID]bool
	// autoReads counts AssistantAutoApprove calls.
	autoReads int
}

func newMemStore() *memStore {
	return &memStore{rows: map[domain.MemberID]*store.AssistantSession{}, keys: map[string]bool{}, auto: map[domain.MemberID]bool{}, hidden: map[domain.MemberID]bool{}}
}

func (m *memStore) AssistantAutoApprove(_ context.Context, id domain.MemberID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoReads++
	return m.auto[id], nil
}

func (m *memStore) SetAssistantAutoApprove(_ context.Context, id domain.MemberID, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auto[id] = on
	return nil
}

func (m *memStore) AssistantShareView(_ context.Context, id domain.MemberID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.hidden[id], nil
}

func (m *memStore) SetAssistantShareView(_ context.Context, id domain.MemberID, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hidden[id] = !on
	return nil
}

func (m *memStore) GetAssistantSession(_ context.Context, id domain.MemberID) (*store.AssistantSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row := m.rows[id]; row != nil {
		c := *row
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) PutAssistantSession(_ context.Context, s *store.AssistantSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[s.MemberID]
	if row == nil {
		row = &store.AssistantSession{MemberID: s.MemberID}
		m.rows[s.MemberID] = row
	}
	row.Agent, row.HarnessSessionID, row.ExecIdentity = s.Agent, s.HarnessSessionID, s.ExecIdentity
	return nil
}

func (m *memStore) ClearAssistantExec(_ context.Context, id domain.MemberID, generation int64, identity string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row := m.rows[id]; row != nil && row.Generation == generation && row.ExecIdentity == identity {
		row.ExecIdentity = ""
	}
	return nil
}

func (m *memStore) ResetAssistantSession(_ context.Context, id domain.MemberID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[id]
	if row == nil {
		row = &store.AssistantSession{MemberID: id}
		m.rows[id] = row
	}
	row.Agent, row.HarnessSessionID = "", ""
	row.Generation++
	m.keys = map[string]bool{}
	return row.Generation, nil
}

func keyOf(id domain.MemberID, generation int64, key string) string {
	return string(id) + "/" + string(rune('0'+generation)) + "/" + key
}

func (m *memStore) HasAssistantPromptKey(_ context.Context, id domain.MemberID, generation int64, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keys[keyOf(id, generation, key)], nil
}

func (m *memStore) AddAssistantPromptKey(_ context.Context, id domain.MemberID, generation int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[keyOf(id, generation, key)] = true
	return nil
}

func TestResolveNeverSwitchesUnasked(t *testing.T) {
	agents := []protocol.AssistantAgent{
		{Name: "claude", DisplayName: "Claude Code", Reason: "no login was found"},
		{Name: "codex", DisplayName: "Codex", Ready: true},
	}
	_, err := resolve(agents, "", "claude")
	var notReady *AgentNotReadyError
	if !errors.As(err, &notReady) || err.Error() != "Claude Code is not ready: no login was found; sign in again or start with another agent" {
		t.Fatalf("resolve = %v, want the stored agent's reason", err)
	}
	if got, err := resolve(agents, "codex", "claude"); err != nil || got != "codex" {
		t.Fatalf("explicit request = %q, %v, want codex", got, err)
	}
	if got, err := resolve(agents, "", ""); err != nil || got != "codex" {
		t.Fatalf("no stored agent = %q, %v, want the first ready", got, err)
	}
}

func TestPromptKeyIsSharedByDuplicates(t *testing.T) {
	s := New(Config{Store: newMemStore()})
	s.cancel()
	m := s.member("m1")
	m.op.Lock()
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- s.Prompt(t.Context(), "m1", "hi", "k", nil) }()
	}
	time.Sleep(50 * time.Millisecond)
	m.op.Unlock()
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("prompt = %v, want the first attempt's failure for both", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a duplicate never returned")
		}
	}
	if len(m.attempts) != 0 {
		t.Fatalf("failed key kept: %v", m.attempts)
	}
}

func TestPersistedPromptKeyIsIgnoredAfterRestart(t *testing.T) {
	db := newMemStore()
	if err := db.AddAssistantPromptKey(t.Context(), "m1", 0, "k"); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Store: db})
	s.cancel()
	if err := s.Prompt(t.Context(), "m1", "hi", "k", nil); err != nil {
		t.Fatalf("a key accepted before the restart ran again: %v", err)
	}
	if err := s.Prompt(t.Context(), "m1", "hi", "other", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("a new key = %v, want it to run", err)
	}
}

func TestResetStartsANewGeneration(t *testing.T) {
	dir := t.TempDir()
	db := newMemStore()
	s := New(Config{Dir: dir, Store: db})
	m := s.member("m1")
	log := func(items int) {
		l, err := acphost.OpenLog(s.logPath("m1"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		for range items {
			if err := l.Append(&acphost.Item{Kind: acphost.KindNotice, Notice: &acphost.Notice{Title: "n"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	log(3)
	viewer := Epoch(0, 0)
	m.op.Lock()
	err := s.resetLocked(t.Context(), m)
	m.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(s.logPath("m1")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("log after reset: %v", statErr)
	}
	if generation, _ := s.Generation(t.Context(), "m1"); generation != 1 {
		t.Fatalf("generation = %d, want 1", generation)
	}
	log(5)

	stale, err := s.Subscribe(t.Context(), "m1", 3, viewer)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Cancel()
	if len(stale.Replay) != 5 || stale.Epoch/EpochStride != 2 || stale.Epoch == viewer {
		t.Fatalf("viewer of the old conversation got %d items at epoch %d, want everything at a new epoch", len(stale.Replay), stale.Epoch)
	}
	current, err := s.Subscribe(t.Context(), "m1", 3, stale.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Cancel()
	if len(current.Replay) != 2 {
		t.Fatalf("current viewer got %d items, want the 2 after its cursor", len(current.Replay))
	}
}

func TestRemoveDeletesMemberFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{Dir: dir})
	if err := os.MkdirAll(filepath.Join(dir, "m1", "sock"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.member("m1")
	if err := s.Remove(t.Context(), "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("member directory after Remove: %v", err)
	}
	if _, kept := s.members["m1"]; kept {
		t.Fatal("member kept in memory")
	}
}

type fakeExec struct{}

func (fakeExec) Identity() runtime.ExecIdentity { return runtime.ExecIdentity{} }
func (fakeExec) Attachment() runtime.Attachment { return nil }
func (fakeExec) Status(context.Context) (runtime.ExecState, error) {
	return runtime.ExecState{}, nil
}
func (fakeExec) Wait(context.Context) (runtime.ExitStatus, error) { return runtime.ExitStatus{}, nil }
func (fakeExec) Stop(context.Context, time.Duration) (runtime.ExitStatus, error) {
	return runtime.ExitStatus{}, nil
}
func (fakeExec) Resize(context.Context, uint, uint) error { return nil }
func (fakeExec) Detach() error                            { return nil }

// A session that ended earlier cleans up late; that must not undo a reset or
// blank the exec identity of the session that replaced it.
func TestLateEndExecLeavesNewerRowAlone(t *testing.T) {
	db := newMemStore()
	s := New(Config{Store: db})
	old := func() *session {
		return &session{agent: "claude", harnessID: "old", generation: 0, identity: "OLD", exec: fakeExec{}}
	}

	db.rows["m1"] = &store.AssistantSession{MemberID: "m1", Generation: 1}
	if err := s.endExec(old(), "m1"); err != nil {
		t.Fatal(err)
	}
	if row := db.rows["m1"]; row.HarnessSessionID != "" || row.Agent != "" || row.Generation != 1 {
		t.Fatalf("row after a reset and a late cleanup = %+v, want the old session not resurrected", row)
	}

	db.rows["m1"] = &store.AssistantSession{MemberID: "m1", Agent: "claude", HarnessSessionID: "new", ExecIdentity: "NEW"}
	if err := s.endExec(old(), "m1"); err != nil {
		t.Fatal(err)
	}
	if row := db.rows["m1"]; row.ExecIdentity != "NEW" {
		t.Fatalf("exec identity = %q after the old session's cleanup, want NEW kept", row.ExecIdentity)
	}

	db.rows["m1"] = &store.AssistantSession{MemberID: "m1", ExecIdentity: "OLD"}
	if err := s.endExec(old(), "m1"); err != nil {
		t.Fatal(err)
	}
	if row := db.rows["m1"]; row.ExecIdentity != "" {
		t.Fatalf("exec identity = %q, want the stopped adapter forgotten", row.ExecIdentity)
	}
}

// A reset holds the lock a subscription takes, so the generation, the replay
// and the epoch always describe the same conversation.
func TestSubscribeWaitsForReset(t *testing.T) {
	s := New(Config{Dir: t.TempDir(), Store: newMemStore()})
	m := s.member("m1")
	m.reset.Lock()
	done := make(chan error, 1)
	go func() {
		stream, err := s.Subscribe(t.Context(), "m1", 0, 0)
		if err == nil {
			stream.Cancel()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("a subscription ran while a reset was in progress")
	case <-time.After(100 * time.Millisecond):
	}
	m.reset.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription never finished")
	}
}
