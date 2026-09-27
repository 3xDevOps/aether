package ptyhost

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func observationSession(t *testing.T, replay int) (*Host, SessionKey, *fakeAtt) {
	t.Helper()
	h, err := New(Config{TranscriptDir: t.TempDir(), DefaultCols: 12, DefaultRows: 3, ReplayBytes: replay})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	key := RunShellSession(domain.RunID("observe-run"), "t1")
	att := newFakeAtt()
	t.Cleanup(func() { _ = att.inR.Close(); _ = att.inW.Close(); _ = att.outW.Close() })
	if err := h.StartDevelopmentSession(context.Background(), key, att, 12, 3); err != nil {
		t.Fatal(err)
	}
	return h, key, att
}

type completedDevelopmentAttachment struct {
	*fakeAtt
	output io.Reader
}

func (a *completedDevelopmentAttachment) Stdout() io.Reader { return a.output }

func TestDevelopmentAdoptsCompletedOutputAtLaunchGeometry(t *testing.T) {
	h, hostErr := New(Config{TranscriptDir: t.TempDir(), DefaultCols: 80, DefaultRows: 24})
	if hostErr != nil {
		t.Fatal(hostErr)
	}
	t.Cleanup(func() { _ = h.Close() })
	att := &completedDevelopmentAttachment{fakeAtt: newFakeAtt(), output: strings.NewReader("abcdefghij")}
	att.refuseResizes(errors.New("execution already exited"))
	t.Cleanup(func() { _ = att.inR.Close(); _ = att.inW.Close(); _ = att.outW.Close() })
	key := RunShellSession("short-lived", "t1")
	if err := h.StartDevelopmentSession(context.Background(), key, att, 7, 3); err != nil {
		t.Fatal(err)
	}
	final, err := h.WaitSession(context.Background(), key, WaitRequest{
		Generation: h.SessionGeneration(key), Ended: true, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !final.Matched || !final.Screen.Ended || final.Screen.Cols != 7 || final.Screen.Rows != 3 {
		t.Fatalf("completed launch observation = %+v", final)
	}
	if final.Screen.Lines[0].Text != "abcdefg" || final.Screen.Lines[1].Text != "hij" ||
		!final.Screen.Lines[1].Wrapped || final.Screen.Snapshot.Cols != 7 || final.Screen.Snapshot.Rows != 3 {
		t.Fatalf("launch output was reinterpreted at host defaults: %+v", final.Screen)
	}
	if calls := att.sizeCalls(); len(calls) != 0 {
		t.Fatalf("adoption resized an already completed execution: %v", calls)
	}
}

func allowSession(h *Host, key SessionKey) SessionAdmission {
	return SessionAdmission{Generation: h.SessionGeneration(key), Admit: func(accept func() error) error { return accept() }}
}

func TestObserveSessionActiveBufferUnicodeStylesAndCursor(t *testing.T) {
	h, key, _ := observationSession(t, 128)
	s := h.lookup(key)
	s.deliver([]byte("primary\x1b[?1049h\x1b[2J\x1b[H\x1b[1;3;4:3;38;2;12;34;56;48;5;42m界e\u0301\x1b[?25l\x1b[2;5H"))
	o, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Alternate || strings.Contains(o.Text, "primary") || o.Lines[0].Text != "界e\u0301" {
		t.Fatalf("active viewport = %#v", o)
	}
	wide, continuation, combined := o.Lines[0].Cells[0], o.Lines[0].Cells[1], o.Lines[0].Cells[2]
	if wide.Text != "界" || wide.Width != 2 || continuation.Width != 0 || combined.Text != "e\u0301" || combined.Width != 1 {
		t.Fatalf("unicode cells: %+v %+v %+v", wide, continuation, combined)
	}
	if !wide.Bold || !wide.Italic || wide.Underline != 3 || wide.Foreground != (ScreenColor{Mode: "rgb", Value: 0x0c2238}) || wide.Background != (ScreenColor{Mode: "indexed", Value: 42}) {
		t.Fatalf("style = %+v", wide)
	}
	if o.Cursor != (ScreenCursor{X: 4, Y: 1, Visible: false}) {
		t.Fatalf("cursor = %+v", o.Cursor)
	}
	if o.Position != o.Snapshot.Position || o.Incarnation != string(o.Position.Epoch) {
		t.Fatal("capture boundaries disagree")
	}
	s.deliver([]byte("\x1b[?1049l"))
	normal, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if normal.Alternate || normal.Lines[0].Text != "primary" {
		t.Fatalf("primary restored = %q", normal.Text)
	}
	if o.Lines[0].Cells[0] != wide {
		t.Fatal("later output mutated captured cells")
	}
}

func TestObserveSessionResizeAndResetReviseWithoutInventingOutput(t *testing.T) {
	h, key, att := observationSession(t, 128)
	before, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if resizeErr := h.ResizeSession(context.Background(), key, allowSession(h, key), 20, 4); resizeErr != nil {
		t.Fatal(resizeErr)
	}
	after, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if after.Cols != 20 || after.Rows != 4 || after.Position != before.Position || after.Revision <= before.Revision || after.GeometryRevision <= before.GeometryRevision {
		t.Fatalf("resize before=%+v after=%+v", before, after)
	}
	calls := att.sizeCalls()
	if calls[len(calls)-1] != [2]uint{20, 4} {
		t.Fatalf("runtime geometry = %v", calls)
	}
	h.lookup(key).deliver([]byte("before reset\x1bc"))
	reset, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(reset.Text) != "" || reset.Revision <= after.Revision || reset.GeometryRevision != after.GeometryRevision {
		t.Fatalf("reset = %+v", reset)
	}
}

func TestSessionOutputBoundsMissingEvictedAndEnded(t *testing.T) {
	h, key, _ := observationSession(t, 8)
	initial, err := h.ReadSessionOutput(key, OutputRequest{MaxBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !initial.MissingCursor || initial.Truncated {
		t.Fatalf("initial = %+v", initial)
	}
	s := h.lookup(key)
	s.deliver([]byte("0123456789"))
	o, err := h.ReadSessionOutput(key, OutputRequest{After: &initial.Position, MaxBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(o.Data) != "234" || !o.Truncated || o.MissingCursor || !o.More || o.Start.Sequence != 2 || o.Next.Sequence != 5 || o.Position.Sequence != 10 {
		t.Fatalf("evicted = %+v", o)
	}
	next, err := h.ReadSessionOutput(key, OutputRequest{After: &o.Next, MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if string(next.Data) != "56789" || next.More || next.Truncated || next.MissingCursor {
		t.Fatalf("next = %+v", next)
	}
	future := next.Position
	future.Sequence++
	missing, err := h.ReadSessionOutput(key, OutputRequest{After: &future, MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !missing.MissingCursor || !missing.Truncated || string(missing.Data) != "23456789" {
		t.Fatalf("future = %+v", missing)
	}
	s.end()
	final, err := h.ReadSessionOutput(key, OutputRequest{After: &o.Next, MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !final.Ended || string(final.Data) != "56789" {
		t.Fatalf("final = %+v", final)
	}
	screen, err := h.ObserveSession(key)
	if err != nil || !screen.Ended || screen.Lines[0].Text != "0123456789" {
		t.Fatalf("final screen=%+v err=%v", screen, err)
	}
}

func TestSessionWaitScreenOutputTimeoutCancellationAndExit(t *testing.T) {
	h, key, _ := observationSession(t, 8)
	initial, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.WaitSession(ctx, key, WaitRequest{Generation: initial.Generation, AfterRevision: initial.Revision})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	timeout, err := h.WaitSession(context.Background(), key, WaitRequest{Generation: initial.Generation, Contains: "ready", Timeout: time.Millisecond})
	if err != nil || timeout.Matched || !timeout.TimedOut || timeout.Screen.Position != initial.Position {
		t.Fatalf("timeout=%+v err=%v", timeout, err)
	}
	result := make(chan WaitObservation, 1)
	fail := make(chan error, 1)
	go func() {
		o, waitErr := h.WaitSession(context.Background(), key, WaitRequest{Generation: initial.Generation, AfterRevision: initial.Revision, Timeout: time.Second})
		if waitErr != nil {
			fail <- waitErr
		}
		result <- o
	}()
	if resizeErr := h.ResizeSession(context.Background(), key, allowSession(h, key), 20, 3); resizeErr != nil {
		t.Fatal(resizeErr)
	}
	select {
	case waitErr := <-fail:
		t.Fatal(waitErr)
	case o := <-result:
		if !o.Matched || o.TimedOut || o.Screen.Cols != 20 || o.Screen.Position != initial.Position {
			t.Fatalf("resize wait = %+v", o)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resize did not wake waiter")
	}
	h.lookup(key).deliver([]byte("application ready"))
	output, err := h.WaitSession(context.Background(), key, WaitRequest{Generation: initial.Generation, AfterOutput: &initial.Position, Timeout: time.Second})
	if err != nil || !output.Matched || !output.Truncated || output.MissingCursor {
		t.Fatalf("output wait=%+v err=%v", output, err)
	}
	visible, err := h.WaitSession(context.Background(), key, WaitRequest{Generation: initial.Generation, Contains: "ready", Timeout: time.Second})
	if err != nil || !visible.Matched {
		t.Fatalf("visible wait=%+v err=%v", visible, err)
	}
	h.lookup(key).end()
	exit, err := h.WaitSession(context.Background(), key, WaitRequest{Generation: initial.Generation, Ended: true, Timeout: time.Second})
	if err != nil || !exit.Matched || !exit.Screen.Ended {
		t.Fatalf("exit wait=%+v err=%v", exit, err)
	}
}

func TestSessionAdmissionFencesReplacementAndDenial(t *testing.T) {
	h, key, att := observationSession(t, 128)
	captured := att.captureStdin()
	old := allowSession(h, key)
	denied := old
	denied.Admit = func(func() error) error { return nil }
	if err := h.WriteSessionInput(context.Background(), key, denied, []byte("bad")); !errors.Is(err, ErrWriteDenied) {
		t.Fatalf("missing admission = %v", err)
	}
	if err := h.WriteSessionInput(context.Background(), key, old, []byte("\x1b[Ahello\x1b")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for captured.String() != "\x1b[Ahello\x1b" {
		select {
		case <-deadline:
			t.Fatalf("input = %q", captured.String())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	h.lookup(key).end()
	replacement := newFakeAtt()
	t.Cleanup(func() { _ = replacement.inR.Close(); _ = replacement.inW.Close(); _ = replacement.outW.Close() })
	if err := h.StartDevelopmentSession(context.Background(), key, replacement, 12, 3); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteSessionInput(context.Background(), key, old, []byte("bad")); !errors.Is(err, ErrSessionReplaced) {
		t.Fatalf("stale input = %v", err)
	}
	if err := h.ResizeSession(context.Background(), key, old, 40, 4); !errors.Is(err, ErrSessionReplaced) {
		t.Fatalf("stale resize = %v", err)
	}
	if _, err := h.WaitSession(context.Background(), key, WaitRequest{Generation: old.Generation, Contains: "ready"}); !errors.Is(err, ErrSessionReplaced) {
		t.Fatalf("stale wait = %v", err)
	}
	oldCursor := TerminalPosition{Epoch: TerminalEpoch(attEpoch(t, h, key)), Sequence: 0}
	oldCursor.Epoch = "previous-incarnation"
	o, err := h.ReadSessionOutput(key, OutputRequest{After: &oldCursor})
	if err != nil || !o.MissingCursor || o.Position.Sequence != 0 {
		t.Fatalf("replacement output=%+v err=%v", o, err)
	}
}

func attEpoch(t *testing.T, h *Host, key SessionKey) string {
	t.Helper()
	o, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	return o.Incarnation
}

func TestDevelopmentReadOnlyViewerCannotResize(t *testing.T) {
	h, key, att := observationSession(t, 128)
	s := h.lookup(key)
	viewer := newClient(&testConn{r: strings.NewReader(""), w: io.Discard}, AttachClient{ReadOnly: true, Cols: 2, Rows: 1})
	if err := s.addClient(viewer); err != nil {
		t.Fatal(err)
	}
	defer s.removeClient(viewer)
	s.resizeClient(viewer, 3, 2)
	o, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if o.Cols != 12 || o.Rows != 3 || len(att.sizeCalls()) != 0 {
		t.Fatalf("viewer resized terminal: %dx%d calls=%v", o.Cols, o.Rows, att.sizeCalls())
	}
}

func TestDevelopmentRespondsWithoutViewerAndCaptureNeverWrites(t *testing.T) {
	h, key, att := observationSession(t, 128)
	input := att.captureStdin()
	s := h.lookup(key)
	s.deliver([]byte("\x1b[2;4H\x1b[6n\x1b[c\x1b]4;3;#123456\x07\x1b]4;3;?\x07"))
	want := "\x1b[2;4R\x1b[?1;2c\x1b]4;3;rgb:1212/3434/5656\x1b\\"
	deadline := time.After(time.Second)
	for input.String() != want {
		select {
		case <-deadline:
			t.Fatalf("no-viewer replies = %q, want %q", input.String(), want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	for range 3 {
		o, err := h.ObserveSession(key)
		if err != nil {
			t.Fatal(err)
		}
		if o.Palette[3] != 0x123456 || o.Cursor.X != 3 || o.Cursor.Y != 1 {
			t.Fatalf("capture = %+v", o)
		}
		// A screenshot renderer parses this snapshot in an isolated emulator.
		replay, err := newTerminalScreen(o.Cols, o.Rows)
		if err != nil {
			t.Fatal(err)
		}
		replay.write(o.Snapshot.Data)
		replay.dispose()
	}
	s.mu.Lock()
	pending := string(s.protocolPending)
	s.mu.Unlock()
	if pending != "" || input.String() != want {
		t.Fatalf("read-only capture emitted input: pending=%q input=%q", pending, input.String())
	}
}

func TestDevelopmentGraphicsAreExplicitAndResetClearsThem(t *testing.T) {
	h, key, _ := observationSession(t, 128)
	s := h.lookup(key)
	s.deliver([]byte("\x1bPq~\x1b\\\x1b_Ga=T;abcd\x1b\\\x1b]1337;File=inline=1:AAAA\x07"))
	o, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.UnsupportedGraphics, ",") != "sixel,kitty,iterm2" {
		t.Fatalf("unsupported graphics = %v", o.UnsupportedGraphics)
	}
	s.deliver([]byte("\x1bc"))
	reset, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(reset.UnsupportedGraphics) != 0 || reset.Revision <= o.Revision {
		t.Fatalf("reset = %+v", reset)
	}
	if strings.Join(o.UnsupportedGraphics, ",") != "sixel,kitty,iterm2" {
		t.Fatal("reset mutated prior capture")
	}
}

func TestDevelopmentWriteRemainsFencedThroughPhysicalAcceptance(t *testing.T) {
	h, key, _ := observationSession(t, 128)
	service := control.New(control.Config{})
	surface := control.Surface{Kind: control.SurfaceTerminal, ID: "t1", Incarnation: attEpoch(t, h, key)}
	agent := control.Principal{Kind: control.PrincipalRunAgent, RunID: "observe-run"}
	lease, _, err := service.AcquireSurface("observe-run", surface, agent, "agent", false, 0, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	writer := &cancelOnceStdin{started: make(chan struct{})}
	s := h.lookup(key)
	s.mu.Lock()
	s.stdin = writer
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admission := SessionAdmission{Generation: h.SessionGeneration(key), Admit: func(accept func() error) error {
		return service.AdmitSurface("observe-run", surface, agent, "agent", lease.Generation, accept)
	}}
	written := make(chan error, 1)
	go func() { written <- h.WriteSessionInput(ctx, key, admission, []byte("old")) }()
	<-writer.started
	taken := make(chan error, 1)
	go func() {
		_, _, err := service.AcquireSurface("observe-run", surface, control.Principal{Kind: control.PrincipalMember, MemberID: "human"}, "tab", true, lease.Generation, func() error { return nil })
		taken <- err
	}()
	select {
	case err := <-taken:
		t.Fatalf("takeover crossed an in-flight physical write: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if err := <-written; !errors.Is(err, context.Canceled) {
		t.Fatalf("write = %v", err)
	}
	if err := <-taken; err != nil {
		t.Fatal(err)
	}
	if err := h.WriteSessionInput(context.Background(), key, admission, []byte("stale")); !errors.Is(err, control.ErrStale) {
		t.Fatalf("stale physical write = %v", err)
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.buf.String() != "" {
		t.Fatalf("stale input reached process: %q", writer.buf.String())
	}
}

func TestScreenPagesPreserveWideColumnsAndBoundEscapedPayloads(t *testing.T) {
	// A viewport wider than the per-request cell cap must still be readable
	// all the way to its right edge, including a split wide-character pair.
	cells := make([]protocol.DevTerminalCell, 500)
	for i := range cells {
		cells[i] = protocol.DevTerminalCell{Text: "<&>", Width: 1, Inverse: true}
	}
	cells[255] = protocol.DevTerminalCell{Text: "界", Width: 2, UnderlineStyle: 3}
	cells[256] = protocol.DevTerminalCell{Width: 0}
	screen := protocol.DevTerminalScreenResult{
		Terminal:       protocol.DevTerminal{Cols: 500, Rows: 1},
		ScreenRevision: 9, Lines: []protocol.DevTerminalRow{{Cells: cells}},
	}
	request := protocol.DevTerminalScreenParams{}
	first, err := protocol.PageDevTerminalScreen(screen, request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.NextRow == nil || *first.NextRow != 0 || first.NextColumn != 256 || first.Lines[0].Cells[255].Text != "界" {
		t.Fatalf("first page boundary = %+v", first)
	}
	if _, marshalErr := protocol.MarshalDevResult(first); marshalErr != nil {
		t.Fatal(marshalErr)
	}
	request.RowOffset, request.ColumnOffset, request.ExpectedScreenRevision = *first.NextRow, first.NextColumn, first.ScreenRevision
	second, err := protocol.PageDevTerminalScreen(screen, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Truncated || second.NextRow != nil || len(second.Lines[0].Cells) != 244 || second.Lines[0].Cells[0].Width != 0 || second.Lines[0].Cells[243].Text != "<&>" {
		t.Fatalf("continuation = %+v", second)
	}
	screen.ScreenRevision++
	if _, revisionErr := protocol.PageDevTerminalScreen(screen, request); !errors.Is(revisionErr, protocol.ErrDevScreenChanged) {
		t.Fatalf("mixed-revision continuation = %v", revisionErr)
	}
	screen.Lines[0].Cells[0].Text = strings.Repeat("<&>\u0301", 600)
	page, err := protocol.PageDevTerminalScreen(screen, protocol.DevTerminalScreenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || page.NextColumn == 0 || page.NextColumn >= 256 {
		t.Fatalf("byte-limited page must make explicit progress: %+v", page)
	}
	if _, err := protocol.MarshalDevResult(page); err != nil {
		t.Fatal(err)
	}
	screen.Lines[0].Cells[0].Text = strings.Repeat("<", protocol.MaxDevResultBytes)
	if _, err := protocol.PageDevTerminalScreen(screen, protocol.DevTerminalScreenParams{}); err == nil {
		t.Fatal("unpageable single cell was silently truncated")
	}
}

func TestDevelopmentRawViewerCannotAcquireInput(t *testing.T) {
	h, key, _ := observationSession(t, 128)
	raw := &testConn{r: strings.NewReader(""), w: io.Discard}
	admission := func(accept func() error) error { return accept() }
	err := h.Attach(context.Background(), key, AttachClient{InputAdmission: admission}, raw, nil)
	if !errors.Is(err, ErrWriteDenied) {
		t.Fatalf("raw writable attach = %v", err)
	}
	var upgradeErr error
	err = h.Attach(context.Background(), key, AttachClient{
		ReadOnly: true, InputAdmission: admission,
		OnControlReady: func(setReadOnly func(bool) error) { upgradeErr = setReadOnly(false) },
	}, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(upgradeErr, ErrWriteDenied) {
		t.Fatalf("raw viewer upgrade = %v", upgradeErr)
	}
}

func TestDevelopmentResizeFailureKeepsObservedGeometry(t *testing.T) {
	h, key, att := observationSession(t, 128)
	before, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("runtime refused resize")
	att.refuseResizes(denied)
	if resizeErr := h.ResizeSession(context.Background(), key, allowSession(h, key), 40, 8); !errors.Is(resizeErr, denied) {
		t.Fatalf("resize = %v", resizeErr)
	}
	after, err := h.ObserveSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if after.Cols != before.Cols || after.Rows != before.Rows || after.GeometryRevision != before.GeometryRevision || after.Revision != before.Revision {
		t.Fatalf("failed resize changed observation: before=%+v after=%+v", before, after)
	}
	calls := att.sizeCalls()
	if len(calls) != 1 || calls[0] != [2]uint{40, 8} {
		t.Fatalf("development resize performed an uncommitted nudge: %v", calls)
	}
	if _, err := h.ReadSessionOutput(key, OutputRequest{Generation: before.Generation + 1}); !errors.Is(err, ErrSessionReplaced) {
		t.Fatalf("wrong output incarnation = %v", err)
	}
}

func TestWaitDoesNotReportLateTextAsSuccessAfterDeadline(t *testing.T) {
	h, key, _ := observationSession(t, 128)
	s := h.lookup(key)
	result := make(chan WaitObservation, 1)
	failure := make(chan error, 1)
	go func() {
		o, err := h.WaitSession(context.Background(), key, WaitRequest{
			Generation: h.SessionGeneration(key), Contains: "late", Timeout: 50 * time.Millisecond,
		})
		if err != nil {
			failure <- err
			return
		}
		result <- o
	}()
	deadline := time.After(time.Second)
	for {
		s.mu.Lock()
		if s.changed != nil {
			break
		}
		s.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("wait did not subscribe")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Delay observation past its deadline, then publish matching output at
	// that same boundary. The final screen is useful, but not timely success.
	time.Sleep(60 * time.Millisecond)
	s.commitOutputLocked([]byte("late"))
	s.mu.Unlock()
	select {
	case err := <-failure:
		t.Fatal(err)
	case observed := <-result:
		if observed.Matched || !observed.TimedOut || !strings.Contains(observed.Screen.Text, "late") {
			t.Fatalf("late observation = %+v", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("expired wait did not return")
	}
}

func TestSessionGeometryPublishesOnlyAcceptedResize(t *testing.T) {
	h, key, att := observationSession(t, 128)
	blocked := &blockingResizeAtt{fakeAtt: att, block: make(chan struct{}, 1), entered: make(chan struct{}, 1)}
	defer close(blocked.block)
	s := h.lookup(key)
	s.mu.Lock()
	s.att = blocked
	s.mu.Unlock()
	generation := h.SessionGeneration(key)
	done := make(chan error, 1)
	go func() { done <- h.ResizeSession(context.Background(), key, allowSession(h, key), 40, 8) }()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("resize did not enter runtime")
	}
	assertGeometry := func(wantCols, wantRows uint) {
		t.Helper()
		cols, rows, gotGeneration, err := h.SessionGeometry(key)
		if err != nil || cols != wantCols || rows != wantRows || gotGeneration != generation {
			t.Fatalf("geometry = %dx%d generation=%d err=%v, want %dx%d generation=%d", cols, rows, gotGeneration, err, wantCols, wantRows, generation)
		}
	}
	assertGeometry(12, 3)
	blocked.block <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertGeometry(40, 8)
	denied := errors.New("runtime refused resize")
	att.refuseResizes(denied)
	blocked.block <- struct{}{}
	if err := h.ResizeSession(context.Background(), key, allowSession(h, key), 50, 10); !errors.Is(err, denied) {
		t.Fatalf("refused resize = %v", err)
	}
	assertGeometry(40, 8)
	s.end()
	assertGeometry(40, 8)
	if err := h.StopSession(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.SessionGeometry(key); !errors.Is(err, ErrNoSession) {
		t.Fatalf("stopped geometry = %v", err)
	}
}
