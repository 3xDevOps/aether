package ptyhost

import (
	"context"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestOnInputReportsFirstKeystrokeOnce pins the hook the server turns into
// a co-author record: it fires once per write attach, after the keystrokes
// reach the PTY, and never for a mirror.
func TestOnInputReportsFirstKeystrokeOnce(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []domain.MemberID
	)
	h, _ := newTestHost(t, func(cfg *Config) {
		cfg.OnInput = func(_ SessionKey, member domain.MemberID) {
			mu.Lock()
			seen = append(seen, member)
			mu.Unlock()
		}
	})
	report := func() []domain.MemberID {
		mu.Lock()
		defer mu.Unlock()
		return append([]domain.MemberID(nil), seen...)
	}

	att := newFakeAtt()
	stdin := att.captureStdin()
	run := domain.RunID("run-input")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	mirror := startAttach(t, h, run, "watcher", 80, 24, true)
	waitAttached(t, h, run, 1)
	mirror.typeKeys(t, "ignored")

	// A pane that only reported focus and answered a device-attributes
	// query has typed nothing, and the bytes still reach the PTY.
	a := startAttach(t, h, run, "bob", 80, 24, false)
	a.typeKeys(t, "\x1b[I\x1b[?1;2c")
	waitFor(t, "reports on stdin", func() bool { return stdin.String() == "\x1b[I\x1b[?1;2c" })
	if got := report(); len(got) != 0 {
		t.Fatalf("terminal reports were recorded as input by %v", got)
	}

	a.typeKeys(t, "ls\r")
	waitFor(t, "keystrokes on stdin", func() bool { return stdin.String() == "\x1b[I\x1b[?1;2cls\r" })
	a.typeKeys(t, "pwd\r")
	waitFor(t, "second keystrokes on stdin", func() bool {
		return stdin.String() == "\x1b[I\x1b[?1;2cls\rpwd\r"
	})
	waitFor(t, "input reported once", func() bool { return len(report()) == 1 })

	a.detach()
	if err := a.wait(t); err != nil {
		t.Fatalf("detach returned %v", err)
	}
	mirror.detach()
	if err := mirror.wait(t); err != nil {
		t.Fatalf("mirror detach returned %v", err)
	}
	if got := report(); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("OnInput calls = %v, want one for bob", got)
	}
}
