package ptyhost

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type historyAdmissionContext struct {
	context.Context
	entered chan<- struct{}
}

func (c *historyAdmissionContext) Done() <-chan struct{} {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func historyTestHost(t *testing.T, run domain.RunID, chunks ...string) (*Host, string) {
	t.Helper()
	h, err := New(Config{TranscriptDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	path := h.transcriptPath(RunSession(run))
	writer, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		writer.output([]byte(chunk))
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	return h, path
}

func historyTexts(lines []HistoryLine) []string {
	out := make([]string, len(lines))
	for i := range lines {
		out[i] = lines[i].Text
	}
	return out
}

func historyDeadlineTestContext(expireAfter int) context.Context {
	base := time.Unix(1000, 0)
	calls := 0
	clock := historyClock(func() time.Time {
		calls++
		if calls > expireAfter {
			return base.Add(time.Hour)
		}
		return base
	})
	return context.WithValue(context.Background(), historyClockContextKey{}, clock)
}

func historyHeaderWithIncarnation(t *testing.T, raw []byte, incarnation int64) []byte {
	t.Helper()
	var header castHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	header.Incarnation = incarnation
	encoded, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func TestHistoryNewestAndOlderPaging(t *testing.T) {
	run := domain.RunID("history-page")
	h, _ := historyTestHost(t, run, "one\ntwo\nthree\nfour\nfive\n")

	newest, err := h.History(context.Background(), run, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(newest.Lines), ","); got != "four,five" {
		t.Fatalf("newest lines = %q", got)
	}
	if !newest.HasMore || newest.NextCursor == "" {
		t.Fatalf("newest pagination = %+v", newest)
	}

	older, err := h.History(context.Background(), run, newest.NextCursor, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(older.Lines), ","); got != "two,three" {
		t.Fatalf("older lines = %q", got)
	}
	oldest, err := h.History(context.Background(), run, older.NextCursor, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(oldest.Lines), ","); got != "one" || oldest.HasMore {
		t.Fatalf("oldest page = %+v (%q)", oldest, got)
	}
}

func TestHistorySearchAcrossOutputChunksAndStripsControls(t *testing.T) {
	run := domain.RunID("history-search")
	h, _ := historyTestHost(t, run,
		"noise\n\x1b[31mNeed", "LE\x1b[0m in a Hay", "stack\nother\nneedle TWO\n")

	page, err := h.History(context.Background(), run, "", "nEeDlE", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "NeedLE in a Haystack,needle TWO" {
		t.Fatalf("search lines = %q", got)
	}
	for _, line := range page.Lines {
		if strings.ContainsRune(line.Text, '\x1b') {
			t.Fatalf("raw escape returned in %q", line.Text)
		}
	}
}

func TestHistorySearchUsesUnicodeCaseFoldingAndReprocessesControls(t *testing.T) {
	run := domain.RunID("history-unicode-fold")
	invalid := string([]byte{'b', 0xe2, '\n', 'c', 0xe2, 0x1b, '[', '3', '1', 'm', 'X', 0x1b, '[', '0', 'm', '\n'})
	h, _ := historyTestHost(t, run, "Kelvin: K\nfinal sigma: ς\ndecomposed: Cafe\u0301\n", invalid)

	page, err := h.History(context.Background(), run, "", "kELVIN: k", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "Kelvin: K" {
		t.Fatalf("Kelvin-fold search = %q", got)
	}
	page, err = h.History(context.Background(), run, "", "Σ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "final sigma: ς" {
		t.Fatalf("sigma-fold search = %q", got)
	}
	page, err = h.History(context.Background(), run, "", "CAFÉ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "decomposed: Cafe\u0301" {
		t.Fatalf("canonical-equivalent search = %q", got)
	}
	all, err := h.History(context.Background(), run, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(historyTexts(all.Lines), ",")
	if !strings.Contains(got, "b�,c�X") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("invalid UTF-8/control normalization = %q", got)
	}
}

func TestHistorySearchUsesBoundedResumableWindows(t *testing.T) {
	run := domain.RunID("history-search-windows")
	chunks := make([]string, 0, 900)
	chunks = append(chunks, "oldest needle\n")
	for i := 0; i < 800; i++ {
		chunks = append(chunks, strings.Repeat("x", 4096)+"\n")
	}
	h, _ := historyTestHost(t, run, chunks...)

	before := ""
	seen := map[string]bool{}
	pages := 0
	found := false
	for ; pages < 64; pages++ {
		page, err := h.History(context.Background(), run, before, "needle", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) > 0 {
			if got := strings.Join(historyTexts(page.Lines), ","); got != "oldest needle" {
				t.Fatalf("search lines = %q", got)
			}
			found = true
			break
		}
		if !page.HasMore || page.NextCursor == "" {
			t.Fatalf("empty bounded window falsely exhausted: %+v", page)
		}
		if seen[page.NextCursor] || page.NextCursor == before {
			t.Fatalf("search cursor did not advance: %q", page.NextCursor)
		}
		seen[page.NextCursor] = true
		before = page.NextCursor
	}
	if !found || pages == 0 {
		t.Fatalf("bounded search found=%v after %d pages", found, pages+1)
	}
}

func TestHistorySearchLimitDoesNotSkipMatchesWithinWindow(t *testing.T) {
	run := domain.RunID("history-search-limit")
	h, _ := historyTestHost(t, run, "needle one\nneedle two\nneedle three\nneedle four\n")

	newest, err := h.History(context.Background(), run, "", "needle", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(newest.Lines), ","); got != "needle three,needle four" || !newest.HasMore {
		t.Fatalf("newest search page = %+v (%q)", newest, got)
	}
	older, err := h.History(context.Background(), run, newest.NextCursor, "needle", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(older.Lines), ","); got != "needle one,needle two" {
		t.Fatalf("older search page = %+v (%q)", older, got)
	}
}

func TestHistoryRejectsMalformedAndCrossRunCursor(t *testing.T) {
	run := domain.RunID("history-cursor")
	h, _ := historyTestHost(t, run, "one\ntwo\n")
	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.History(context.Background(), run, "not-a-cursor", "", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("malformed cursor error = %v", err)
	}
	if _, err = h.History(context.Background(), run, strings.Repeat("A", 2<<20), "", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("oversized cursor error = %v", err)
	}
	other := domain.RunID("other-run")
	writer, err := newCastWriter(h.transcriptPath(RunSession(other)), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	writer.output([]byte("other history\n"))
	if err = writer.close(); err != nil {
		t.Fatal(err)
	}
	if _, err = h.History(context.Background(), other, page.NextCursor, "", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("cross-run cursor error = %v", err)
	}
}

func TestHistoryCursorStableAcrossActiveAppend(t *testing.T) {
	run := domain.RunID("history-append")
	h, path := historyTestHost(t, run, "one\ntwo\nthree\n")
	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(castLine(castTimeForTest(t, path), "o", []byte("four\n"))); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	older, err := h.History(context.Background(), run, page.NextCursor, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(older.Lines), ","); got != "one,two" {
		t.Fatalf("older lines after append = %q", got)
	}
}

func TestHistoryCursorSurvivesNewerSegmentAndRejectsReplacement(t *testing.T) {
	run := domain.RunID("history-segment-cursor")
	h, path := historyTestHost(t, run, "one\ntwo\nthree\n")
	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}

	writer, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	writer.output([]byte("four\nfive\n"))
	if err = writer.close(); err != nil {
		t.Fatal(err)
	}
	older, err := h.History(context.Background(), run, page.NextCursor, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(older.Lines), ","); got != "one,two" {
		t.Fatalf("older lines after rotation = %q", got)
	}

	priors, err := priorCastPaths(path)
	if err != nil || len(priors) != 1 {
		t.Fatalf("prior segments = %v, %v", priors, err)
	}
	replacement, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priors[0], replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.History(context.Background(), run, page.NextCursor, "", 2); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("replaced segment cursor error = %v", err)
	}
}

func TestHistoryNewestDoesNotInspectOlderSegments(t *testing.T) {
	run := domain.RunID("history-lazy-newest")
	h, path := historyTestHost(t, run, "one\ntwo\nthree\n")
	malformed := filepath.Join(filepath.Dir(path), stableCastSegmentName(path, 1))
	if err := os.WriteFile(malformed, []byte("not a cast\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "three" {
		t.Fatalf("newest lines = %q", got)
	}
}

func castTimeForTest(t *testing.T, path string) (start time.Time) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	header, err := readCastHeader(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return time.Unix(header.Timestamp, 0)
}

func TestHistoryBoundsHugeLineAndSkipsHugeRawEvent(t *testing.T) {
	run := domain.RunID("history-bounds")
	h, path := historyTestHost(t, run, "kept\n", strings.Repeat("x", maxHistoryLineBytes*4)+"\n")
	page, err := h.History(context.Background(), run, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) == 0 || len(page.Lines[len(page.Lines)-1].Text) > maxHistoryLineBytes {
		t.Fatalf("line bound not enforced: %+v", page.Lines)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(castLine(castTimeForTest(t, path), "o", []byte(strings.Repeat("z", maxHistoryEventBytes+1)))); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	page, err = h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 1 || len(page.Lines[0].Text) > maxHistoryLineBytes {
		t.Fatalf("page after huge event = %+v", page)
	}
}

func TestHistoryOversizedEventReturnsResumableCursor(t *testing.T) {
	run := domain.RunID("history-oversized-resume")
	h, path := historyTestHost(t, run, "kept\n")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	huge := []byte(strings.Repeat("z", maxHistoryPageRawRead+2*maxHistoryEventBytes))
	if _, err = f.Write(castLine(castTimeForTest(t, path), "o", huge)); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 0 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first oversized page = %+v", page)
	}
	page, err = h.History(context.Background(), run, page.NextCursor, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "kept" {
		t.Fatalf("resumed oversized page = %+v (%q)", page, got)
	}
}

func TestHistoryKeepsBoundedSuffixOfNewlineFreeWindow(t *testing.T) {
	run := domain.RunID("history-newline-free")
	chunks := make([]string, 2200)
	for i := range chunks {
		chunks[i] = strings.Repeat("x", 4096)
	}
	chunks[len(chunks)-1] = strings.Repeat("z", 4096)
	h, _ := historyTestHost(t, run, chunks...)

	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 1 || page.Lines[0].Text == "" || len(page.Lines[0].Text) > maxHistoryLineBytes {
		t.Fatalf("newline-free page = %+v", page)
	}
	if !strings.HasSuffix(page.Lines[0].Text, chunks[len(chunks)-1]) {
		t.Fatalf("newline-free line is not the newest suffix: %q", page.Lines[0].Text)
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("newline-free pagination = %+v", page)
	}
	older, err := h.History(context.Background(), run, page.NextCursor, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Lines) != 1 || older.Lines[0].Text == "" || len(older.Lines[0].Text) > maxHistoryLineBytes {
		t.Fatalf("resumed newline-free page = %+v", older)
	}
	if older.NextCursor == page.NextCursor {
		t.Fatalf("newline-free cursor did not advance: %+v", older)
	}
}

func TestHistoryMissingTranscriptAndCancellation(t *testing.T) {
	h, err := New(Config{TranscriptDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.History(context.Background(), domain.RunID("missing"), "", "", 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing transcript error = %v", err)
	}
	run := domain.RunID("history-cancel")
	h, _ = historyTestHost(t, run, "line\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.History(ctx, run, "", "line", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search error = %v", err)
	}
}

func TestHistoryEndedSessionHasNoWriterToFlush(t *testing.T) {
	run := domain.RunID("history-ended-writer")
	h, _ := historyTestHost(t, run, "complete\n")
	h.sessions[RunSession(run)] = &session{}

	page, err := h.History(context.Background(), run, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "complete" {
		t.Fatalf("ended history = %q", got)
	}
}

func TestHistoryNormalizesTerminalLineEditingAndControlForms(t *testing.T) {
	run := domain.RunID("history-terminal-editing")
	h, _ := historyTestHost(t, run,
		"long status\r\x1b[2Kok\n",
		"ab\x1b(Bcd\n",
		"red \u009b31mgreen\u009b0m done\n",
		"a\u009dhidden\u009cb\n",
		"abcdef\r\x1b[3C!\x1b[2D?\x1b[1GZ\n",
	)

	page, err := h.History(context.Background(), run, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "ok,abcd,red green done,ab,Zb?!ef" {
		t.Fatalf("normalized lines = %q", got)
	}
}

func TestHistorySearchCompletesLineAcrossRawWindowAndFullFolds(t *testing.T) {
	run := domain.RunID("history-window-fold")
	controls := strings.Repeat("\x1b[0m", 40000)
	h, _ := historyTestHost(t, run, "Stra", controls, controls, controls, controls, "ße\n")

	page, err := h.History(context.Background(), run, "", "STRASSE", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(page.Lines), ","); got != "Straße" {
		t.Fatalf("window-spanning full-fold search = %q", got)
	}
}

func TestHistoryCursorResolvesDottedRunSegmentDirectly(t *testing.T) {
	run := domain.RunID("history.dotted-run")
	h, path := historyTestHost(t, run, "one\ntwo\nthree\n")
	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	writer.output([]byte("new\n"))
	if err = writer.close(); err != nil {
		t.Fatal(err)
	}
	priors, err := priorCastPaths(path)
	if err != nil || len(priors) != 1 {
		t.Fatalf("prior segments = %v, %v", priors, err)
	}
	if name := filepath.Base(priors[0]); !historySegmentName(path, name) {
		t.Fatalf("stable segment name %q was not recognized", name)
	}

	stem := strings.TrimSuffix(path, ".cast")
	for i := 0; i < 32; i++ {
		name := stem + ".9" + strconv.Itoa(i) + ".cast"
		if historySegmentName(path, filepath.Base(name)) {
			t.Fatalf("dotted sibling %q was mistaken for a history segment", name)
		}
		if err = os.WriteFile(name, []byte("malformed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{"01", "+1", "0", "-1", "not-a-number"} {
		name := stem + ".~" + suffix + ".cast"
		if historySegmentName(path, filepath.Base(name)) {
			t.Fatalf("noncanonical segment %q was accepted", name)
		}
		if err = os.WriteFile(name, []byte("malformed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	older, err := h.History(context.Background(), run, page.NextCursor, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(older.Lines), ","); got != "one,two" {
		t.Fatalf("direct dotted cursor history = %q", got)
	}
}

func TestHistorySegmentDiscoveryIsBoundedAndResumable(t *testing.T) {
	run := domain.RunID("history-segment-bound")
	h, path := historyTestHost(t, run)
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxHistorySegmentsPerPage+2; i++ {
		incarnation := int64(100000 + i)
		name := filepath.Join(filepath.Dir(path), stableCastSegmentName(path, incarnation))
		if err = os.WriteFile(name, historyHeaderWithIncarnation(t, header, incarnation), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	page, err := h.History(context.Background(), run, "", "absent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 0 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("bounded segment page = %+v", page)
	}
}

func TestHistoryRejectsForgedCursorBeforeFilesystemLookup(t *testing.T) {
	run := domain.RunID("history-forged-cursor")
	h, _ := historyTestHost(t, run, "one\ntwo\n")
	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	forged := page.NextCursor[:len(page.NextCursor)-1] + "A"
	if forged == page.NextCursor {
		forged = page.NextCursor[:len(page.NextCursor)-1] + "B"
	}
	oldDir := h.cfg.TranscriptDir
	if err := os.Rename(oldDir, oldDir+"-gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.History(context.Background(), run, forged, "", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("forged cursor error = %v, want invalid cursor before filesystem lookup", err)
	}
}

func TestHistoryTinyEventsAreBoundedAndResumeWithoutGaps(t *testing.T) {
	run := domain.RunID("history-tiny-events")
	chunks := make([]string, 0, maxHistoryEvents*2+32)
	want := make(map[string]bool)
	for i := 0; i < maxHistoryEvents*2+17; i++ {
		if i%1000 == 0 {
			line := "needle-" + strconv.Itoa(i)
			chunks = append(chunks, line+"\n")
			want[line] = true
		} else {
			chunks = append(chunks, "x\n")
		}
	}
	h, _ := historyTestHost(t, run, chunks...)

	before := ""
	got := make(map[string]bool)
	for pages := 0; pages < 8; pages++ {
		page, err := h.History(context.Background(), run, before, "needle-", 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range page.Lines {
			if got[line.Text] {
				t.Fatalf("duplicate resumed line %q", line.Text)
			}
			got[line.Text] = true
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == before {
			t.Fatalf("tiny-event cursor did not advance: %+v", page)
		}
		before = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("resumed matches = %v, want %v", got, want)
	}
	for line := range want {
		if !got[line] {
			t.Fatalf("resumed search skipped %q", line)
		}
	}
}

func TestHistoryHugeCSINumericParameterCannotEscapeColumnBounds(t *testing.T) {
	run := domain.RunID("history-huge-csi-c")
	h, _ := historyTestHost(t, run, "\x1b["+strings.Repeat("9", 64)+"C\t!\n")

	page, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 1 || len(page.Lines[0].Text) > maxHistoryLineBytes {
		t.Fatalf("huge CSI C page = %+v", page)
	}
}

func TestHistoryAdmissionWaitDoesNotBlockTranscriptRotation(t *testing.T) {
	run := domain.RunID("history-admission-rotation")
	h, path := historyTestHost(t, run, "one\n")
	oldSlots := historyReadSlots
	historyReadSlots = make(chan struct{}, 1)
	historyReadSlots <- struct{}{}
	defer func() { historyReadSlots = oldSlots }()

	base, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 1)
	ctx := &historyAdmissionContext{Context: base, entered: entered}
	historyDone := make(chan error, 1)
	go func() {
		_, err := h.History(ctx, run, "", "", 1)
		historyDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		<-historyReadSlots
		<-historyDone
		t.Fatal("history request did not queue for admission")
	}
	rotationDone := make(chan error, 1)
	go func() {
		writer, err := newCastWriter(path, 80, 24)
		if err == nil {
			err = writer.close()
		}
		rotationDone <- err
	}()
	rotationBlocked := false
	var rotationErr error
	select {
	case rotationErr = <-rotationDone:
	case <-time.After(time.Second):
		rotationBlocked = true
	}

	cancel()
	<-historyReadSlots
	historyErr := <-historyDone
	if rotationBlocked {
		<-rotationDone
		t.Fatal("transcript rotation blocked behind queued history admission")
	}
	if rotationErr != nil {
		t.Fatal(rotationErr)
	}
	if historyErr != nil && !errors.Is(historyErr, context.Canceled) {
		t.Fatalf("queued history error = %v", historyErr)
	}
}

func TestHistorySegmentDiscoveryIsSnapshottedOncePerRequest(t *testing.T) {
	run := domain.RunID("history-discovery-pass")
	_, path := historyTestHost(t, run)
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, incarnation := range []int64{100, 200, 300} {
		name := filepath.Join(filepath.Dir(path), stableCastSegmentName(path, incarnation))
		if err = os.WriteFile(name, historyHeaderWithIncarnation(t, header, incarnation), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	discovery := historySegmentDiscovery{}
	work := newHistoryWorkDeadline(context.Background())
	segment, ok, err := discovery.nextSegment(context.Background(), work, path, path)
	if err != nil || !ok || filepath.Base(segment.path) != stableCastSegmentName(path, 300) {
		t.Fatalf("first discovered segment = %q, %v, %v", segment.path, ok, err)
	}
	name := filepath.Join(filepath.Dir(path), stableCastSegmentName(path, 250))
	if err = os.WriteFile(name, historyHeaderWithIncarnation(t, header, 250), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, incarnation := range []int64{200, 100} {
		segment, ok, err = discovery.nextSegment(context.Background(), work, path, segment.path)
		if err != nil || !ok || filepath.Base(segment.path) != stableCastSegmentName(path, incarnation) {
			t.Fatalf("next discovered segment = %q, %v, %v", segment.path, ok, err)
		}
	}
	if segment, ok, err = discovery.nextSegment(context.Background(), work, path, segment.path); err != nil || ok {
		t.Fatalf("discovery unexpectedly rescanned and found %q: %v, %v", segment.path, ok, err)
	}
}

func TestHistoryCursorUsesFullQueryDigestAndRejectsOldVersion(t *testing.T) {
	run := domain.RunID("history-query-cursor")
	h, path := historyTestHost(t, run, "needle one\nneedle two\n")
	page, err := h.History(context.Background(), run, "", "needle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" {
		t.Fatalf("missing search cursor: %+v", page)
	}
	if _, err = h.History(context.Background(), run, page.NextCursor, "different", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("cross-query cursor error = %v", err)
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	payload := raw[:len(raw)-sha256.Size]
	var cursor historyCursor
	if err = json.Unmarshal(payload, &cursor); err != nil {
		t.Fatal(err)
	}
	if len(cursor.Query) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		t.Fatalf("query digest length = %d", len(cursor.Query))
	}
	originalSegmentID := cursor.SegmentID
	cursor.Current = false
	cursor.SegmentID = stableHistorySegmentID(path, cursor.Incarnation+1)
	payload, err = json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, h.historyCursorKey[:])
	_, _ = mac.Write(payload)
	mismatchedCursor := base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
	if _, err = authenticateHistoryCursor(h.historyCursorKey[:], mismatchedCursor, run, "needle", path); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("mismatched archived segment identity error = %v", err)
	}
	cursor.Current = true
	cursor.SegmentID = originalSegmentID
	cursor.Version = 4
	payload, err = json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	mac = hmac.New(sha256.New, h.historyCursorKey[:])
	_, _ = mac.Write(payload)
	oldCursor := base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
	if _, err = h.History(context.Background(), run, oldCursor, "needle", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("old-version cursor error = %v", err)
	}
}

func TestHistorySegmentDiscoveryHasExplicitDirectoryBudget(t *testing.T) {
	run := domain.RunID("history-discovery-budget")
	h, path := historyTestHost(t, run)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	for i := 0; i <= maxHistoryDirectoryEntries; i++ {
		name := filepath.Join(dir, "unrelated-"+strconv.Itoa(i))
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.History(context.Background(), run, "", "", 1); !errors.Is(err, ErrHistoryDiscoveryLimit) {
		t.Fatalf("over-budget discovery error = %v", err)
	}
}

func TestHistoryReverseReaderStopsAfterOneBoundedReadPastDeadline(t *testing.T) {
	run := domain.RunID("history-reader-deadline")
	_, path := historyTestHost(t, run, strings.Repeat("x", historyReverseBlock*2))
	segment := historySegment{path: path, current: true}
	if err := loadHistorySegment(&segment); err != nil {
		t.Fatal(err)
	}

	ctx := historyDeadlineTestContext(3)
	work := newHistoryWorkDeadline(ctx)
	budget := historyReverseBlock * 2
	reader := historyReverseReader{ctx: ctx, work: work, budget: &budget, maxEvents: maxHistoryEvents}
	defer reader.close()
	position := segment.fileBytes
	start := position
	_, ok, stopped, err := reader.previous(segment, 0, &position)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !stopped {
		t.Fatalf("deadline read returned ok=%v stopped=%v", ok, stopped)
	}
	if position != start {
		t.Fatalf("deadline read advanced position from %d to %d", start, position)
	}
	if used := historyReverseBlock*2 - budget; used != historyReverseBlock {
		t.Fatalf("deadline read bytes = %d, want one %d-byte block", used, historyReverseBlock)
	}
}

func TestHistoryElapsedBudgetReturnsAuthenticatedResumableCursor(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "page", want: "alpha,beta needle"},
		{name: "search", query: "needle", want: "beta needle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := domain.RunID("history-deadline-" + tt.name)
			h, _ := historyTestHost(t, run, "alpha\nbeta needle\n")
			page, err := h.History(historyDeadlineTestContext(1), run, "", tt.query, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Lines) != 0 || !page.HasMore || page.NextCursor == "" {
				t.Fatalf("deadline page = %+v", page)
			}

			resumed, err := h.History(context.Background(), run, page.NextCursor, tt.query, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(historyTexts(resumed.Lines), ","); got != tt.want {
				t.Fatalf("resumed lines = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHistoryDecoderStopsAtBoundedDeadlineInterval(t *testing.T) {
	ctx := historyDeadlineTestContext(2)
	work := newHistoryWorkDeadline(ctx)
	decoder := newHistoryDecoder(ctx, work, func(historyDecodedLine) {})
	err := decoder.feed(historyEvent{data: []byte(strings.Repeat("x", 8192))})
	if !errors.Is(err, errHistoryWorkDeadline) {
		t.Fatalf("decoder deadline error = %v", err)
	}
	if decoder.column != 4096 {
		t.Fatalf("decoder processed %d bytes past check interval", decoder.column)
	}
}

func TestHistoryCancellationWinsOverElapsedWorkBudget(t *testing.T) {
	run := domain.RunID("history-deadline-cancel")
	h, _ := historyTestHost(t, run, "line\n")
	ctx, cancel := context.WithCancel(historyDeadlineTestContext(1))
	cancel()
	if _, err := h.History(ctx, run, "", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled deadline request error = %v", err)
	}
}

func TestHistoryPagesLegacyDecimalArchives(t *testing.T) {
	run := domain.RunID("history-legacy-archive")
	h, path := historyTestHost(t, run, "legacy-old\n")
	header, err := inspectCastHeader(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), ".cast")+"."+strconv.FormatInt(header.incarnation, 10)+".cast")
	if err = os.Rename(path, legacy); err != nil {
		t.Fatal(err)
	}
	// A dotted sibling and an unreadable decimal name must not block or leak.
	sibling := strings.TrimSuffix(path, ".cast") + ".9.cast"
	siblingWriter, err := newCastWriter(sibling, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	siblingWriter.output([]byte("other-run\n"))
	if err = siblingWriter.close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(strings.TrimSuffix(path, ".cast")+".91.cast", []byte("malformed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writer, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	writer.output([]byte("legacy-new\n"))
	if err = writer.close(); err != nil {
		t.Fatal(err)
	}

	newest, err := h.History(context.Background(), run, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(newest.Lines), ","); got != "legacy-new" || !newest.HasMore {
		t.Fatalf("newest legacy page = %+v (%q)", newest, got)
	}
	older, err := h.History(context.Background(), run, newest.NextCursor, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(older.Lines), ","); got != "legacy-old" {
		t.Fatalf("legacy archive page = %+v (%q)", older, got)
	}
	found, err := h.History(context.Background(), run, "", "legacy-old", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyTexts(found.Lines), ","); got != "legacy-old" {
		t.Fatalf("legacy archive search = %+v (%q)", found, got)
	}
}
