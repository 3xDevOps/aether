package ptyhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	recovered, err := readCastScreen(filepath.Join(dir, string(run)+".cast"))
	if err != nil {
		t.Fatalf("reconstruct current transcript: %v", err)
	}
	defer recovered.screen.dispose()
	if got := recovered.screen.term.String(); !strings.Contains(got, "first-lifesecond-life") {
		t.Fatalf("current transcript lost screen continuity across restart: %q", got)
	}
	asides, err := filepath.Glob(filepath.Join(dir, string(run)+".*.cast"))
	if err != nil || len(asides) != 1 {
		t.Fatalf("aside transcripts = %v (err %v), want exactly one", asides, err)
	}
	if got := replayOutput(asides[0]); got != "first-life" {
		t.Fatalf("preserved transcript = %q, want %q", got, "first-life")
	}

	replay, replayBytes, err := h.Replay(run)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatalf("read Replay: %v", err)
	}
	if err := replay.Close(); err != nil {
		t.Fatalf("close Replay: %v", err)
	}
	if replayBytes != len(got) {
		t.Fatalf("Replay byte count = %d, want %d", replayBytes, len(got))
	}
	if string(got) != "first-lifesecond-life" {
		t.Fatalf("full replay = %q, want both transcript incarnations", got)
	}
}

func TestLegacyRotatedCastsReplayAndRepairCheckpoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.cast")

	first, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	first.output([]byte("canonical-old\n"))
	if err = first.close(); err != nil {
		t.Fatal(err)
	}
	firstHeader, err := inspectCastHeader(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	second.output([]byte("legacy-middle\n"))
	if err = second.close(); err != nil {
		t.Fatal(err)
	}
	secondHeader, err := inspectCastHeader(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "run."+strconv.FormatInt(secondHeader.incarnation, 10)+".cast")
	if err = os.Rename(path, legacy); err != nil {
		t.Fatal(err)
	}
	latest, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	latest.output([]byte("current\n"))
	if err = latest.close(); err != nil {
		t.Fatal(err)
	}

	// A valid current cast belonging to run.123 is not run's archive: its
	// header incarnation does not match the ambiguous decimal suffix.
	dotted := filepath.Join(dir, "run.123.cast")
	sibling, err := newCastWriter(dotted, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	sibling.output([]byte("other-run\n"))
	if err = sibling.close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run.0.cast", "run.01.cast", "run.-1.cast", "run.no.cast"} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte("not an archive"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := priorCastPaths(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(dir, stableCastSegmentName(path, firstHeader.incarnation))
	wantPaths := []string{canonical, legacy}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("prior cast paths = %v, want %v", paths, wantPaths)
	}

	assertReplay := func() {
		t.Helper()
		replay, total, err := openFullCastReplay(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(replay)
		if err != nil {
			t.Fatal(err)
		}
		if err = replay.Close(); err != nil {
			t.Fatal(err)
		}
		want := "canonical-old\nlegacy-middle\ncurrent\n"
		if string(got) != want || total != len(want) {
			t.Fatalf("replay = %q (%d bytes), want %q (%d bytes)", got, total, want, len(want))
		}
	}
	assertReplay()
	if _, err := repairColdSnapshot(path); err != nil {
		t.Fatalf("repair legacy checkpoint: %v", err)
	}
	assertReplay()
}

func TestLegacyCastDiscoveryIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.cast")
	for i := 1; i <= maxLegacyCastHeaderInspections+1; i++ {
		name := "run." + strconv.Itoa(i) + ".cast"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("corrupt"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := priorCastPaths(path); err == nil || !strings.Contains(err.Error(), "discovery limit exceeded") {
		t.Fatalf("priorCastPaths error = %v, want legacy discovery limit", err)
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
	if cerr := w.close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	got, err := readCastTail(path, 6)
	if err != nil {
		t.Fatalf("readCastTail: %v", err)
	}
	if string(got) != "two\n" {
		t.Fatalf("readCastTail = %q, want %q", got, "two\n")
	}
}

func TestReadRecentCastUsesBoundedNewestSegments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.cast")
	first, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	first.output([]byte("first-life\n"))
	if err = first.close(); err != nil {
		t.Fatal(err)
	}
	second, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	second.output([]byte("second-life\n"))
	if err = second.close(); err != nil {
		t.Fatal(err)
	}

	got, err := readRecentCast(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first-life\nsecond-life\n" {
		t.Fatalf("recent replay = %q", got)
	}

	// A huge newest event cannot make the helper read past its fixed raw
	// window. Starting in the middle of that event returns no partial output.
	large := filepath.Join(t.TempDir(), "large.cast")
	w, err := newCastWriter(large, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	w.output(bytes.Repeat([]byte("x"), 1<<20))
	if err = w.close(); err != nil {
		t.Fatal(err)
	}
	window, used, err := readCastTailWindow(large, 16, 128)
	if err != nil {
		t.Fatal(err)
	}
	if used != 128 || len(window) != 0 {
		t.Fatalf("bounded window used=%d output=%d, want 128 and 0", used, len(window))
	}
}

func TestRestartReplaysCompleteTranscriptHistory(t *testing.T) {
	h, _ := newTestHost(t, func(cfg *Config) { cfg.ReplayBytes = 8 })
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
		return attach.out.String() == "first-life\nsecond-life\n"
	})
	attach.detach()
	if err := attach.wait(t); err != nil {
		t.Fatalf("detach replay client: %v", err)
	}
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("second StopSession: %v", err)
	}
}

// Recovery seeds the replay ring from only the transcript tail. Mode state
// must nevertheless come from the full prior transcript when the mode-setting
// sequence is older than that tail.
func TestRestartSeedsReplayModesFromTranscriptHistory(t *testing.T) {
	h, dir := newTestHost(t, func(c *Config) { c.ReplayBytes = 64 })
	run := domain.RunID("run-replay-modes")
	ctx := context.Background()

	first := newFakeAtt()
	if err := h.StartSession(ctx, RunSession(run), first); err != nil {
		t.Fatalf("first StartSession: %v", err)
	}
	first.writeOutput(t, "\x1b[?2004h")
	first.writeOutput(t, strings.Repeat("cursor redraw\r\n", 32))
	waitFor(t, "first output recorded", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("first StopSession: %v", err)
	}

	// A crash can interrupt the last JSON event without losing the PTY.
	path := filepath.Join(dir, string(run)+".cast")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`[1,"o","\u001b[?2004`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	for restart := range 2 {
		next := newFakeAtt()
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := h.StartSession(ctx, RunSession(run), next)
			if err == nil {
				break
			}
			if !errors.Is(err, ErrSnapshotPending) {
				t.Fatalf("recovery %d StartSession: %v", restart, err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("recovery %d checkpoint repair did not finish", restart)
			}
			time.Sleep(time.Millisecond)
		}
		s := h.lookup(RunSession(run))
		c := newClient(nil, AttachClient{Cols: 120, Rows: 30})
		if err := s.addClient(c); err != nil {
			t.Fatalf("addClient: %v", err)
		}
		replay, err := io.ReadAll(c.replay)
		if err != nil {
			t.Fatalf("read recovery %d replay: %v", restart, err)
		}
		if err := c.replay.Close(); err != nil {
			t.Fatalf("close recovery %d replay: %v", restart, err)
		}
		if !bytes.HasPrefix(replay, []byte("\x1b[?2004h")) {
			t.Fatalf("recovery %d replay lacks historical mode preamble: %q", restart, replay)
		}
		if err := h.StopSession(ctx, RunSession(run)); err != nil {
			t.Fatal(err)
		}
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

func TestLateMarkerAppendsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-late.cast")
	w, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	w.output([]byte("before\n"))
	w.marker("open marker")
	if err := w.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	w.lateMarker("inject by Ana: ship it")

	_, events := parseCast(t, path)
	var markers []string
	for _, ev := range events {
		if ev.code == "m" {
			markers = append(markers, ev.data)
		}
	}
	if len(markers) != 2 || markers[0] != "open marker" || markers[1] != "inject by Ana: ship it" {
		t.Fatalf("markers = %v, want the open marker and the late append", markers)
	}
}

func TestTranscriptLifecycleRegistryRetiresWithoutSplitLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.cast")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var active atomic.Int32
	var split atomic.Bool
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				lifecycle := acquireTranscriptLifecycle(path)
				lifecycle.entry.mu.Lock()
				if active.Add(1) != 1 {
					split.Store(true)
				}
				runtime.Gosched()
				active.Add(-1)
				lifecycle.entry.mu.Unlock()
				lifecycle.release()
			}
		}()
	}
	close(start)
	wg.Wait()
	if split.Load() {
		t.Fatal("same transcript path was protected by split lifecycle locks")
	}
	transcriptLifecycleRegistry.Lock()
	_, retained := transcriptLifecycleRegistry.entries[path]
	transcriptLifecycleRegistry.Unlock()
	if retained {
		t.Fatal("unused transcript lifecycle entry was not retired")
	}
}
