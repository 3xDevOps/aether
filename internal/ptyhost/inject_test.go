package ptyhost

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type stdinWrite struct {
	data string
	at   time.Time
}

func recordWrites(mu *sync.Mutex, writes *[]stdinWrite) funcWriter {
	return funcWriter{write: func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		*writes = append(*writes, stdinWrite{data: string(p), at: time.Now()})
		return len(p), nil
	}}
}

func checkWrites(t *testing.T, writes []stdinWrite, want ...string) {
	t.Helper()
	got := make([]string, len(writes))
	for i, w := range writes {
		got[i] = w.data
	}
	if !slices.Equal(got, want) {
		t.Fatalf("stdin writes %q, want %q", got, want)
	}
	if len(writes) == 2 {
		if gap := writes[1].at.Sub(writes[0].at); gap < submitAfterPaste {
			t.Fatalf("submit followed the paste after %v, want at least %v", gap, submitAfterPaste)
		}
	}
}

func TestInjectPastesWhenTheAgentEnabledBracketedPaste(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-paste")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	var mu sync.Mutex
	var writes []stdinWrite
	sess := h.lookup(RunSession(run))
	sess.mu.Lock()
	sess.stdin = recordWrites(&mu, &writes)
	sess.mu.Unlock()
	a := startAttach(t, h, run, "m1", 120, 30, false)
	waitAttached(t, h, run, 1)

	step := func(output, message, submit string, want ...string) {
		t.Helper()
		att.writeOutput(t, output)
		waitFor(t, "agent output "+output, func() bool { return strings.HasSuffix(a.out.String(), output) })
		mu.Lock()
		writes = nil
		mu.Unlock()
		if err := h.Inject(context.Background(), RunSession(run), "Ana", "", message, submit); err != nil {
			t.Fatalf("Inject(%q): %v", message, err)
		}
		mu.Lock()
		defer mu.Unlock()
		checkWrites(t, writes, want...)
	}

	step("ready> ", "one\ntwo", "\r", "one\ntwo\r")
	step("\x1b[?2004h", "one\ntwo", "\r", "\x1b[200~one\ntwo\x1b[201~", "\r")
	step("working ", "a\x1b[201~b\x1b[20\x1b[20\x1b[201~1~1~c", "\r\r", "\x1b[200~abc\x1b[201~", "\r\r")
	step("\x1b[?2004l", "one\ntwo", "\r", "one\ntwo\r")
}

func TestInjectSeesBracketedPasteHeldBehindAResize(t *testing.T) {
	att := &pendingResizeAttachment{entered: make(chan struct{}), release: make(chan struct{})}
	var mu sync.Mutex
	var writes []stdinWrite
	s := &session{
		att:     att,
		stdin:   recordWrites(&mu, &writes),
		ring:    newRingAt(maxClientBuffer, TerminalPosition{}),
		clients: make(map[*client]struct{}),
		cols:    80,
		rows:    30,
		geoGen:  1,
	}
	resized := make(chan struct{})
	go func() {
		s.applyResize()
		close(resized)
	}()
	<-att.entered
	s.deliver([]byte("\x1b[?2004h"))
	if err := s.inject(context.Background(), "Ana", "", "one\ntwo", "\r"); err != nil {
		t.Fatalf("inject: %v", err)
	}
	close(att.release)
	<-resized
	mu.Lock()
	defer mu.Unlock()
	checkWrites(t, writes, "\x1b[200~one\ntwo\x1b[201~", "\r")
}

// partialStdin stops a write halfway once its context is done, as the Docker
// attachment does by pulling its write deadline in.
type partialStdin struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	buf     []byte
}

func (w *partialStdin) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *partialStdin) WriteContext(ctx context.Context, p []byte) (int, error) {
	if w.entered != nil {
		close(w.entered)
		w.entered = nil
		<-w.release
	}
	if err := ctx.Err(); err != nil {
		n, _ := w.Write(p[:len(p)/2])
		return n, err
	}
	return w.Write(p)
}

func (w *partialStdin) Close() error { return nil }

func TestInjectFinishesAPasteItStarted(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	w := &partialStdin{entered: entered, release: release}
	s := &session{stdin: w, clients: make(map[*client]struct{})}
	s.modes.scan([]byte("\x1b[?2004h"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.inject(ctx, "Ana", "", "one\ntwo", "\r") }()
	<-entered
	cancel()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("inject: %v", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if got, want := string(w.buf), "\x1b[200~one\ntwo\x1b[201~\r"; got != want {
		t.Fatalf("stdin = %q, want %q", got, want)
	}
}

func TestInjectDeliveredDuringAFullResizeIgnoresTheCallerLeaving(t *testing.T) {
	var mu sync.Mutex
	var writes []stdinWrite
	done := make(chan struct{})
	s := &session{
		stdin:         recordWrites(&mu, &writes),
		clients:       make(map[*client]struct{}),
		resizeActive:  true,
		resizeDone:    done,
		pendingScreen: make([]byte, maxClientBuffer),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- s.inject(ctx, "Ana", "", "ship it", "\r") }()
	waitFor(t, "the line on stdin", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(writes) == 1
	})
	cancel()
	select {
	case err := <-result:
		t.Fatalf("inject returned %v while its banner waited for the resize", err)
	case <-time.After(100 * time.Millisecond):
	}
	s.mu.Lock()
	s.resizeActive, s.resizeDone = false, nil
	close(done)
	s.mu.Unlock()
	if err := <-result; err != nil {
		t.Fatalf("inject: %v", err)
	}
}
