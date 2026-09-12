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
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func writeRecordingCast(t *testing.T, dir, name string, header castHeader, events string) {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(append(h, '\n'), []byte(events)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRecording(t *testing.T, h *Host, run domain.RunID) []byte {
	t.Helper()
	stream, err := h.Recording(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRecordingFlushesLiveCastWithoutTouchingPTYGeometry(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Config{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	run := domain.RunID("run-live")
	att := newFakeAtt()
	if err = h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.StopSession(context.Background(), RunSession(run)) }()
	att.writeOutput(t, "live-history")
	waitFor(t, "live output recording", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	before := att.sizeCalls()
	stream, err := h.Recording(run)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("live-history")) {
		t.Fatalf("live output missing from recording: %q", body)
	}
	after := att.sizeCalls()
	if len(after) != len(before) {
		t.Fatalf("recording changed live PTY geometry: before=%v after=%v", before, after)
	}
}

func recordingHeader(timestamp int64) castHeader {
	return castHeader{Version: 2, Width: 120, Height: 30, Timestamp: timestamp, Env: map[string]string{"TERM": "xterm"}}
}

func TestRecordingKeepsOutputOlderThanReplayRing(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Config{TranscriptDir: dir, ReplayBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("old-output-", 200000)
	writeRecordingCast(t, dir, "run-old.cast", recordingHeader(10), string(recordingEventLine(0, "o", old)))
	body := readRecording(t, h, "run-old")
	if !bytes.Contains(body, []byte(old[:64])) || !bytes.Contains(body, []byte(old[len(old)-64:])) {
		t.Fatalf("complete recording lost output outside replay ring: body=%d bytes", len(body))
	}
}

func TestRecordingMergesRotationsChronologicallyAndAlignsTimes(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Config{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	writeRecordingCast(t, dir, "run-multi.100.cast", recordingHeader(10), "[1.25,\"o\",\"first\"]\n[2,\"m\",\"mark\"]\n")
	writeRecordingCast(t, dir, "run-multi.200.cast", recordingHeader(20), "[0.5,\"r\",\"90x20\"]\n[1,\"o\",\"second\"]\n")
	writeRecordingCast(t, dir, "run-multi.cast", recordingHeader(30), "[0.25,\"o\",\"third\"]\n")
	body := readRecording(t, h, "run-multi")
	var got [][]any
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n")[1:] {
		var event []any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		got = append(got, event)
	}
	want := [][]any{
		{1.25, "o", "first"},
		{2.0, "m", "mark"},
		{10.0, "r", "120x30"},
		{10.5, "r", "90x20"},
		{11.0, "o", "second"},
		{20.0, "r", "120x30"},
		{20.25, "o", "third"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged recording events = %#v, want %#v", got, want)
	}
}

func TestRecordingIgnoresCrashTailAndIsFinite(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Config{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run-tail.cast")
	writeRecordingCast(t, dir, "run-tail.cast", recordingHeader(1), "[0,\"o\",\"ke\"]\n[0.1,\"o\",\"crash")
	stream, err := h.Recording("run-tail")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Appending after the bounded snapshot is opened must not make the
	// HTTP response follow the live file forever.
	_, _ = f.WriteString("[0.2,\"o\",\"late\"]\n")
	_ = f.Close()
	body, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("ke")) || bytes.Contains(body, []byte("crash")) || bytes.Contains(body, []byte("late")) {
		t.Fatalf("crash tail or post-snapshot write leaked: %q", body)
	}
	if _, err := h.Recording("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing recording error = %v, want os.ErrNotExist", err)
	}
}

func TestRecordingRejectsCorruptCompleteFinalEvent(t *testing.T) {
	h, dir := newTestHost(t)
	writeRecordingCast(t, dir, "run-corrupt.cast", recordingHeader(1), "[0,\"o\",\"valid\"]\n[\"invalid-time\",\"o\",\"corrupt\"]")
	stream, err := h.Recording("run-corrupt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := io.ReadAll(stream); err == nil {
		t.Fatal("complete corrupt final event reported successful history")
	}
}
