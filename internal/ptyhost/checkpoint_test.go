package ptyhost

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func writeClosedCast(t *testing.T, path string, output []byte) castSegment {
	t.Helper()
	writer, err := newCastWriter(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	writer.output(output)
	if err = writer.close(); err != nil {
		t.Fatal(err)
	}
	segment, err := inspectCastSegment(path)
	if err != nil {
		t.Fatal(err)
	}
	return segment
}

func snapshotForBytes(t *testing.T, data []byte, position TerminalPosition) ScreenSnapshot {
	t.Helper()
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()
	var modes modeScanner
	screen.write(data)
	modes.scan(data)
	return makeScreenSnapshot(screen, modes, position)
}

func TestCheckpointV2RoundTripsTerminalPosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.cast")
	output := []byte("first\r\nsecond")
	segment := writeClosedCast(t, path, output)
	position := TerminalPosition{Epoch: "epoch-roundtrip", Sequence: TerminalSequence(len(output))}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, output, position), []castSegment{segment})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}

	recovered, segments, gotPosition, legacy, err := loadCurrentCheckpoint(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.screen.dispose()
	if legacy {
		t.Fatal("v2 checkpoint reported as legacy")
	}
	if gotPosition != position {
		t.Fatalf("position = %#v, want %#v", gotPosition, position)
	}
	if len(segments) != 1 || segments[0].outputBytes != len(output) {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestCheckpointV1MigratesWithFreshEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.cast")
	output := []byte("legacy screen")
	segment := writeClosedCast(t, path, output)
	snapshot := snapshotForBytes(t, output, TerminalPosition{})
	legacy := screenCheckpoint{
		Version: 1, Incarnation: segment.incarnation, CastOffset: segment.fileBytes,
		Cols: snapshot.Cols, Rows: snapshot.Rows, Data: snapshot.Data,
		Segments: []checkpointSegment{{
			Path: filepath.Base(path), Incarnation: segment.incarnation,
			FileBytes: segment.fileBytes, OutputBytes: segment.outputBytes,
		}},
	}
	if err := writeCheckpointFile(checkpointPath(path), legacy); err != nil {
		t.Fatal(err)
	}

	repaired, err := repairColdSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Position.Epoch == "" || repaired.Position.Sequence != 0 {
		t.Fatalf("migrated position = %#v, want fresh epoch at zero", repaired.Position)
	}
	migrated, err := decodeCheckpoint(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != screenCheckpointVersion || migrated.Epoch != repaired.Position.Epoch || migrated.Sequence != 0 {
		t.Fatalf("migrated checkpoint = %#v", migrated)
	}
}

func TestCheckpointRejectsFutureAndTruncatedBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.cast")
	output := []byte("short")
	segment := writeClosedCast(t, path, output)
	position := TerminalPosition{Epoch: "epoch-invalid", Sequence: TerminalSequence(len(output))}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, output, position), []castSegment{segment})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Sequence++
	if err := writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := loadCurrentCheckpoint(path, false); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("future checkpoint error = %v", err)
	}

	checkpoint.Sequence--
	checkpoint.Segments[0].FileBytes++
	checkpoint.CastOffset++
	if err := writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := loadCurrentCheckpoint(path, false); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated checkpoint error = %v", err)
	}
}

func TestRestartPreservesCheckpointPosition(t *testing.T) {
	dir := t.TempDir()
	newHost := func(t *testing.T) *Host {
		t.Helper()
		h, err := New(Config{TranscriptDir: dir, ReplayBytes: 1 << 20, DefaultCols: 80, DefaultRows: 24})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	run := domain.RunID("position-restart")
	positionOf := func(h *Host) TerminalPosition {
		s := h.lookup(RunSession(run))
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.ring.position()
	}
	first := newHost(t)
	att1 := newFakeAtt()
	if err := first.StartSession(context.Background(), RunSession(run), att1); err != nil {
		t.Fatal(err)
	}
	att1.writeOutput(t, "before-crash")
	waitFor(t, "first position", func() bool {
		return positionOf(first).Sequence == TerminalSequence(len("before-crash"))
	})
	if err := first.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatal(err)
	}
	before, err := decodeCheckpoint(filepath.Join(dir, string(run)+".screen"))
	if err != nil {
		t.Fatal(err)
	}

	second := newHost(t)
	att2 := newFakeAtt()
	if err := second.StartSession(context.Background(), RunSession(run), att2); err != nil {
		t.Fatal(err)
	}
	got := positionOf(second)
	want := TerminalPosition{Epoch: before.Epoch, Sequence: before.Sequence}
	if got != want {
		t.Fatalf("restart position = %#v, want %#v", got, want)
	}
	att2.writeOutput(t, "-after")
	waitFor(t, "advanced restart position", func() bool {
		return positionOf(second).Sequence == want.Sequence+TerminalSequence(len("-after"))
	})
}

func TestCheckpointPersistenceFailureKeepsLiveSnapshotAvailable(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("checkpoint-write-failure")
	att := newFakeAtt()
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatal(err)
	}
	att.writeOutput(t, "still-live")
	s := h.lookup(RunSession(run))
	waitFor(t, "live output", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.ring.position().Sequence == TerminalSequence(len("still-live"))
	})
	s.mu.Lock()
	s.checkpoint = filepath.Join(dir, "missing", "checkpoint.screen")
	s.mu.Unlock()
	if err := s.checkpointNow(); err != nil {
		t.Fatalf("best-effort checkpoint returned error: %v", err)
	}
	s.mu.Lock()
	persistErr := s.checkpointErr
	s.mu.Unlock()
	if persistErr == nil {
		t.Fatal("checkpoint persistence seam did not fail")
	}
	snapshot, err := h.Snapshot(run)
	if err != nil {
		t.Fatalf("live Snapshot after persistence failure: %v", err)
	}
	if snapshot.Position.Sequence != TerminalSequence(len("still-live")) || len(snapshot.Data) == 0 {
		t.Fatalf("live snapshot after persistence failure = %#v", snapshot)
	}
}

func TestSnapshotRepairIsPromptAndSingleflight(t *testing.T) {
	h, dir := newTestHost(t)
	path := filepath.Join(dir, "slow-repair.cast")
	writeClosedCast(t, path, []byte(strings.Repeat("large-output\r\n", 1<<18)))
	if err := os.WriteFile(checkpointPath(path), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	original := reconstructSnapshot
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	reconstructSnapshot = func(path string) (recordedScreen, error) {
		calls.Add(1)
		close(started)
		<-release
		return original(path)
	}
	defer func() { reconstructSnapshot = original }()

	before := time.Now()
	if _, err := h.Snapshot("slow-repair"); !errors.Is(err, ErrSnapshotPending) {
		t.Fatalf("first Snapshot error = %v, want ErrSnapshotPending", err)
	}
	if elapsed := time.Since(before); elapsed >= 250*time.Millisecond {
		t.Fatalf("Snapshot blocked for %v before returning pending", elapsed)
	}
	<-started
	for range 8 {
		if _, err := h.Snapshot("slow-repair"); !errors.Is(err, ErrSnapshotPending) {
			t.Fatalf("concurrent Snapshot error = %v, want pending", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("repair calls = %d, want 1", got)
	}
	close(release)
	waitFor(t, "snapshot repair", func() bool {
		_, err := h.Snapshot("slow-repair")
		return err == nil
	})
}

func TestSnapshotRepairCannotReplaceNewerCheckpoint(t *testing.T) {
	h, dir := newTestHost(t)
	path := filepath.Join(dir, "repair-race.cast")
	segment := writeClosedCast(t, path, []byte("old"))
	if err := os.WriteFile(checkpointPath(path), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	original := reconstructSnapshot
	started := make(chan struct{})
	release := make(chan struct{})
	reconstructSnapshot = func(path string) (recordedScreen, error) {
		recovered, err := original(path)
		close(started)
		<-release
		return recovered, err
	}
	defer func() { reconstructSnapshot = original }()
	if _, err := h.Snapshot("repair-race"); !errors.Is(err, ErrSnapshotPending) {
		t.Fatalf("Snapshot error = %v, want pending", err)
	}
	<-started

	newerPosition := TerminalPosition{Epoch: "newer-epoch", Sequence: 3}
	newer, err := checkpointFromSnapshot(snapshotForBytes(t, []byte("newer"), newerPosition), []castSegment{segment})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCheckpointFile(checkpointPath(path), newer); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitFor(t, "newer checkpoint retained", func() bool {
		snapshot, snapErr := h.Snapshot("repair-race")
		return snapErr == nil && snapshot.Position == newerPosition
	})
	persisted, err := decodeCheckpoint(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Epoch != newerPosition.Epoch || persisted.Sequence != newerPosition.Sequence {
		t.Fatalf("repair regressed newer checkpoint: %#v", persisted)
	}
}

func TestRecentReplayReportsOnlyProvenPosition(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("recent-window")
	path := filepath.Join(dir, string(run)+".cast")
	output := []byte("bounded recent output\n")
	segment := writeClosedCast(t, path, output)

	incomplete, err := h.RecentReplay(run, 128)
	if err != nil {
		t.Fatal(err)
	}
	if incomplete.Complete || incomplete.Position != (TerminalPosition{}) || incomplete.Cols != 80 || incomplete.Rows != 24 {
		t.Fatalf("uncheckpointed window = %#v", incomplete)
	}
	_ = incomplete.Reader.Close()

	position := TerminalPosition{Epoch: "recent-epoch", Sequence: TerminalSequence(len(output))}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, output, position), []castSegment{segment})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}
	complete, err := h.RecentReplay(run, 128)
	if err != nil {
		t.Fatal(err)
	}
	if !complete.Complete || complete.Position != position || complete.Cols != 80 || complete.Rows != 24 {
		t.Fatalf("checkpointed window = %#v", complete)
	}
	data, err := io.ReadAll(complete.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = complete.Reader.Close()
	if string(data) != string(output) || complete.Bytes != len(data) {
		t.Fatalf("window output = %q (%d)", data, complete.Bytes)
	}
}

func TestReplayUsesV2CheckpointByteMetadata(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("metadata-replay")
	path := filepath.Join(dir, string(run)+".cast")
	output := []byte("metadata output")
	segment := writeClosedCast(t, path, output)
	position := TerminalPosition{Epoch: "metadata-epoch", Sequence: TerminalSequence(len(output))}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, output, position), []castSegment{segment})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}
	replay, size, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if size != len(output) {
		t.Fatalf("Replay size = %d, want %d", size, len(output))
	}
	data, err := io.ReadAll(replay)
	if err != nil || string(data) != string(output) {
		t.Fatalf("Replay data = %q, err %v", data, err)
	}
}
