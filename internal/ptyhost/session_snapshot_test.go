package ptyhost

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"

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
		ring:         newRing(1 << 20),
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
		ring:    newRing(maxClientBuffer + 1),
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
	if got := s.ring.written; got != 0 {
		t.Fatalf("ring cursor advanced while resize was pending: %d", got)
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
	if got, want := s.ring.written, uint64(maxClientBuffer+1); got != want {
		t.Fatalf("ring cursor = %d, want %d after committed handoff", got, want)
	}
	if len(s.pendingScreen) != 0 {
		t.Fatalf("pending output remained after resize completion: %d", len(s.pendingScreen))
	}
}

func TestScreenCheckpointFlushesPendingUTF8BeforeSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.cast")
	tr, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	s := &session{
		run:        RunSession("checkpoint"),
		tr:         tr,
		screen:     screen,
		checkpoint: checkpointPath(path),
		clients:    make(map[*client]struct{}),
		ring:       newRing(1 << 20),
		cols:       80,
		rows:       24,
	}
	s.deliver([]byte("prefix\xc3"))
	checkpointErr := s.checkpointNow()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	if len(tr.pending) != 0 {
		t.Fatalf("cast pending bytes survived checkpoint: %d", len(tr.pending))
	}
	recovered, _, ok, err := recoverCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("recover checkpoint: ok=%v err=%v", ok, err)
	}
	defer recovered.screen.dispose()
	s.deliver([]byte{0xa9})
	recovered.screen.write([]byte{0xa9})
	if got, want := recovered.screen.term.String(), s.screen.term.String(); got != want {
		t.Fatalf("split UTF-8 checkpoint diverged: got %q want %q", got, want)
	}
	_ = tr.close()
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
		ring:    newRing(1024),
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
