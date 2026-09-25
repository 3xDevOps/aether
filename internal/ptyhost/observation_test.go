package ptyhost

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func observationSession(t *testing.T, replay int) (*Host, SessionKey, *fakeAtt) {
	t.Helper()
	h, err := New(Config{TranscriptDir: t.TempDir(), DefaultCols: 12, DefaultRows: 3, ReplayBytes: replay})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = h.Close() })
	key := RunShellSession(domain.RunID("observe-run"), "t1")
	att := newFakeAtt()
	t.Cleanup(func() { _ = att.inR.Close(); _ = att.inW.Close(); _ = att.outW.Close() })
	if err := h.StartDevelopmentSession(context.Background(), key, att); err != nil { t.Fatal(err) }
	return h, key, att
}

func allowSession(h *Host, key SessionKey) SessionAdmission {
	return SessionAdmission{Generation: h.SessionGeneration(key), Admit: func(accept func() error) error { return accept() }}
}

func TestObserveSessionActiveBufferUnicodeStylesAndCursor(t *testing.T) {
	h, key, _ := observationSession(t, 128)
	s := h.lookup(key)
	s.deliver([]byte("primary\x1b[?1049h\x1b[2J\x1b[H\x1b[1;3;4:3;38;2;12;34;56;48;5;42m界e\u0301\x1b[?25l\x1b[2;5H"))
	o, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	if !o.Alternate || strings.Contains(o.Text, "primary") || o.Lines[0].Text != "界e\u0301" {
		t.Fatalf("active viewport = %#v", o)
	}
	wide, continuation, combined := o.Lines[0].Cells[0], o.Lines[0].Cells[1], o.Lines[0].Cells[2]
	if wide.Text != "界" || wide.Width != 2 || continuation.Width != 0 || combined.Text != "e\u0301" || combined.Width != 1 {
		t.Fatalf("unicode cells: %+v %+v %+v", wide, continuation, combined)
	}
	if !wide.Bold || !wide.Italic || wide.Underline != 3 || wide.Foreground != (ScreenColor{Mode:"rgb", Value:0x0c2238}) || wide.Background != (ScreenColor{Mode:"indexed", Value:42}) {
		t.Fatalf("style = %+v", wide)
	}
	if o.Cursor != (ScreenCursor{X:4, Y:1, Visible:false}) { t.Fatalf("cursor = %+v", o.Cursor) }
	if o.Position != o.Snapshot.Position || o.Incarnation != string(o.Position.Epoch) { t.Fatal("capture boundaries disagree") }
	s.deliver([]byte("\x1b[?1049l"))
	normal, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	if normal.Alternate || normal.Lines[0].Text != "primary" { t.Fatalf("primary restored = %q", normal.Text) }
	if o.Lines[0].Cells[0] != wide { t.Fatal("later output mutated captured cells") }
}

func TestObserveSessionResizeAndResetReviseWithoutInventingOutput(t *testing.T) {
	h, key, att := observationSession(t, 128)
	before, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	if err := h.ResizeSession(context.Background(), key, allowSession(h,key), 20, 4); err != nil { t.Fatal(err) }
	after, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	if after.Cols != 20 || after.Rows != 4 || after.Position != before.Position || after.Revision <= before.Revision || after.GeometryRevision <= before.GeometryRevision {
		t.Fatalf("resize before=%+v after=%+v", before, after)
	}
	calls := att.sizeCalls()
	if calls[len(calls)-1] != [2]uint{20,4} { t.Fatalf("runtime geometry = %v", calls) }
	h.lookup(key).deliver([]byte("before reset\x1bc"))
	reset, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	if strings.TrimSpace(reset.Text) != "" || reset.Revision <= after.Revision || reset.GeometryRevision != after.GeometryRevision { t.Fatalf("reset = %+v", reset) }
}

func TestSessionOutputBoundsMissingEvictedAndEnded(t *testing.T) {
	h, key, _ := observationSession(t, 8)
	initial, err := h.ReadSessionOutput(key, OutputRequest{MaxBytes: 3})
	if err != nil { t.Fatal(err) }
	if !initial.MissingCursor || initial.Truncated { t.Fatalf("initial = %+v", initial) }
	s := h.lookup(key)
	s.deliver([]byte("0123456789"))
	o, err := h.ReadSessionOutput(key, OutputRequest{After: &initial.Position, MaxBytes: 3})
	if err != nil { t.Fatal(err) }
	if string(o.Data) != "234" || !o.Truncated || o.MissingCursor || !o.More || o.Start.Sequence != 2 || o.Next.Sequence != 5 || o.Position.Sequence != 10 { t.Fatalf("evicted = %+v", o) }
	next, err := h.ReadSessionOutput(key, OutputRequest{After:&o.Next, MaxBytes: 8})
	if err != nil { t.Fatal(err) }
	if string(next.Data) != "56789" || next.More || next.Truncated || next.MissingCursor { t.Fatalf("next = %+v", next) }
	future := next.Position
	future.Sequence++
	missing, err := h.ReadSessionOutput(key, OutputRequest{After:&future, MaxBytes: 8})
	if err != nil { t.Fatal(err) }
	if !missing.MissingCursor || !missing.Truncated || string(missing.Data) != "23456789" { t.Fatalf("future = %+v", missing) }
	s.end()
	final, err := h.ReadSessionOutput(key, OutputRequest{After:&o.Next, MaxBytes: 8})
	if err != nil { t.Fatal(err) }
	if !final.Ended || string(final.Data) != "56789" { t.Fatalf("final = %+v", final) }
	screen, err := h.ObserveSession(key)
	if err != nil || !screen.Ended || screen.Lines[0].Text != "0123456789" { t.Fatalf("final screen=%+v err=%v", screen, err) }
}

func TestSessionWaitScreenOutputTimeoutCancellationAndExit(t *testing.T) {
	h, key, _ := observationSession(t, 8)
	initial, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.WaitSession(ctx, key, WaitRequest{Generation:initial.Generation, AfterRevision:initial.Revision})
	if !errors.Is(err, context.Canceled) { t.Fatalf("cancellation = %v", err) }
	timeout, err := h.WaitSession(context.Background(), key, WaitRequest{Generation:initial.Generation, Contains:"ready", Timeout:time.Millisecond})
	if err != nil || timeout.Matched || !timeout.TimedOut || timeout.Screen.Position != initial.Position { t.Fatalf("timeout=%+v err=%v", timeout, err) }
	result := make(chan WaitObservation, 1)
	fail := make(chan error, 1)
	go func() {
		o, err := h.WaitSession(context.Background(), key, WaitRequest{Generation:initial.Generation, AfterRevision:initial.Revision, Timeout:time.Second})
		if err != nil { fail <- err; return }
		result <- o
	}()
	if err := h.ResizeSession(context.Background(), key, allowSession(h,key), 20, 3); err != nil { t.Fatal(err) }
	select {
	case err := <-fail: t.Fatal(err)
	case o := <-result:
		if !o.Matched || o.TimedOut || o.Screen.Cols != 20 || o.Screen.Position != initial.Position { t.Fatalf("resize wait = %+v", o) }
	case <-time.After(2*time.Second): t.Fatal("resize did not wake waiter")
	}
	h.lookup(key).deliver([]byte("application ready"))
	output, err := h.WaitSession(context.Background(), key, WaitRequest{Generation:initial.Generation, AfterOutput:&initial.Position, Timeout:time.Second})
	if err != nil || !output.Matched || !output.Truncated || output.MissingCursor { t.Fatalf("output wait=%+v err=%v", output, err) }
	visible, err := h.WaitSession(context.Background(), key, WaitRequest{Generation:initial.Generation, Contains:"ready", Timeout:time.Second})
	if err != nil || !visible.Matched { t.Fatalf("visible wait=%+v err=%v", visible, err) }
	h.lookup(key).end()
	exit, err := h.WaitSession(context.Background(), key, WaitRequest{Generation:initial.Generation, Ended:true, Timeout:time.Second})
	if err != nil || !exit.Matched || !exit.Screen.Ended { t.Fatalf("exit wait=%+v err=%v", exit, err) }
}

func TestSessionAdmissionFencesReplacementAndDenial(t *testing.T) {
	h, key, att := observationSession(t, 128)
	captured := att.captureStdin()
	old := allowSession(h,key)
	denied := old
	denied.Admit = func(func() error) error { return nil }
	if err := h.WriteSessionInput(context.Background(), key, denied, []byte("bad")); !errors.Is(err, ErrWriteDenied) { t.Fatalf("missing admission = %v", err) }
	if err := h.WriteSessionInput(context.Background(), key, old, []byte("\x1b[Ahello\x1b")); err != nil { t.Fatal(err) }
	deadline := time.After(time.Second)
	for captured.String() != "\x1b[Ahello\x1b" {
		select { case <-deadline: t.Fatalf("input = %q", captured.String()); default: time.Sleep(time.Millisecond) }
	}
	h.lookup(key).end()
	replacement := newFakeAtt()
	t.Cleanup(func() { _ = replacement.inR.Close(); _ = replacement.inW.Close(); _ = replacement.outW.Close() })
	if err := h.StartDevelopmentSession(context.Background(), key, replacement); err != nil { t.Fatal(err) }
	if err := h.WriteSessionInput(context.Background(), key, old, []byte("bad")); !errors.Is(err, ErrSessionReplaced) { t.Fatalf("stale input = %v", err) }
	if err := h.ResizeSession(context.Background(), key, old, 40, 4); !errors.Is(err, ErrSessionReplaced) { t.Fatalf("stale resize = %v", err) }
	if _, err := h.WaitSession(context.Background(), key, WaitRequest{Generation:old.Generation, Contains:"ready"}); !errors.Is(err, ErrSessionReplaced) { t.Fatalf("stale wait = %v", err) }
	oldCursor := TerminalPosition{Epoch:TerminalEpoch(attEpoch(t,h,key)), Sequence:0}
	oldCursor.Epoch = "previous-incarnation"
	o, err := h.ReadSessionOutput(key, OutputRequest{After:&oldCursor})
	if err != nil || !o.MissingCursor || o.Position.Sequence != 0 { t.Fatalf("replacement output=%+v err=%v", o, err) }
}

func attEpoch(t *testing.T, h *Host, key SessionKey) string {
	t.Helper()
	o, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	return o.Incarnation
}

func TestDevelopmentReadOnlyViewerCannotResize(t *testing.T) {
	h, key, att := observationSession(t, 128)
	s := h.lookup(key)
	viewer := newClient(&testConn{r:strings.NewReader(""), w:io.Discard}, AttachClient{ReadOnly:true, Cols:2, Rows:1})
	if err := s.addClient(viewer); err != nil { t.Fatal(err) }
	defer s.removeClient(viewer)
	s.resizeClient(viewer, 3, 2)
	o, err := h.ObserveSession(key)
	if err != nil { t.Fatal(err) }
	if o.Cols != 12 || o.Rows != 3 || len(att.sizeCalls()) != 1 { t.Fatalf("viewer resized terminal: %dx%d calls=%v", o.Cols,o.Rows,att.sizeCalls()) }
}
