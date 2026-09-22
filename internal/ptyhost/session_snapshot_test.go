package ptyhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type snapshotResizeAttachment struct {
	hook func(cols, rows uint)
}

func (a *snapshotResizeAttachment) Stdin() io.WriteCloser { return snapshotNopWriteCloser{} }
func (a *snapshotResizeAttachment) Stdout() io.Reader     { return bytes.NewReader(nil) }
func (a *snapshotResizeAttachment) Stderr() io.Reader     { return bytes.NewReader(nil) }
func (a *snapshotResizeAttachment) Resize(_ context.Context, cols, rows uint) error {
	if a.hook != nil {
		a.hook(cols, rows)
	}
	return nil
}
func (a *snapshotResizeAttachment) Close() error { return nil }

type snapshotNopWriteCloser struct{}

func (snapshotNopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (snapshotNopWriteCloser) Close() error                { return nil }

func TestResizeSnapshotMatchesColdCast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "screen.cast")
	tr, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}

	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()

	att := &snapshotResizeAttachment{}
	s := &session{
		att:          att,
		tr:           tr,
		ring:         newRingAt(1<<20, TerminalPosition{}),
		clients:      make(map[*client]struct{}),
		cols:         80,
		rows:         30,
		acceptedCols: 80,
		acceptedRows: 24,
		screen:       screen,
		geoGen:       1,
	}
	att.hook = func(_, rows uint) {
		if rows == 30 {
			// This is emitted synchronously from the accepted resize RPC. The
			// repaint's cursor must land on row 30 in both live and cold state.
			s.deliver([]byte("\x1b[30;1HREPAINT"))
		}
	}

	s.deliver([]byte("before"))
	s.applyResize()
	live := s.screen.term.String()
	_ = tr.close()

	recovered, err := readCastScreen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.screen.dispose()
	if got := recovered.screen.term.String(); got != live {
		t.Fatalf("cold cast screen differs from live screen:\nlive=%q\ncold=%q", live, got)
	}
	if recovered.screen.cols != 80 || recovered.screen.rows != 30 {
		t.Fatalf("cold cast geometry = %dx%d, want 80x30", recovered.screen.cols, recovered.screen.rows)
	}
}

type pendingResizeAttachment struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *pendingResizeAttachment) Stdin() io.WriteCloser { return snapshotNopWriteCloser{} }
func (a *pendingResizeAttachment) Stdout() io.Reader     { return bytes.NewReader(nil) }
func (a *pendingResizeAttachment) Stderr() io.Reader     { return bytes.NewReader(nil) }
func (a *pendingResizeAttachment) Resize(_ context.Context, _, rows uint) error {
	if rows == 30 {
		a.once.Do(func() { close(a.entered) })
		<-a.release
	}
	return nil
}
func (a *pendingResizeAttachment) Close() error { return nil }

func TestResizePendingOverflowPublishesCursorAfterCommit(t *testing.T) {
	att := &pendingResizeAttachment{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	s := &session{
		att:     att,
		ring:    newRingAt(maxClientBuffer+1, TerminalPosition{}),
		clients: make(map[*client]struct{}),
		cols:    80,
		rows:    30,
		geoGen:  1,
	}

	resizeDone := make(chan struct{})
	go func() {
		s.applyResize()
		close(resizeDone)
	}()
	<-att.entered

	fill := bytes.Repeat([]byte{'a'}, maxClientBuffer)
	s.deliver(fill)
	if got := s.ring.position().Sequence; got != 0 {
		t.Fatalf("ring position advanced while resize was pending: %d", got)
	}

	outputDone := make(chan struct{})
	go func() {
		s.deliver([]byte{'b'})
		close(outputDone)
	}()
	select {
	case <-outputDone:
		t.Fatal("overflowing output was published before resize completion")
	default:
	}

	close(att.release)
	<-resizeDone
	<-outputDone
	if got, want := s.ring.position().Sequence, TerminalSequence(maxClientBuffer+1); got != want {
		t.Fatalf("ring position = %d, want %d after committed handoff", got, want)
	}
	if len(s.pendingScreen) != 0 {
		t.Fatalf("pending output remained after resize completion: %d", len(s.pendingScreen))
	}
}

func TestScreenCheckpointRecoversSplitUTF8Suffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.cast")
	tr, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.close() }()
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	position := TerminalPosition{Epoch: "checkpoint-test"}
	s := &session{
		run:        RunSession("checkpoint"),
		resumeID:   string(position.Epoch),
		tr:         tr,
		screen:     screen,
		checkpoint: checkpointPath(path),
		clients:    make(map[*client]struct{}),
		ring:       newRingAt(1<<20, position),
		cols:       80,
		rows:       24,
	}
	s.deliver([]byte("prefix\xc3"))
	checkpointErr := s.checkpointNow()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	s.deliver([]byte{0xa9})
	if err = tr.close(); err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := recoverCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("recover checkpoint: ok=%v err=%v", ok, err)
	}
	defer recovered.screen.dispose()
	if got, want := recovered.screen.term.String(), s.screen.term.String(); got != want {
		t.Fatalf("split UTF-8 checkpoint diverged: got %q want %q", got, want)
	}
}

func TestCheckpointBoundaryKeepsConcurrentSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.cast")
	tr, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		_ = tr.close()
		t.Fatal(err)
	}
	defer screen.dispose()
	position := TerminalPosition{Epoch: "checkpoint-suffix-test"}
	s := &session{
		run:        RunSession("checkpoint-suffix"),
		resumeID:   string(position.Epoch),
		tr:         tr,
		screen:     screen,
		checkpoint: checkpointPath(path),
		clients:    make(map[*client]struct{}),
		ring:       newRingAt(1<<20, position),
	}
	s.deliver([]byte("prefix"))
	s.mu.Lock()
	capture, err := s.captureCheckpointLocked()
	s.mu.Unlock()
	if err != nil {
		_ = tr.close()
		t.Fatal(err)
	}
	s.deliver([]byte("suffix"))
	if err = s.persistCheckpoint(capture); err != nil {
		_ = tr.close()
		t.Fatal(err)
	}
	_ = tr.close()
	recovered, ok, err := recoverCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("recover checkpoint: ok=%v err=%v", ok, err)
	}
	defer recovered.screen.dispose()
	if got, want := recovered.screen.term.String(), s.screen.term.String(); got != want {
		t.Fatalf("checkpoint suffix diverged: got %q want %q", got, want)
	}
}

func TestCheckpointPersistenceDoesNotHoldSessionLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.cast")
	tr, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.close() }()
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	s := &session{
		run:        RunSession("checkpoint-live"),
		tr:         tr,
		screen:     screen,
		checkpoint: checkpointPath(path),
		clients:    make(map[*client]struct{}),
		ring:       newRingAt(1<<20, TerminalPosition{}),
	}
	s.deliver([]byte("prefix\xc3"))
	s.checkpointMu.Lock()
	checkpointDone := make(chan error, 1)
	go func() { checkpointDone <- s.checkpointNow() }()
	var liveDone chan struct{}
	defer func() {
		s.checkpointMu.Unlock()
		if checkpointErr := <-checkpointDone; checkpointErr != nil {
			t.Error(checkpointErr)
		}
		if liveDone != nil {
			<-liveDone
		}
	}()
	waitFor(t, "checkpoint capture", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return len(tr.pending) == 0
	})

	const marker = "live-during-checkpoint"
	suffix := append([]byte{0xa9}, bytes.Repeat([]byte("output\r\n"), 16<<10)...)
	suffix = append(suffix, marker...)
	liveDone = make(chan struct{})
	active := false
	go func() {
		s.deliver(suffix)
		active = s.isActive()
		close(liveDone)
	}()
	select {
	case <-liveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("live output or control remained blocked by checkpoint persistence")
	}
	if !active || !strings.Contains(s.screen.term.String(), marker) {
		t.Fatal("active terminal did not display output during checkpoint persistence")
	}
	recorded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(recorded, []byte(marker)) {
		t.Fatal("ordinary transcript writes stalled behind checkpoint persistence")
	}
}

func TestCheckpointCountsResizeFlushedUTF8AcrossRestart(t *testing.T) {
	h, _ := newTestHost(t, func(cfg *Config) { cfg.ReplayBytes = 1 })
	run := domain.RunID("run-checkpoint-utf8")
	att1 := newFakeAtt()
	if err := h.StartSession(context.Background(), RunSession(run), att1); err != nil {
		t.Fatal(err)
	}
	att1.writeOutput(t, "prefix\xc3")
	s := h.lookup(RunSession(run))
	waitFor(t, "pending UTF-8 output", func() bool {
		s.tr.mu.Lock()
		defer s.tr.mu.Unlock()
		return len(s.tr.pending) == 1
	})
	s.tr.resize(80, 24)
	s.tr.mu.Lock()
	gotCount := s.tr.outputBytes
	s.tr.mu.Unlock()
	if gotCount != len([]byte("prefix\xc3")) {
		t.Fatalf("resize-flushed output count = %d, want %d", gotCount, len([]byte("prefix\xc3")))
	}
	if err := s.checkpointNow(); err != nil {
		t.Fatal(err)
	}
	att1.writeOutput(t, string([]byte{0xa9}))
	want := []byte("prefix\xc3\xa9")
	waitFor(t, "complete UTF-8 replay", func() bool {
		s.tr.mu.Lock()
		defer s.tr.mu.Unlock()
		return s.tr.outputBytes == len(want)
	})
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatal(err)
	}

	att2 := newFakeAtt()
	if err := h.StartSession(context.Background(), RunSession(run), att2); err != nil {
		t.Fatal(err)
	}
	kr, kw := io.Pipe()
	t.Cleanup(func() {
		_ = kw.Close()
		_ = kr.Close()
	})
	conn := &replayConn{r: kr}
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(context.Background(), RunSession(run), AttachClient{
			Member: "replay", Cols: 80, Rows: 24, ReadOnly: true, Snapshot: true,
		}, conn, nil)
	}()
	waitFor(t, "restart replay", func() bool { return len(conn.replayBytes()) == 1 })
	_ = kw.Close()
	if err := <-errCh; err != nil {
		t.Fatalf("replay attach: %v", err)
	}
	var replay []byte
	for _, part := range conn.replayBytes() {
		replay = append(replay, part...)
	}
	if !bytes.Equal(replay, want) {
		t.Fatalf("restart replay = %q, want %q", replay, want)
	}
}

func TestClientControlToggleFencesInputAndReconcilesGeometry(t *testing.T) {
	s := &session{
		clients: make(map[*client]struct{}),
		ring:    newRingAt(1024, TerminalPosition{}),
		cols:    80,
		rows:    24,
		stdin:   snapshotNopWriteCloser{},
	}
	c := newClient(nil, AttachClient{Cols: 80, Rows: 24})
	other := newClient(nil, AttachClient{Cols: 100, Rows: 30, ReadOnly: true})
	s.clients[c] = struct{}{}
	s.clients[other] = struct{}{}
	if err := s.setClientReadOnly(c, true); err != nil {
		t.Fatal(err)
	}
	if !c.readOnly || s.imposesNow(c) {
		t.Fatalf("control revoke left writable geometry state: readOnly=%v imposes=%v", c.readOnly, s.imposesNow(c))
	}
	if err := s.writeClientStdinContext(context.Background(), c, []byte("blocked")); err != ErrWriteDenied {
		t.Fatalf("revoked input = %v, want ErrWriteDenied", err)
	}
	if err := s.setClientReadOnly(c, false); err != nil {
		t.Fatal(err)
	}
	if err := s.writeClientStdinContext(context.Background(), c, []byte("allowed")); err != nil {
		t.Fatalf("granted input = %v", err)
	}
}

func TestOlderCheckpointCannotReplaceNewerScreen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.cast")
	tr, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.close() }()
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	s := &session{
		run:        RunSession("checkpoint-order"),
		tr:         tr,
		screen:     screen,
		checkpoint: checkpointPath(path),
		clients:    make(map[*client]struct{}),
		ring:       newRingAt(1<<20, TerminalPosition{}),
	}
	capture := func() *checkpointCapture {
		t.Helper()
		s.mu.Lock()
		captured, captureErr := s.captureCheckpointLocked()
		s.mu.Unlock()
		if captureErr != nil {
			t.Fatal(captureErr)
		}
		return captured
	}
	s.deliver([]byte("checkpoint-old"))
	older := capture()
	defer older.boundary.release()
	s.deliver([]byte("\r\x1b[2Kcheckpoint-latest"))
	newer := capture()
	defer newer.boundary.release()
	if err = s.persistCheckpoint(newer); err != nil {
		t.Fatal(err)
	}
	if err = s.persistCheckpoint(older); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	var saved screenCheckpoint
	if err = json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	restored, err := newTerminalScreen(saved.Cols, saved.Rows)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.dispose()
	restored.write(saved.Data)
	if text := restored.term.String(); !strings.Contains(text, "checkpoint-latest") {
		t.Fatalf("persisted screen rolled back: %q", text)
	}
}

func TestFinalizingSessionDefersArchiveFallback(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("finalizing-archive")
	key := RunSession(run)
	att := newFakeAtt()
	if err := h.StartSession(context.Background(), key, att); err != nil {
		t.Fatal(err)
	}
	s := h.lookup(key)
	s.checkpointMu.Lock()
	unlock := sync.OnceFunc(s.checkpointMu.Unlock)
	defer unlock()
	const want = "final-before-archive"
	att.writeOutput(t, want)
	_ = att.outW.Close()
	waitFor(t, "session finalization", func() bool { return !s.isActive() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	attachErr := h.Attach(ctx, key, AttachClient{ReadOnly: true}, &bytes.Buffer{}, nil)
	if !errors.Is(attachErr, context.DeadlineExceeded) {
		t.Fatalf("attach during final persistence = %v, want cancellation before archive fallback", attachErr)
	}
	unlock()
	if err := h.StopSession(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	replay, size, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	data, err := io.ReadAll(replay)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want || size != len(want) {
		t.Fatalf("finished archive = %q (%d bytes), want %q", data, size, want)
	}
}

func TestInjectionBannerAdvancesTerminalPosition(t *testing.T) {
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	start := TerminalPosition{Epoch: "banner", Sequence: 41}
	s := &session{
		resumeID: "banner",
		ring:     newRingAt(1<<20, start),
		clients:  make(map[*client]struct{}),
		screen:   screen,
	}
	banner := renderBanner("Ana", "blue", "ship it")
	if err = s.annotateInjection(context.Background(), "Ana", "blue", "ship it"); err != nil {
		t.Fatal(err)
	}
	position := s.ring.position()
	if want := start.Sequence + TerminalSequence(len(banner)); position != (TerminalPosition{Epoch: start.Epoch, Sequence: want}) {
		t.Fatalf("position after banner = %+v, want epoch %q sequence %d", position, start.Epoch, want)
	}
	if delta, ok := s.ring.since(start); !ok || !bytes.Equal(delta, banner) {
		t.Fatalf("banner delta = %q, ok=%v, want %q", delta, ok, banner)
	}
}

func TestScreenSnapshotAndDeltaMeetAtExactPosition(t *testing.T) {
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	seed := TerminalPosition{Epoch: "seam", Sequence: 100}
	s := &session{
		resumeID: "seam",
		ring:     newRingAt(1024, seed),
		clients:  make(map[*client]struct{}),
		screen:   screen,
	}
	s.deliver([]byte("before"))
	s.mu.Lock()
	snapshot := s.screenSnapshotLocked()
	s.mu.Unlock()
	if snapshot.Position != (TerminalPosition{Epoch: seed.Epoch, Sequence: 106}) {
		t.Fatalf("snapshot position = %+v", snapshot.Position)
	}
	s.deliver([]byte("after"))
	delta, ok := s.ring.since(snapshot.Position)
	if !ok || string(delta) != "after" {
		t.Fatalf("snapshot delta = %q, ok=%v", delta, ok)
	}
	restored := restoreScreenSnapshot(t, snapshot)
	if _, err = restored.Write(delta); err != nil {
		t.Fatal(err)
	}
	assertEquivalentScreen(t, s.screen.term, restored)
	cloned := cloneScreenSnapshot(snapshot)
	if cloned.Position != snapshot.Position {
		t.Fatalf("cloned position = %+v, want %+v", cloned.Position, snapshot.Position)
	}
}

func TestConcurrentOutputAndSnapshotPreserveOrdering(t *testing.T) {
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	s := &session{
		resumeID: "concurrent",
		ring:     newRingAt(1024, TerminalPosition{Epoch: "concurrent", Sequence: 17}),
		clients:  make(map[*client]struct{}),
		screen:   screen,
	}
	s.deliver([]byte("base"))
	start := make(chan struct{})
	snapshotCh := make(chan ScreenSnapshot, 1)
	delivered := make(chan struct{})
	go func() {
		<-start
		s.mu.Lock()
		snapshotCh <- s.screenSnapshotLocked()
		s.mu.Unlock()
	}()
	go func() {
		<-start
		s.deliver([]byte("-concurrent"))
		close(delivered)
	}()
	close(start)
	snapshot := <-snapshotCh
	<-delivered
	delta, ok := s.ring.since(snapshot.Position)
	if !ok {
		t.Fatalf("snapshot position %+v was not resumable", snapshot.Position)
	}
	restored := restoreScreenSnapshot(t, snapshot)
	if _, err = restored.Write(delta); err != nil {
		t.Fatal(err)
	}
	assertEquivalentScreen(t, s.screen.term, restored)
}

func TestStopIgnoresCheckpointDiagnostic(t *testing.T) {
	s := &session{
		clients:       make(map[*client]struct{}),
		checkpointErr: errors.New("checkpoint storage unavailable"),
	}
	if err := s.stop(); err != nil {
		t.Fatalf("first stop returned checkpoint diagnostic: %v", err)
	}
	if err := s.stop(); err != nil {
		t.Fatalf("idempotent stop returned checkpoint diagnostic: %v", err)
	}
}
