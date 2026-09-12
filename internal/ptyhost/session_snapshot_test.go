package ptyhost

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
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

	fill := bytes.Repeat([]byte{'a'}, maxClientBuffer-1)
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
	if got, want := s.ring.written, uint64(maxClientBuffer); got != want {
		t.Fatalf("ring cursor = %d, want %d after committed handoff", got, want)
	}
	if len(s.pendingScreen) != 0 {
		t.Fatalf("pending output remained after resize completion: %d", len(s.pendingScreen))
	}
}
