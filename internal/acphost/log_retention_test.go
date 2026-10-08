package acphost

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestLogCheckpointRestoresActiveTurnAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{
		{Kind: KindReset, Turn: 6},
		{Kind: KindModeChange, Turn: 6, Mode: "ask"},
		{Kind: KindTurnStart, Turn: 7},
		{Kind: KindMessage, Turn: 7, Message: &Message{Text: "still working"}},
	} {
		if appendErr := l.Append(&it); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	// Keep only the message: the turn boundary and mode are now private state.
	if compactErr := l.compact(l.size - l.offsets[3]); compactErr != nil {
		t.Fatal(compactErr)
	}
	if closeErr := l.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"seq":5,"kind":"turn_end"`)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if writeErr := os.WriteFile(path+".compact", []byte(`{"_acphost_checkpoint":`), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	ro, err := OpenLogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ro.History(math.MaxInt64, 0)
	_ = ro.Close()
	if err != nil || !page.TruncatedBefore || page.OldestSeq != 4 || len(page.Items) != 1 || page.Items[0].Kind != KindMessage {
		t.Fatalf("public snapshot: %+v, %v", page, err)
	}
	if !strings.HasSuffix(readFile(t, path), `"turn_end"`) {
		t.Fatal("read-only snapshot changed torn suffix")
	}
	if _, statErr := os.Stat(path + ".compact"); statErr != nil {
		t.Fatalf("read-only snapshot removed interrupted copy: %v", statErr)
	}
	l, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if _, statErr := os.Stat(path + ".compact"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("interrupted copy not reclaimed: %v", statErr)
	}
	last, _, active := l.state()
	if last.Seq != 4 || last.Epoch != 1 || last.Turn != 7 || !active || l.Mode() != "ask" {
		t.Fatalf("restored last=%+v active=%v mode=%q", last, active, l.Mode())
	}
	if _, _, itemErr := l.Item(3); !errors.Is(itemErr, ErrHistoryExpired) {
		t.Fatalf("expired item: %v", itemErr)
	}
	if _, found, itemErr := l.Item(50); itemErr != nil || found {
		t.Fatalf("never-recorded item: found=%v err=%v", found, itemErr)
	}
	replay, err := l.Replay(1)
	if err != nil || !replay.Reset || !replay.TruncatedBefore || replay.OldestSeq != 4 || replay.Seq != 4 || len(replay.Items) != 1 {
		t.Fatalf("expired replay: %+v, %v", replay, err)
	}
	replay, err = l.Replay(3)
	if err != nil || replay.Reset || len(replay.Items) != 1 {
		t.Fatalf("cursor immediately before retained window: %+v, %v", replay, err)
	}
	empty, err := l.History(3, 10)
	if err != nil || len(empty.Items) != 0 || !empty.TruncatedBefore || empty.OldestSeq != 4 {
		t.Fatalf("expired page: %+v, %v", empty, err)
	}
	it := Item{Kind: KindTurnEnd, Turn: 7}
	if appendErr := l.Append(&it); appendErr != nil || it.Seq != 5 || it.Epoch != 1 {
		t.Fatalf("continued item: %+v, %v", it, appendErr)
	}
	if _, _, stillActive := l.state(); stillActive {
		t.Fatal("turn remains open after end")
	}
	reset := Item{Kind: KindReset, Turn: 7}
	if appendErr := l.Append(&reset); appendErr != nil || reset.Seq != 6 || reset.Epoch != 2 {
		t.Fatalf("continued reset: %+v, %v", reset, appendErr)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("compacted permissions: %v, %v", info, err)
	}
}

func TestLogPinnedReadersSurviveAtomicCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	appendN(t, l, 20, KindUsage)
	ro, err := OpenLogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	// Pin the subscription boundary, then let a writer replace its inode before
	// decoding. This is the same interleaving as a slow replay or evidence read.
	boundary, pinned := l.beginReplay(0)
	written := make(chan error, 1)
	go func() {
		l.mu.Lock()
		writeErr := l.compact(l.size - l.offsets[15])
		l.mu.Unlock()
		if writeErr == nil {
			writeErr = l.Append(&Item{Kind: KindNotice, Notice: &Notice{Title: "latest"}})
		}
		written <- writeErr
	}()
	if writeErr := <-written; writeErr != nil {
		t.Fatal(writeErr)
	}
	old, err := pinned.items()
	if err != nil || len(old) != 20 || old[0].Seq != 1 || old[19].Seq != boundary.Seq {
		t.Fatalf("pinned reader: %v, %v", seqs(old), err)
	}
	page, err := ro.History(math.MaxInt64, 0)
	if err != nil || page.TruncatedBefore || page.OldestSeq != 1 || len(page.Items) != 20 {
		t.Fatalf("read-only inode snapshot: %+v, %v", page, err)
	}
	page, err = l.History(math.MaxInt64, 0)
	if err != nil || !page.TruncatedBefore || page.OldestSeq != 16 || len(page.Items) != 6 || page.Items[5].Seq != 21 {
		t.Fatalf("current snapshot: %+v, %v", page, err)
	}
	// No private checkpoint may leak through any public reader.
	for _, it := range page.Items {
		if it.Seq < page.OldestSeq || it.Kind == "" {
			t.Fatalf("private record exposed: %+v", it)
		}
	}
}

func TestEssentialHistoryRollsWithoutLosingPendingPermission(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	answered := make(chan string, 1)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		res, err := m.request(call.ctx, acp.ClientMethodSessionRequestPermission, m.permissionRequest())
		if err != nil {
			answered <- err.Error()
		} else {
			answered <- string(res)
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("clean"), false, nil); err != nil {
		t.Fatal(err)
	}
	pending := rec.waitInputs(t, 1)
	requestSeq := ofKind(items(t, s), KindRequest)[0].Seq
	payload := strings.Repeat("n", 64<<10)
	s.mu.Lock()
	for range MaxRunBytes/len(payload) + 32 {
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Title: "activity", Description: payload}})
	}
	s.mu.Unlock()
	if _, size, active := s.Log().state(); size > MaxRunBytes || !active {
		t.Fatalf("active retained state: size=%d active=%v", size, active)
	}
	if _, _, err := s.Log().Item(requestSeq); !errors.Is(err, ErrHistoryExpired) {
		t.Fatalf("old public permission item: %v", err)
	}
	if st := s.State(); !st.TurnInFlight || len(st.Pending) != 1 || st.Pending[0].ID != pending[0].ID {
		t.Fatalf("live permission was lost: %+v", st)
	}
	replay, live, cancel, err := s.Subscribe(requestSeq)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if !replay.Reset || !replay.TruncatedBefore || len(replay.Items) == 0 || replay.Items[0].Seq != replay.OldestSeq {
		t.Fatalf("expired subscriber: %+v", replay)
	}
	if err := s.Answer(pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	if got := <-answered; got != `{"outcome":{"optionId":"allow","outcome":"selected"}}` {
		t.Fatalf("agent answer: %s", got)
	}
	rec.waitInputs(t, 0)
	rec.waitIdle(t)
	it := <-live
	if it.Seq != replay.Seq+1 || it.Kind != KindRequest || it.Request.Status != RequestAnswered {
		t.Fatalf("latest delivery: %+v after %d", it, replay.Seq)
	}
}

func TestOversizedLegacyLogMigratesOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	// Old versions let essential notices grow without limit. Write that old
	// format directly, then open it through the normal migration path.
	payload := strings.Repeat("x", 64<<10)
	count := MaxRunBytes/len(payload) + 2
	for i := 1; i <= count; i++ {
		b, marshalErr := json.Marshal(Item{Seq: int64(i), Epoch: 3, Turn: 9, Kind: KindNotice, Notice: &Notice{Title: payload}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, writeErr := f.Write(append(b, '\n')); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	page, err := l.History(math.MaxInt64, 1)
	if err != nil || !page.TruncatedBefore || page.OldestSeq <= 1 || len(page.Items) != 1 || page.Items[0].Seq != int64(count) {
		t.Fatalf("migrated page: %+v, %v", page, err)
	}
	if _, size, _ := l.state(); size > MaxRunBytes {
		t.Fatalf("migrated size %d", size)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("legacy permissions: %v, %v", info, err)
	}
	it := Item{Kind: KindMessage, Turn: 9, Message: &Message{Text: "continued"}}
	if appendErr := l.Append(&it); appendErr != nil || it.Seq != int64(count+1) || it.Epoch != 3 {
		t.Fatalf("migrated append: %+v, %v", it, appendErr)
	}
}
