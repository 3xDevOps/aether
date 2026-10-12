package ptyhost

import (
	"io"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A tap replays what the current PTY session printed, never what an earlier
// process of the run printed before its session was replaced, as after a
// restart or a move to another account, also once that session has ended.
func TestTapReplaysOnlyTheCurrentSession(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("tap-session")
	key := RunSession(run)
	if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("old process\r\n"))
	if err := h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("new process\r\n"))
	tap, err := h.TapOutput(run)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("new process\r\n"))
	if _, err = io.ReadFull(tap, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "new process\r\n" {
		t.Fatalf("tap replayed %q, want only the current session's output", got)
	}
	_ = tap.Close()
	if err = h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}

	// A process that ended before anyone tapped it still has its output read.
	late, err := h.TapOutput(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = late.Close() }()
	all, err := io.ReadAll(late)
	if err != nil || string(all) != "new process\r\n" {
		t.Fatalf("tap of the ended session read %q, %v; want only its own output", all, err)
	}
}
