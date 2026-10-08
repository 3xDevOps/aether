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
	// This is the on-disk format written by the former compaction policy.
	// The lost prefix is not reconstructed, and the incomplete tail is repaired
	// only when the sole writer opens the log.
	const checkpoint = "{\"_acphost_checkpoint\":{\"last\":{\"seq\":3,\"epoch\":1,\"turn\":7,\"kind\":\"turn_start\"},\"open_turn\":true,\"mode\":\"ask\"}}\n"
	const suffix = "{\"seq\":4,\"epoch\":1,\"turn\":7,\"kind\":\"message\",\"message\":{\"text\":\"still working\"}}\n"
	if err := os.WriteFile(path, []byte(checkpoint+suffix+`{"seq":5,"kind":"turn_end"`), 0o600); err != nil {
		t.Fatal(err)
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
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
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
	if closeErr := l.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	l, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	last, _, active = l.state()
	if last.Seq != 6 || last.Epoch != 2 || last.Turn != 7 || active || l.Mode() != "ask" {
		t.Fatalf("reopened last=%+v active=%v mode=%q", last, active, l.Mode())
	}
	page, err = l.History(math.MaxInt64, 10)
	if err != nil || !page.TruncatedBefore || page.OldestSeq != 4 || len(page.Items) != 3 || page.Items[0].Seq != 4 || page.Items[2].Seq != 6 {
		t.Fatalf("continued history: %+v, %v", page, err)
	}
	if !strings.HasPrefix(readFile(t, path), checkpoint+suffix) {
		t.Fatal("opening and appending changed the legacy prefix or retained item")
	}
}

func TestLogCheckpointWithoutPublicItemsContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	const checkpoint = "{\"_acphost_checkpoint\":{\"last\":{\"seq\":40,\"epoch\":3,\"turn\":9,\"kind\":\"turn_start\"},\"open_turn\":true,\"mode\":\"ask\"}}\n"
	if err := os.WriteFile(path, []byte(checkpoint), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	last, _, active := l.state()
	if l.Len() != 0 || last.Seq != 40 || last.Epoch != 3 || last.Turn != 9 || !active || l.Mode() != "ask" {
		t.Fatalf("checkpoint-only state: last=%+v active=%v mode=%q len=%d", last, active, l.Mode(), l.Len())
	}
	replay, err := l.Replay(40)
	if err != nil || replay.Reset || !replay.TruncatedBefore || replay.Seq != 40 || replay.Epoch != 3 || len(replay.Items) != 0 {
		t.Fatalf("checkpoint-only replay: %+v, %v", replay, err)
	}
	it := Item{Kind: KindMessage, Turn: 9, Message: &Message{Text: "continued"}}
	if appendErr := l.Append(&it); appendErr != nil || it.Seq != 41 || it.Epoch != 3 {
		t.Fatalf("continued item: %+v, %v", it, appendErr)
	}
	if closeErr := l.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	l, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := l.History(math.MaxInt64, 10)
	if err != nil || !page.TruncatedBefore || page.OldestSeq != 41 || len(page.Items) != 1 || page.Items[0].Seq != 41 {
		t.Fatalf("continued page: %+v, %v", page, err)
	}
	last, _, active = l.state()
	if last.Seq != 41 || last.Epoch != 3 || last.Turn != 9 || !active || l.Mode() != "ask" {
		t.Fatalf("continued state: last=%+v active=%v mode=%q", last, active, l.Mode())
	}
	if _, _, itemErr := l.Item(40); !errors.Is(itemErr, ErrHistoryExpired) {
		t.Fatalf("missing prefix item: %v", itemErr)
	}
}

func TestLogSnapshotsSurviveAppendAndDelete(t *testing.T) {
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
	// Capture the subscription boundary before the next append. A slow replay
	// must see exactly that prefix even if the log is then explicitly deleted.
	boundary, first := l.beginReplay(0)
	_, second := l.beginReplay(10)
	if appendErr := l.Append(&Item{Kind: KindNotice, Notice: &Notice{Title: "latest"}}); appendErr != nil {
		t.Fatal(appendErr)
	}
	page, err := ro.History(math.MaxInt64, 0)
	if err != nil || page.TruncatedBefore || page.OldestSeq != 1 || len(page.Items) != 20 {
		t.Fatalf("read-only snapshot: %+v, %v", page, err)
	}
	page, err = l.History(math.MaxInt64, 0)
	if err != nil || page.TruncatedBefore || page.OldestSeq != 1 || len(page.Items) != 21 || page.Items[20].Seq != 21 {
		t.Fatalf("current snapshot: %+v, %v", page, err)
	}
	if deleteErr := l.Delete(); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("deleted history still exists: %v", statErr)
	}
	old, err := first.items()
	if err != nil || len(old) != 20 || old[0].Seq != 1 || old[19].Seq != boundary.Seq {
		t.Fatalf("first snapshot: %v, %v", seqs(old), err)
	}
	old, err = second.items()
	if err != nil || len(old) != 10 || old[0].Seq != 11 || old[9].Seq != boundary.Seq {
		t.Fatalf("second snapshot: %v, %v", seqs(old), err)
	}
	if _, readErr := l.ReadAfter(0, 1); !errors.Is(readErr, ErrLogClosed) {
		t.Fatalf("read after deletion: %v", readErr)
	}
	if deleteErr := l.Delete(); deleteErr != nil {
		t.Fatalf("repeated deletion: %v", deleteErr)
	}
}

func TestLargeHistoryKeepsPendingPermission(t *testing.T) {
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
	for range (64<<20)/len(payload) + 32 {
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Title: "activity", Description: payload}})
	}
	s.mu.Unlock()
	if _, size, active := s.Log().state(); size <= 64<<20 || !active {
		t.Fatalf("active history state: size=%d active=%v", size, active)
	}
	if it, found, err := s.Log().Item(requestSeq); err != nil || !found || it.Request == nil || it.Request.ID != pending[0].ID || it.Request.Status != RequestPending {
		t.Fatalf("original permission item: %+v, found=%v err=%v", it, found, err)
	}
	if st := s.State(); !st.TurnInFlight || len(st.Pending) != 1 || st.Pending[0].ID != pending[0].ID {
		t.Fatalf("live permission was lost: %+v", st)
	}
	replay, live, cancel, err := s.Subscribe(requestSeq)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if replay.Reset || replay.TruncatedBefore || len(replay.Items) != ReplayWindow || replay.Items[0].Seq != replay.OldestSeq {
		t.Fatalf("bounded subscriber: %+v", replay)
	}
	if answerErr := s.Answer(pending[0].ID, "allow", nil); answerErr != nil {
		t.Fatal(answerErr)
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

func TestOversizedExistingLogStaysIntactOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	// Write a complete pre-existing log directly, beyond the former 64 MiB
	// ceiling, so opening it cannot hide deletion behind append behavior.
	payload := strings.Repeat("x", 64<<10)
	count := (64<<20)/len(payload) + 2
	for i := range count {
		b, marshalErr := json.Marshal(Item{Seq: int64(i + 1), Epoch: 3, Turn: 9, Kind: KindNotice, Notice: &Notice{Title: payload}})
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
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	page, err := l.History(math.MaxInt64, 1)
	if err != nil || page.TruncatedBefore || page.OldestSeq != 1 || len(page.Items) != 1 || page.Items[0].Seq != int64(count) {
		t.Fatalf("existing page: %+v, %v", page, err)
	}
	if _, size, _ := l.state(); size <= 64<<20 || size != before.Size() {
		t.Fatalf("existing size %d, want %d above 64 MiB", size, before.Size())
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 || !os.SameFile(before, info) {
		t.Fatalf("existing file changed: %v, %v", info, err)
	}
	earliest, found, err := l.Item(1)
	if err != nil || !found || earliest.Notice == nil || earliest.Notice.Title != payload || earliest.Epoch != 3 || earliest.Turn != 9 {
		t.Fatalf("earliest item: seq=%d found=%v err=%v", earliest.Seq, found, err)
	}
	it := Item{Kind: KindMessage, Turn: 9, Message: &Message{Text: "continued"}}
	if appendErr := l.Append(&it); appendErr != nil || it.Seq != int64(count+1) || it.Epoch != 3 {
		t.Fatalf("continued append: %+v, %v", it, appendErr)
	}
	if closeErr := l.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	l, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	earliest, found, err = l.Item(1)
	if err != nil || !found || earliest.Notice == nil || earliest.Notice.Title != payload {
		t.Fatalf("earliest item after reopen: seq=%d found=%v err=%v", earliest.Seq, found, err)
	}
	latest, found, err := l.Item(it.Seq)
	if err != nil || !found || latest.Message == nil || latest.Message.Text != "continued" || latest.Epoch != 3 || latest.Turn != 9 {
		t.Fatalf("latest item after reopen: %+v found=%v err=%v", latest, found, err)
	}
}
