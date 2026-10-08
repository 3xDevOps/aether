package ptyhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRollingCastRetainsLiveBoundaryAndRestartScreen(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("rolling-cast")
	key := RunSession(run)
	if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	s := h.lookup(key)
	tap, err := h.TapOutput(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tap.Close() }()
	type tapResult struct {
		data []byte
		err  error
	}
	tapDone := make(chan tapResult, 1)
	go func() {
		data := make([]byte, len("first\r\nsecond\r\nthird\r\n")+9*len("split-\xe7\x95\x8c\xff segment-00\r\n")+len("CURRENT"))
		_, readErr := io.ReadFull(tap, data)
		tapDone <- tapResult{data: data, err: readErr}
	}()
	first := []byte("first\r\nsecond\r\nthird\r\n")
	s.deliver(first)
	page, err := h.History(t.Context(), run, "", "", 1)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("initial cursor: %+v, %v", page, err)
	}
	pinned, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Reader.Close() }()
	before := s.ring.position()
	s.mu.Lock()
	stale, err := s.captureCheckpointLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Real 16 MiB events cross the disk budget without filling the emulator
	// with irrelevant text. Markers do not alter live output or its sequence.
	marker := strings.Repeat("x", castSegmentBytes)
	var retained []byte
	sequence := before.Sequence
	live := append([]byte(nil), first...)
	for i := range 9 {
		s.mu.Lock()
		if i == 0 {
			// The first rotation seals an incomplete UTF-8 prefix. Replay
			// must join it to the next event without replacing raw bytes.
			s.tr.marker(marker)
			s.commitOutputLocked([]byte("split-\xe7"))
		} else {
			s.commitOutputLocked([]byte("split-\xe7"))
			s.tr.marker(marker)
		}
		output := []byte(fmt.Sprintf("\x95\x8c\xff segment-%02d\r\n", i))
		s.commitOutputLocked(output)
		s.mu.Unlock()
		whole := append([]byte("split-\xe7"), output...)
		live = append(live, whole...)
		sequence += TerminalSequence(len(whole))
		if i == 0 {
			window, replayErr := h.Replay(run)
			if replayErr != nil {
				t.Fatal(replayErr)
			}
			data, readErr := io.ReadAll(window.Reader)
			_ = window.Reader.Close()
			if readErr != nil || !bytes.Equal(data, live) {
				t.Fatalf("split UTF-8 rotation changed recorded bytes: %q, want %q, %v", data, live, readErr)
			}
		}
		if i >= 2 {
			retained = append(retained, whole...)
		}
	}
	if position := s.ring.position(); position.Epoch != before.Epoch || position.Sequence != sequence {
		t.Fatalf("rotation reset the live position: %+v, expected %s/%d", position, before.Epoch, sequence)
	}
	if err = stale.persist(); !errors.Is(err, errCheckpointSuperseded) {
		t.Fatalf("old capture could overwrite the rotated checkpoint: %v", err)
	}
	// A reader opened before unlink still has its original file and byte fence.
	got, err := io.ReadAll(pinned.Reader)
	if err != nil || !bytes.Equal(got, first) || pinned.TruncatedBefore {
		t.Fatalf("pinned replay changed during rotation: %q, %+v, %v", got, pinned, err)
	}
	if _, err = h.History(t.Context(), run, page.NextCursor, "", 1); !errors.Is(err, ErrHistoryCursorExpired) {
		t.Fatalf("authenticated expired cursor = %v", err)
	}
	forged, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	forged[len(forged)-1] ^= 1
	if _, err = h.History(t.Context(), run, base64.RawURLEncoding.EncodeToString(forged), "", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("forged pruned cursor = %v", err)
	}
	s.deliver([]byte("CURRENT"))
	retained = append(retained, "CURRENT"...)
	sequence += TerminalSequence(len("CURRENT"))
	live = append(live, "CURRENT"...)
	if tapped := <-tapDone; tapped.err != nil || !bytes.Equal(tapped.data, live) {
		t.Fatalf("live tap reset or lost bytes across retention: %q, %v", tapped.data, tapped.err)
	}
	if err = h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	window, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(window.Reader)
	_ = window.Reader.Close()
	if readErr != nil || !bytes.Equal(got, retained) || window.Bytes != len(retained) {
		t.Fatalf("retained raw output lost byte fidelity: %q, want %q, %v", got, retained, readErr)
	}
	if !window.TruncatedBefore || !window.Complete || window.Position.Sequence != sequence || window.Position.Epoch != before.Epoch {
		t.Fatalf("retained replay metadata = %+v", window)
	}
	page, err = h.History(t.Context(), run, "", "", 2)
	if err != nil || !page.TruncatedBefore {
		t.Fatalf("retained page metadata = %+v, %v", page, err)
	}
	path := filepath.Join(dir, string(run)+".cast")
	segments, err := collectCastHeaders(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk int64
	for _, segment := range segments {
		disk += segment.fileBytes
	}
	if len(segments) > castRetainedSegments || disk > castRetainedBytes {
		t.Fatalf("retention not bounded: %d segments, %d bytes", len(segments), disk)
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(Config{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	snapshot, err := restarted.Snapshot(run)
	if err != nil || !snapshot.TruncatedBefore || snapshot.Position != window.Position || !bytes.Contains(snapshot.Data, []byte("CURRENT")) {
		t.Fatalf("latest compact screen did not survive expiry/restart: %+v, %v", snapshot, err)
	}
	if err = restarted.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	restarted.lookup(key).deliver([]byte("-resumed"))
	resumed, err := restarted.Snapshot(run)
	if err != nil || !resumed.TruncatedBefore || resumed.Position.Epoch != before.Epoch || resumed.Position.Sequence != sequence+TerminalSequence(len("-resumed")) {
		t.Fatalf("restart did not continue absolute position: %+v, %v", resumed, err)
	}
}

func TestCastAgeExpiryPreservesCheckpointAndPinnedDeletion(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("aged-cast")
	key := RunSession(run)
	if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("ANCHOR\r\none\r\ntwo\r\n"))
	cursor, err := h.History(t.Context(), run, "", "", 1)
	if err != nil || cursor.NextCursor == "" {
		t.Fatalf("cursor = %+v, %v", cursor, err)
	}
	if err = h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err = h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	s := h.lookup(key)
	s.deliver([]byte("current"))
	path := filepath.Join(dir, string(run)+".cast")
	archives, err := priorCastPaths(path)
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives = %v, %v", archives, err)
	}
	old := time.Now().Add(-castRetainedAge - time.Hour)
	for _, file := range []string{archives[0], path} {
		if err = os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise history resolution concurrently with checkpoint-driven pruning.
	readDone := make(chan error, 1)
	go func() {
		_, readErr := h.History(context.Background(), run, cursor.NextCursor, "", 1)
		readDone <- readErr
	}()
	if err = s.checkpointNow(); err != nil {
		t.Fatal(err)
	}
	if readErr := <-readDone; readErr != nil && !errors.Is(readErr, ErrHistoryCursorExpired) {
		t.Fatalf("history/prune race returned a false corruption error: %v", readErr)
	}
	if _, err = os.Stat(archives[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sealed age was not enforced: %v", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("old current segment was removed: %v", err)
	}
	if err = h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	recovered, _, position, _, err := loadCurrentCheckpoint(path, false)
	if err != nil {
		t.Fatalf("valid checkpoint required an expired prefix: %v", err)
	}
	if !strings.Contains(recovered.screen.term.String(), "ANCHOR") || !strings.Contains(recovered.screen.term.String(), "current") {
		t.Fatalf("screen lost expired-prefix state: %q", recovered.screen.term.String())
	}
	recovered.screen.dispose()
	window, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = window.Reader.Close() }()
	if !window.TruncatedBefore || !window.Complete || window.Position != position {
		t.Fatalf("atomic replay metadata = %+v", window)
	}
	if err = os.WriteFile(h.ItemLogPath(run)+".compact", []byte("interrupted ACP compaction"), 0o600); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- h.RemoveRunTranscripts(context.Background(), run) }()
	if err = <-deleted; err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(window.Reader)
	if err != nil || string(data) != "current" {
		t.Fatalf("opened replay failed after concurrent delete: %q, %v", data, err)
	}
	for _, file := range []string{path, checkpointPath(path), castRetentionPath(path), h.ItemLogPath(run) + ".compact"} {
		if _, err = os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("run deletion left %s: %v", filepath.Base(file), err)
		}
	}
}

func TestCastPublishedExpirySurvivesInterruptedUnlink(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("prune-publication")
	key := RunSession(run)
	for _, output := range []string{"old\r\n", "latest"} {
		if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
			t.Fatal(err)
		}
		h.lookup(key).deliver([]byte(output))
		if err := h.StopSession(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, string(run)+".cast")
	checkpoint, err := decodeCheckpoint(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	old := checkpoint.Segments[0]
	// Simulate a stop after metadata publication but before unlink. The
	// existing compact checkpoint still describes both original segments.
	if err = writeCastRetention(path, castRetention{Before: checkpoint.Incarnation, OutputBytes: uint64(old.OutputBytes)}); err != nil {
		t.Fatal(err)
	}
	window, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = window.Reader.Close() }()
	data, err := io.ReadAll(window.Reader)
	if err != nil || string(data) != "latest" || !window.TruncatedBefore || !window.Complete {
		t.Fatalf("interrupted prune replay = %q, %+v, %v", data, window, err)
	}
	if _, err = os.Stat(filepath.Join(dir, old.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published prune did not retry its unlink: %v", err)
	}
}

func TestCastRotationCrashRecoversAbsoluteCheckpointSuffix(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("rotation-crash")
	key := RunSession(run)
	if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("durable-screen"))
	if err := h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, string(run)+".cast")
	checkpoint, err := decodeCheckpoint(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(castHeader{Version: 2, Width: checkpoint.Cols, Height: checkpoint.Rows, Timestamp: time.Now().Unix(), Incarnation: checkpoint.Incarnation + 1})
	if err != nil {
		t.Fatal(err)
	}
	next := append(header, '\n')
	next = append(next, castLine(time.Now(), "s", checkpoint.Data)...)
	next = append(next, castLine(time.Now(), "o", []byte("-suffix"))...)
	if err = os.WriteFile(path+".next", next, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, filepath.Join(dir, stableCastSegmentName(path, checkpoint.Incarnation))); err != nil {
		t.Fatal(err)
	}
	if err = recoverCastRotation(path); err != nil {
		t.Fatal(err)
	}
	// Repair a checkpoint naming the old current file after a rotation crash;
	// it must apply only the retained suffix, preserving the original epoch.
	snapshot, err := repairColdSnapshot(path)
	if err != nil || snapshot.Position.Epoch != checkpoint.Epoch || snapshot.Position.Sequence != checkpoint.Sequence+TerminalSequence(len("-suffix")) {
		t.Fatalf("crash recovery reset continuity: %+v, %v", snapshot, err)
	}
	if !bytes.Contains(snapshot.Data, []byte("durable-screen-suffix")) {
		t.Fatalf("crash recovery lost current screen: %q", snapshot.Data)
	}
}

func TestMaintenancePrunesStoppedRecordingKinds(t *testing.T) {
	for _, key := range []SessionKey{RunSession("cold-run"), TerminalSession("member", "tab"), RunShellSession("cold-run", "shell")} {
		t.Run(string(key), func(t *testing.T) {
			h, _ := newTestHost(t)
			for _, text := range []string{"old\r\n", "LATEST"} {
				if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
					t.Fatal(err)
				}
				h.lookup(key).deliver([]byte(text))
				if err := h.StopSession(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			}
			path := h.transcriptPath(key)
			archives, err := priorCastPaths(path)
			if err != nil || len(archives) != 1 {
				t.Fatalf("archives = %v, %v", archives, err)
			}
			checkpoint, err := decodeCheckpoint(checkpointPath(path))
			if err != nil {
				t.Fatal(err)
			}
			if run, isRun := key.Run(); isRun {
				snapshot, snapshotErr := h.Snapshot(run)
				if snapshotErr != nil || snapshot.TruncatedBefore {
					t.Fatalf("complete cold screen = %+v, %v", snapshot, snapshotErr)
				}
			}
			old := time.Now().Add(-castRetainedAge - time.Hour)
			for _, file := range []string{archives[0], path} {
				if err = os.Chtimes(file, old, old); err != nil {
					t.Fatal(err)
				}
			}
			if err = h.PruneTranscripts(t.Context()); err != nil {
				t.Fatal(err)
			}
			if run, isRun := key.Run(); isRun {
				snapshot, snapshotErr := h.Snapshot(run)
				if snapshotErr != nil || !snapshot.TruncatedBefore || !bytes.Contains(snapshot.Data, []byte("LATEST")) {
					t.Fatalf("cached screen hid newly expired history: %+v, %v", snapshot, snapshotErr)
				}
			}
			if _, err = os.Stat(archives[0]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stopped archive survived maintenance: %v", err)
			}
			recovered, _, position, _, err := loadCurrentCheckpoint(path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.screen.dispose()
			if position.Epoch != checkpoint.Epoch || position.Sequence != checkpoint.Sequence {
				t.Fatalf("maintenance changed absolute position: %+v", position)
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatalf("latest segment lost: %v", err)
			}
		})
	}
}

func TestMaintenanceProtectsActiveAndStartingRecordings(t *testing.T) {
	h, _ := newTestHost(t)
	key := TerminalSession("member", "active")
	for range 2 {
		if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
			t.Fatal(err)
		}
		h.lookup(key).deliver([]byte("screen"))
		if err := h.StopSession(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	path := h.transcriptPath(key)
	archives, err := priorCastPaths(path)
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives = %v, %v", archives, err)
	}
	old := time.Now().Add(-castRetainedAge - time.Hour)
	if err = os.Chtimes(archives[0], old, old); err != nil {
		t.Fatal(err)
	}
	if err = h.reserve(key); err != nil {
		t.Fatal(err)
	}
	if err = h.PruneTranscripts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(archives[0]); err != nil {
		t.Fatalf("starting recording was pruned: %v", err)
	}
	h.unreserve(key)
	now := time.Now()
	if err = os.Chtimes(archives[0], now, now); err != nil {
		t.Fatal(err)
	}
	if err = h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	s := h.lookup(key)
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	if err = os.Chtimes(archives[0], old, old); err != nil {
		t.Fatal(err)
	}
	if err = h.PruneTranscripts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(archives[0]); err != nil {
		t.Fatalf("active recording was pruned: %v", err)
	}
}
