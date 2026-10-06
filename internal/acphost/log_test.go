package acphost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendN(t *testing.T, l *Log, n int, kind Kind) {
	t.Helper()
	for range n {
		if err := l.Append(&Item{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
}

func seqs(its []Item) []int64 {
	out := make([]int64, len(its))
	for i, it := range its {
		out[i] = it.Seq
	}
	return out
}

func TestLogReadsAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, l, 5, KindUsage)
	appendN(t, l, 1, KindReset)
	appendN(t, l, 1, KindUsage)
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}

	// A crash mid-write leaves a torn last line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":8,"kind":"mess`)
	_ = f.Close()

	l, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if l.Len() != 7 || l.LastSeq() != 7 {
		t.Fatalf("len %d last %d", l.Len(), l.LastSeq())
	}
	appendN(t, l, 1, KindUsage)
	after, err := l.ReadAfter(5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(after); len(got) != 2 || got[0] != 6 || got[1] != 7 || after[0].Epoch != 1 || after[1].Epoch != 1 {
		t.Fatalf("ReadAfter: %+v", after)
	}
	before, err := l.ReadBefore(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(before); len(got) != 2 || got[0] != 2 || got[1] != 3 || before[0].Epoch != 0 {
		t.Fatalf("ReadBefore: %+v", before)
	}
	all, err := l.ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 8 || all[7].Seq != 8 || all[7].Time.IsZero() {
		t.Fatalf("all: %v", seqs(all))
	}
	if err := l.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("log not deleted: %v", err)
	}
	if err := l.Append(&Item{Kind: KindUsage}); err != ErrLogClosed {
		t.Fatalf("append after delete: %v", err)
	}
	if _, err := l.ReadAfter(0, 0); err != ErrLogClosed {
		t.Fatalf("read after delete: %v", err)
	}
}

func TestLogRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	if err := os.WriteFile(path, []byte("{\"seq\":1}\nnot json\n{\"seq\":3}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(path); err == nil || !strings.Contains(err.Error(), "byte 10") {
		t.Fatalf("OpenLog: %v", err)
	}
}

func TestItemCap(t *testing.T) {
	big := strings.Repeat("x", MaxItemBytes)
	it := Item{Kind: KindToolCall, ToolCall: &ToolCall{ID: "t1", Title: "cat", Output: big, Content: []Content{{Type: "text", Text: big}}}}
	b, err := it.encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxItemBytes || !it.Truncated || it.ToolCall.ID != "t1" {
		t.Fatalf("encoded %d bytes, truncated %v", len(b), it.Truncated)
	}

	log, err := OpenLog(filepath.Join(t.TempDir(), "run.items.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	s := &Session{subs: map[chan Item]struct{}{}, logger: discard, log: log}
	s.proj = newProjector(s.emitLocked, func() {}, func(string, string) {})
	// One 300 KiB chunk is split into segments, not cut.
	s.proj.chunk(KindMessage, "assistant", "m1", textPrompt(strings.Repeat("y", 300<<10))[0])
	s.proj.closeText()
	its, _ := s.log.ReadAfter(0, 0)
	if text := messageText(its, "assistant"); len(text) != 300<<10 {
		t.Fatalf("split message lost text: %d bytes in %d items", len(text), len(its))
	}
	for _, it := range its {
		if it.Truncated {
			t.Fatal("a split segment was truncated")
		}
	}

	segment := strings.Repeat("z", maxTextSegment)
	for {
		if _, size, _ := s.log.state(); size >= MaxRunBytes {
			break
		}
		s.emitLocked(Item{Kind: KindMessage, Message: &Message{Role: "assistant", MessageID: "m2", Text: segment}})
	}
	last := s.log.LastSeq()
	s.emitLocked(Item{Kind: KindMessage, Message: &Message{Role: "assistant", MessageID: "m3", Text: "dropped"}})
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: "end_turn"})
	s.emitLocked(Item{Kind: KindUsage, Usage: &Usage{Used: 1}})
	tail, err := s.log.ReadAfter(last, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[0].Kind != KindNotice || tail[1].Kind != KindTurnEnd {
		t.Fatalf("after the cap: %+v", tail)
	}
}
