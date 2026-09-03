package ptyhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type castEvent struct {
	t    float64
	code string
	data string
}

func parseCast(t *testing.T, path string) (castHeader, []castEvent) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("empty transcript %s", path)
	}
	var header castHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("parse header %q: %v", lines[0], err)
	}
	var events []castEvent
	for _, ln := range lines[1:] {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(ln), &arr); err != nil {
			t.Fatalf("parse event %q: %v", ln, err)
		}
		if len(arr) != 3 {
			t.Fatalf("event %q has %d elements", ln, len(arr))
		}
		var ts float64
		if err := json.Unmarshal(arr[0], &ts); err != nil {
			t.Fatalf("event %q timestamp: %v", ln, err)
		}
		var code string
		if err := json.Unmarshal(arr[1], &code); err != nil {
			t.Fatalf("event %q code: %v", ln, err)
		}
		data, err := decodeCastString(arr[2])
		if err != nil {
			t.Fatalf("event %q data: %v", ln, err)
		}
		events = append(events, castEvent{t: ts, code: code, data: string(data)})
	}
	return header, events
}

func TestUTF8Boundary(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"ab\xc3", 2},
		{"ab\xc3\xa9", 4},
		{"\xe2\x96", 0},
		{"\xe2\x96\xb8", 3},
		{"a\xf0\x9f\x98", 1},
		{"a\xf0\x9f\x98\x80", 5},
		{"\x80\x80\x80\x80", 4}, // invalid: passes through whole
	}
	for _, c := range cases {
		if got := utf8Boundary([]byte(c.in)); got != c.want {
			t.Errorf("utf8Boundary(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCastWriterSplitRunes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.cast")
	w, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	w.output([]byte("h\xc3"))
	w.output([]byte("\xa9llo"))
	w.output([]byte("\xf0\x9f"))
	if err := w.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := w.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	_, events := parseCast(t, path)
	var out strings.Builder
	for _, ev := range events {
		if ev.code == "o" {
			out.WriteString(ev.data)
		}
	}
	// Runes split across writes are recorded whole; the dangling half-rune
	// flushed at close survives byte-for-byte via surrogate escapes.
	if out.String() != "héllo\xf0\x9f" {
		t.Fatalf("split rune corrupted output: %q", out.String())
	}
}

func TestTranscriptBinaryFidelity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.cast")
	w, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	live := "caf\xe9 \xfe\xff \x00\x1b[1m\"quoted\\\" é 😀\r\n\ttail\x80"
	w.output([]byte(live))
	if cerr := w.close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	for i, ln := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("transcript line %d is not valid JSON: %q", i, ln)
		}
	}
	_, events := parseCast(t, path)
	var out strings.Builder
	for _, ev := range events {
		if ev.code == "o" {
			out.WriteString(ev.data)
		}
	}
	if out.String() != live {
		t.Fatalf("replay = %q, want live bytes %q", out.String(), live)
	}
}

func TestCastStringRoundTrip(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("plain"),
		[]byte("caf\xe9 legacy latin-1"),
		{0x00, 0x1f, 0x7f, 0x80, 0xc0, 0xfe, 0xff},
		[]byte("mixed \xf0\x9f\x98\x80 emoji then \xf0\x9f broken"),
		[]byte("\\u0041 literal escape text \" and \\"),
	}
	for _, in := range cases {
		tok := appendCastString(nil, in)
		if !json.Valid(tok) {
			t.Errorf("appendCastString(%q) is not valid JSON: %s", in, tok)
			continue
		}
		out, err := decodeCastString(tok)
		if err != nil {
			t.Errorf("decodeCastString(%s): %v", tok, err)
			continue
		}
		if string(out) != string(in) {
			t.Errorf("round trip %q -> %s -> %q", in, tok, out)
		}
	}
}

func TestTranscriptPreservedAcrossRestart(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("run-restart")
	ctx := context.Background()

	att1 := newFakeAtt()
	if err := h.StartSession(ctx, RunSession(run), att1); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att1.writeOutput(t, "first-life")
	waitFor(t, "first output recorded", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// Reboot-recovery restart of the same run must not wipe the transcript.
	att2 := newFakeAtt()
	if err := h.StartSession(ctx, RunSession(run), att2); err != nil {
		t.Fatalf("restart StartSession: %v", err)
	}
	att2.writeOutput(t, "second-life")
	waitFor(t, "second output recorded", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("second StopSession: %v", err)
	}

	replayOutput := func(path string) string {
		_, events := parseCast(t, path)
		var out strings.Builder
		for _, ev := range events {
			if ev.code == "o" {
				out.WriteString(ev.data)
			}
		}
		return out.String()
	}
	if got := replayOutput(filepath.Join(dir, string(run)+".cast")); got != "second-life" {
		t.Fatalf("current transcript = %q, want %q", got, "second-life")
	}
	asides, err := filepath.Glob(filepath.Join(dir, string(run)+".*.cast"))
	if err != nil || len(asides) != 1 {
		t.Fatalf("aside transcripts = %v (err %v), want exactly one", asides, err)
	}
	if got := replayOutput(asides[0]); got != "first-life" {
		t.Fatalf("preserved transcript = %q, want %q", got, "first-life")
	}
}
func TestReadCastTailDecodesOutputAndIgnoresOtherEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.cast")
	w, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	w.output([]byte("one\n"))
	w.marker("not terminal output")
	w.output([]byte("two\n"))
	if err := w.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := readCastTail(path, 6)
	if err != nil {
		t.Fatalf("readCastTail: %v", err)
	}
	if string(got) != "two\n" {
		t.Fatalf("readCastTail = %q, want %q", got, "two\n")
	}
}

func TestRestartSeedsReplayFromTranscriptTail(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("run-replay-restart")
	ctx := context.Background()

	att1 := newFakeAtt()
	if err := h.StartSession(ctx, RunSession(run), att1); err != nil {
		t.Fatalf("first StartSession: %v", err)
	}
	att1.writeOutput(t, "first-life\n")
	waitFor(t, "first output recorded", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("first StopSession: %v", err)
	}

	att2 := newFakeAtt()
	if err := h.StartSession(ctx, RunSession(run), att2); err != nil {
		t.Fatalf("second StartSession: %v", err)
	}
	att2.writeOutput(t, "second-life\n")
	waitFor(t, "second output recorded", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	attach := startAttach(t, h, run, "member", 80, 24, false)
	waitFor(t, "replay output", func() bool {
		return strings.Contains(attach.out.String(), "first-life\nsecond-life\n")
	})
	attach.detach()
	if err := attach.wait(t); err != nil {
		t.Fatalf("detach replay client: %v", err)
	}
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("second StopSession: %v", err)
	}
}

func TestTranscriptWrittenIncrementally(t *testing.T) {
	h, dir := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-flush")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "live-data")
	path := filepath.Join(dir, string(run)+".cast")
	deadline := time.Now().Add(2*transcriptFlushInterval + time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), "live-data") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("transcript not flushed incrementally while session is live")
}
