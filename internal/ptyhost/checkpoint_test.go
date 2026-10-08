package ptyhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCompactCheckpointRoundTripsTerminalPosition(t *testing.T) {
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
		t.Fatal("current checkpoint reported as legacy")
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
	finished := make(chan struct{})
	var calls atomic.Int32
	reconstructSnapshot = func(path string) (recordedScreen, error) {
		calls.Add(1)
		close(started)
		<-release
		defer close(finished)
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
	<-finished
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

func TestReplayUsesCheckpointByteMetadata(t *testing.T) {
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
	window, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = window.Reader.Close() }()
	if window.Bytes != len(output) {
		t.Fatalf("Replay size = %d, want %d", window.Bytes, len(output))
	}
	data, err := io.ReadAll(window.Reader)
	if err != nil || string(data) != string(output) {
		t.Fatalf("Replay data = %q, err %v", data, err)
	}
}

func TestCheckpointLongLineageRemainsWritable(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID(strings.Repeat("l", 128))
	path := filepath.Join(dir, string(run)+".cast")
	// Real distinct files whose former per-file checkpoint metadata exceeds
	// 8 MiB. Small output events keep the regression affordable in the suite.
	const count = 40000
	output := []byte("old\r\nolder\r\n")
	start := time.Now().Add(-time.Hour)
	firstIncarnation := start.UnixNano()
	event := castLine(start, "o", output)
	segments := make([]castSegment, 0, count)
	legacy := screenCheckpoint{Version: 2}
	for i := range count {
		incarnation := firstIncarnation + int64(i)
		name := path
		if i != count-1 {
			name = filepath.Join(dir, stableCastSegmentName(path, incarnation))
		}
		header := fmt.Sprintf("{\"version\":2,\"width\":80,\"height\":24,\"timestamp\":%d,\"incarnation\":%d}\n", start.Unix(), incarnation)
		data := append([]byte(header), event...)
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		segments = append(segments, castSegment{path: name, incarnation: incarnation, fileBytes: int64(len(data)), outputBytes: len(output)})
		legacy.Segments = append(legacy.Segments, checkpointSegment{
			Path: filepath.Base(name), Incarnation: incarnation, FileBytes: int64(len(data)), OutputBytes: len(output),
		})
	}
	oldEncoding, err := json.Marshal(legacy)
	if err != nil || len(oldEncoding) <= maxScreenCheckpointBytes {
		t.Fatalf("fixture did not exceed the old checkpoint ceiling: %d, %v", len(oldEncoding), err)
	}
	position := TerminalPosition{Epoch: "long-lineage-epoch", Sequence: TerminalSequence(count * len(output))}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, bytes.Repeat(output, 12), position), segments)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatalf("long recording could not publish a checkpoint: %v", err)
	}
	page, err := h.History(t.Context(), run, "", "", 1)
	if err != nil || page.NextCursor == "" || strings.Join(historyTexts(page.Lines), ",") != "older" {
		t.Fatalf("initial history = %+v, %v", page, err)
	}
	key := RunSession(run)
	if err = h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	s := h.lookup(key)
	s.deliver([]byte("resumed"))
	if err = s.checkpointNow(); err != nil {
		t.Fatal(err)
	}
	// Cross the real complete-event rotation boundary after the lineage has
	// already outgrown the old metadata limit.
	s.mu.Lock()
	s.tr.marker(strings.Repeat("x", castSegmentBytes))
	s.commitOutputLocked([]byte("-rotated"))
	s.mu.Unlock()
	if err = h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	latest, err := decodeCheckpoint(checkpointPath(path))
	if err != nil || latest.Version != screenCheckpointVersion || len(latest.Segments) != 1 || latest.SegmentCount != count+2 {
		t.Fatalf("long-lineage rotation did not publish a compact checkpoint: %+v, %v", latest, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() >= castSegmentBytes {
		t.Fatalf("rotation stopped sealing the active segment: %v, %v", info, err)
	}
	info, err = os.Stat(checkpointPath(path))
	if err != nil || info.Size() >= 64<<10 {
		t.Fatalf("checkpoint grew with old segments: %v, %v", info, err)
	}
	wantPosition := TerminalPosition{Epoch: position.Epoch, Sequence: position.Sequence + TerminalSequence(len("resumed-rotated"))}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.Snapshot(run)
	if err != nil || snapshot.Position != wantPosition || !bytes.Contains(snapshot.Data, []byte("resumed-rotated")) {
		t.Fatalf("long-lineage restart reset the screen: %+v, %v", snapshot, err)
	}
	window, err := reopened.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(window.Reader)
	_ = window.Reader.Close()
	want := append(bytes.Repeat(output, count), "resumed-rotated"...)
	if readErr != nil || !bytes.Equal(data, want) || window.Bytes != len(want) || window.Position != wantPosition || window.TruncatedBefore {
		t.Fatalf("long-lineage replay lost history: got %d bytes, want %d, %+v, %v", len(data), len(want), window, readErr)
	}
	older, err := reopened.History(t.Context(), run, page.NextCursor, "", 1)
	if err != nil || strings.Join(historyTexts(older.Lines), ",") != "old" || older.TruncatedBefore {
		t.Fatalf("authenticated cursor lost its original segment: %+v, %v", older, err)
	}
	if err = reopened.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	reopened.lookup(key).deliver([]byte("-again"))
	if err = reopened.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	latest, err = decodeCheckpoint(checkpointPath(path))
	if err != nil || latest.Sequence != wantPosition.Sequence+TerminalSequence(len("-again")) || latest.Epoch != wantPosition.Epoch || latest.SegmentCount != count+3 {
		t.Fatalf("long-lineage writer lost absolute accounting: %+v, %v", latest, err)
	}
	t.Logf("preserved %d original cast files; former metadata %d bytes; compact checkpoint %d bytes; reopened replay %d bytes; resumed sequence %d",
		count, len(oldEncoding), info.Size(), len(data), latest.Sequence)
}

func TestCompactCheckpointKeepsRecentReadsLazyAndFullReplayHonest(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("compact-lazy")
	path := filepath.Join(dir, string(run)+".cast")
	first := writeClosedCast(t, path, []byte("old\r\nolder\r\n"))
	cursor, err := h.History(t.Context(), run, "", "", 1)
	if err != nil || cursor.NextCursor == "" {
		t.Fatalf("initial cursor = %+v, %v", cursor, err)
	}
	current := writeClosedCast(t, path, []byte("latest\r\n"))
	first.path = filepath.Join(dir, stableCastSegmentName(path, first.incarnation))
	position := TerminalPosition{Epoch: "compact-lazy-epoch", Sequence: TerminalSequence(first.outputBytes + current.outputBytes)}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, []byte("old\r\nolder\r\nlatest\r\n"), position), []castSegment{first, current})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(first.path); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.Snapshot(run)
	if err != nil || snapshot.Position != position {
		t.Fatalf("current screen required reading old segments: %+v, %v", snapshot, err)
	}
	recent, err := h.RecentReplay(run, 4)
	if err != nil {
		t.Fatal(err)
	}
	_ = recent.Reader.Close()
	if !recent.Complete || recent.Position != position {
		t.Fatalf("current end boundary lost its proof: %+v", recent)
	}
	if window, replayErr := h.Replay(run); replayErr == nil {
		_ = window.Reader.Close()
		t.Fatal("full replay silently accepted a missing interior segment")
	}
	if _, err = h.History(t.Context(), run, cursor.NextCursor, "", 1); !errors.Is(err, ErrHistoryCursorExpired) {
		t.Fatalf("cursor into missing history = %v", err)
	}
	currentFile, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := currentFile.Write(castLine(time.Now(), "o", []byte("suffix")))
	closeErr := currentFile.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append suffix: %v, %v", writeErr, closeErr)
	}
	if _, err = repairColdSnapshot(path); err == nil {
		t.Fatal("stale repair silently shortened an incomplete lineage")
	}
	unchanged, err := decodeCheckpoint(checkpointPath(path))
	if err != nil || unchanged.Epoch != checkpoint.Epoch || unchanged.Sequence != checkpoint.Sequence || unchanged.SegmentCount != checkpoint.SegmentCount {
		t.Fatalf("failed repair replaced the absolute boundary: %+v, %v", unchanged, err)
	}
}

func TestCheckpointV2MigratesWithoutResettingPosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2-migration.cast")
	first := writeClosedCast(t, path, []byte("old\r\n"))
	current := writeClosedCast(t, path, []byte("latest"))
	first.path = filepath.Join(dir, stableCastSegmentName(path, first.incarnation))
	position := TerminalPosition{Epoch: "v2-epoch", Sequence: TerminalSequence(first.outputBytes + current.outputBytes)}
	checkpoint, err := checkpointFromSnapshot(snapshotForBytes(t, []byte("old\r\nlatest"), position), []castSegment{first, current})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Version, checkpoint.SegmentCount = 2, 0
	checkpoint.Segments = []checkpointSegment{
		{Path: filepath.Base(first.path), Incarnation: first.incarnation, FileBytes: first.fileBytes, OutputBytes: first.outputBytes},
		{Path: filepath.Base(current.path), Incarnation: current.incarnation, FileBytes: current.fileBytes, OutputBytes: current.outputBytes},
	}
	if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repairColdSnapshot(path)
	if err != nil || snapshot.Position != position || !bytes.Contains(snapshot.Data, []byte("latest")) {
		t.Fatalf("v2 migration changed terminal state: %+v, %v", snapshot, err)
	}
	migrated, err := decodeCheckpoint(checkpointPath(path))
	if err != nil || migrated.Version != screenCheckpointVersion || migrated.SegmentCount != 2 || len(migrated.Segments) != 1 || migrated.CastOutputBytes != checkpoint.CastOutputBytes {
		t.Fatalf("v2 migration did not compact its lineage: %+v, %v", migrated, err)
	}
}
