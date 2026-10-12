package ptyhost

import (
	"io"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A tap replays what the current PTY session printed, never what an earlier
// process of the run printed before its session was replaced, as after a
// restart or a move to another account. A tapped session that ended, or one
// the scheduler stopped, is not read again.
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
	if _, err := h.TapOutput(run); err == nil {
		t.Fatal("a stopped session was tapped")
	}
	att := newFakeAtt()
	if err := h.StartSession(t.Context(), key, att); err != nil {
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
	_ = att.outW.Close()
	if _, err = io.ReadAll(tap); err != nil {
		t.Fatal(err)
	}
	_ = tap.Close()
	if _, err = h.TapOutput(run); err == nil {
		t.Fatal("a session its tap already read was read again")
	}
}

// A process that ended on its own before any tap attached, as a replacement
// agent that exits at once, still has its own output read, once.
func TestTapReadsAnUntappedProcessThatEnded(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("tap-ended")
	key := RunSession(run)
	if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("old process\r\n"))
	if err := h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	att := newFakeAtt()
	if err := h.StartSession(t.Context(), key, att); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("quick exit\r\n"))
	_ = att.outW.Close()
	<-h.lookup(key).done
	late, err := h.TapOutput(run)
	if err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(late)
	_ = late.Close()
	if err != nil || string(all) != "quick exit\r\n" {
		t.Fatalf("tap of the ended session read %q, %v; want only its own output", all, err)
	}
	if _, err = h.TapOutput(run); err == nil {
		t.Fatal("the ended session was read twice")
	}
}
