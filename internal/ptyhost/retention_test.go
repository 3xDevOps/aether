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

func TestCastRotationPreservesHistoryBeyondFormerLimits(t *testing.T) {
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
	originalCursor := page.NextCursor
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
	}
	if position := s.ring.position(); position.Epoch != before.Epoch || position.Sequence != sequence {
		t.Fatalf("rotation reset the live position: %+v, expected %s/%d", position, before.Epoch, sequence)
	}
	if err = stale.persist(); !errors.Is(err, errCheckpointSuperseded) {
		t.Fatalf("old capture could overwrite the rotated checkpoint: %v", err)
	}
	// A reader opened before rotation still has its original byte fence.
	got, err := io.ReadAll(pinned.Reader)
	if err != nil || !bytes.Equal(got, first) || pinned.TruncatedBefore {
		t.Fatalf("pinned replay changed during rotation: %q, %+v, %v", got, pinned, err)
	}
	if older, historyErr := h.History(t.Context(), run, page.NextCursor, "", 1); historyErr != nil || strings.Join(historyTexts(older.Lines), ",") != "second" {
		t.Fatalf("original cursor lost history: %+v, %v", older, historyErr)
	}
	forged, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	forged[len(forged)-1] ^= 1
	if _, err = h.History(t.Context(), run, base64.RawURLEncoding.EncodeToString(forged), "", 1); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("forged cursor = %v", err)
	}
	s.deliver([]byte("CURRENT"))
	sequence += TerminalSequence(len("CURRENT"))
	live = append(live, "CURRENT"...)
	if tapped := <-tapDone; tapped.err != nil || !bytes.Equal(tapped.data, live) {
		t.Fatalf("live tap reset or lost bytes across rotation: %q, %v", tapped.data, tapped.err)
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
	if readErr != nil || !bytes.Equal(got, live) || window.Bytes != len(live) {
		t.Fatalf("recorded output lost byte fidelity: %q, want %q, %v", got, live, readErr)
	}
	if window.TruncatedBefore || !window.Complete || window.Position.Sequence != sequence || window.Position.Epoch != before.Epoch {
		t.Fatalf("replay metadata = %+v", window)
	}
	page, err = h.History(t.Context(), run, "", "", 2)
	if err != nil || page.TruncatedBefore {
		t.Fatalf("history page metadata = %+v, %v", page, err)
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
	if len(segments) <= 8 || disk <= 128<<20 {
		t.Fatalf("test did not cross former limits: %d segments, %d bytes", len(segments), disk)
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
	if err != nil || snapshot.TruncatedBefore || snapshot.Position != window.Position || !bytes.Contains(snapshot.Data, []byte("CURRENT")) {
		t.Fatalf("latest compact screen did not survive restart: %+v, %v", snapshot, err)
	}
	reopened, err := restarted.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reopened.Reader)
	_ = reopened.Reader.Close()
	if readErr != nil || !bytes.Equal(data, live) || reopened.Position != window.Position || reopened.TruncatedBefore {
		t.Fatalf("reopened recording lost history: %q, %+v, %v", data, reopened, readErr)
	}
	if older, historyErr := restarted.History(t.Context(), run, originalCursor, "", 1); historyErr != nil || older.TruncatedBefore || strings.Join(historyTexts(older.Lines), ",") != "second" {
		t.Fatalf("reopened history = %+v, %v", older, historyErr)
	}
	if err = restarted.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	restarted.lookup(key).deliver([]byte("-resumed"))
	resumed, err := restarted.Snapshot(run)
	if err != nil || resumed.TruncatedBefore || resumed.Position.Epoch != before.Epoch || resumed.Position.Sequence != sequence+TerminalSequence(len("-resumed")) {
		t.Fatalf("restart did not continue absolute position: %+v, %v", resumed, err)
	}
}

func TestCastAgeDoesNotDeleteHistoryAndExplicitDeletionStopsReplay(t *testing.T) {
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
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, file := range []string{archives[0], path} {
		if err = os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// History remains readable concurrently with checkpoint publication.
	readDone := make(chan error, 1)
	go func() {
		_, readErr := h.History(context.Background(), run, cursor.NextCursor, "", 1)
		readDone <- readErr
	}()
	if err = s.checkpointNow(); err != nil {
		t.Fatal(err)
	}
	if readErr := <-readDone; readErr != nil {
		t.Fatalf("checkpoint invalidated history: %v", readErr)
	}
	if _, err = os.Stat(archives[0]); err != nil {
		t.Fatalf("old archive was removed: %v", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("old current segment was removed: %v", err)
	}
	if err = h.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	recovered, _, position, _, err := loadCurrentCheckpoint(path, false)
	if err != nil {
		t.Fatalf("valid checkpoint lost its history: %v", err)
	}
	if !strings.Contains(recovered.screen.term.String(), "ANCHOR") || !strings.Contains(recovered.screen.term.String(), "current") {
		t.Fatalf("screen lost earlier state: %q", recovered.screen.term.String())
	}
	recovered.screen.dispose()
	window, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = window.Reader.Close() }()
	if window.TruncatedBefore || !window.Complete || window.Position != position {
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
	if !errors.Is(err, os.ErrNotExist) || len(data) != 0 {
		t.Fatalf("replay silently skipped explicitly deleted history: %q, %v", data, err)
	}
	for _, file := range []string{archives[0], path, checkpointPath(path), castRetentionPath(path), h.ItemLogPath(run) + ".compact"} {
		if _, err = os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("run deletion left %s: %v", filepath.Base(file), err)
		}
	}
}

func TestCastExistingFrontierPreservesPositionAndRemainingHistory(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprintf("prefix-removed-%t", removed), func(t *testing.T) {
			h, dir := newTestHost(t)
			run := domain.RunID("old-frontier")
			key := RunSession(run)
			var cursor string
			for _, output := range []string{"old\r\nolder\r\n", "latest\r\nremaining\r\n"} {
				if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
					t.Fatal(err)
				}
				h.lookup(key).deliver([]byte(output))
				if cursor == "" {
					page, err := h.History(t.Context(), run, "", "", 1)
					if err != nil || page.NextCursor == "" {
						t.Fatalf("initial history = %+v, %v", page, err)
					}
					cursor = page.NextCursor
				}
				if err := h.StopSession(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, string(run)+".cast")
			checkpoint, err := decodeCheckpoint(checkpointPath(path))
			if err != nil {
				t.Fatal(err)
			}
			// An old v2 checkpoint still lists both files at publication.
			segments, err := collectFullCastSegments(path)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint.Version = 2
			checkpoint.Segments = nil
			for _, segment := range segments {
				checkpoint.Segments = append(checkpoint.Segments, checkpointSegment{
					Path: filepath.Base(segment.path), Incarnation: segment.incarnation,
					FileBytes: segment.fileBytes, OutputBytes: segment.outputBytes,
				})
			}
			if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
				t.Fatal(err)
			}
			old := checkpoint.Segments[0]
			// Cover both completed old pruning and interruption after publication.
			frontier, err := json.Marshal(castRetention{Before: checkpoint.Incarnation, OutputBytes: uint64(old.OutputBytes)})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(castRetentionPath(path), frontier, 0o600); err != nil {
				t.Fatal(err)
			}
			oldPath := filepath.Join(dir, old.Path)
			if removed {
				if err = os.Remove(oldPath); err != nil {
					t.Fatal(err)
				}
			}
			if err = h.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(Config{TranscriptDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			window, err := reopened.Replay(run)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(window.Reader)
			_ = window.Reader.Close()
			position := TerminalPosition{Epoch: checkpoint.Epoch, Sequence: checkpoint.Sequence}
			if readErr != nil || string(data) != "latest\r\nremaining\r\n" || !window.TruncatedBefore || !window.Complete || window.Position != position {
				t.Fatalf("existing frontier replay = %q, %+v, %v", data, window, readErr)
			}
			page, err := reopened.History(t.Context(), run, "", "", 10)
			if err != nil || !page.TruncatedBefore || strings.Join(historyTexts(page.Lines), ",") != "latest,remaining" {
				t.Fatalf("remaining history = %+v, %v", page, err)
			}
			if _, err = reopened.History(t.Context(), run, cursor, "", 1); !errors.Is(err, ErrHistoryCursorExpired) {
				t.Fatalf("old authenticated cursor = %v", err)
			}
			if !removed {
				if _, err = os.Stat(oldPath); err != nil {
					t.Fatalf("reading old frontier deleted more data: %v", err)
				}
			}
			if err = reopened.StartSession(t.Context(), key, newFakeAtt()); err != nil {
				t.Fatal(err)
			}
			reopened.lookup(key).deliver([]byte("resumed"))
			if err = reopened.lookup(key).checkpointNow(); err != nil {
				t.Fatal(err)
			}
			snapshot, err := reopened.Snapshot(run)
			if err != nil || !snapshot.TruncatedBefore || snapshot.Position.Epoch != position.Epoch || snapshot.Position.Sequence != position.Sequence+TerminalSequence(len("resumed")) || !bytes.Contains(snapshot.Data, []byte("resumed")) {
				t.Fatalf("frontier restart reset state: %+v, %v", snapshot, err)
			}
			persisted, err := os.ReadFile(castRetentionPath(path))
			if err != nil || !bytes.Equal(persisted, frontier) {
				t.Fatalf("frontier was changed: %q, %v", persisted, err)
			}
			if err = reopened.StopSession(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			if err = reopened.RemoveRunTranscripts(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{oldPath, path, checkpointPath(path), castRetentionPath(path)} {
				if _, err = os.Stat(file); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("explicit deletion left %s: %v", file, err)
				}
			}
		})
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

func TestCastOldRecordingKindsSurviveReopen(t *testing.T) {
	for _, key := range []SessionKey{RunSession("cold-run"), TerminalSession("member", "tab"), RunShellSession("cold-run", "shell")} {
		t.Run(string(key), func(t *testing.T) {
			h, dir := newTestHost(t)
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
			old := time.Now().Add(-8 * 24 * time.Hour)
			for _, file := range []string{archives[0], path} {
				if err = os.Chtimes(file, old, old); err != nil {
					t.Fatal(err)
				}
			}
			if err = h.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(Config{TranscriptDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			snapshot, err := repairColdSnapshot(path)
			if err != nil || snapshot.Position.Epoch != checkpoint.Epoch || snapshot.Position.Sequence != checkpoint.Sequence {
				t.Fatalf("cold repair changed absolute position: %+v, %v", snapshot, err)
			}
			reader, _, err := openFullCastReplay(path)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil || string(data) != "old\r\nLATEST" {
				t.Fatalf("old recording lost history: %q, %v", data, readErr)
			}
			if err = reopened.StartSession(t.Context(), key, newFakeAtt()); err != nil {
				t.Fatal(err)
			}
			reopened.lookup(key).deliver([]byte("-resumed"))
			if err = reopened.lookup(key).checkpointNow(); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(archives[0]); err != nil {
				t.Fatalf("restart removed old archive: %v", err)
			}
		})
	}
}

func TestCastReplayKeepsFiniteWindowAcrossRestart(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("finite-replay")
	key := RunSession(run)
	for _, text := range []string{"sealed\r\n", "active"} {
		if err := h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
			t.Fatal(err)
		}
		h.lookup(key).deliver([]byte(text))
		if err := h.StopSession(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	window, err := h.Replay(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = window.Reader.Close() }()
	if err = h.StartSession(t.Context(), key, newFakeAtt()); err != nil {
		t.Fatal(err)
	}
	h.lookup(key).deliver([]byte("-outside-window"))
	data, err := io.ReadAll(window.Reader)
	if err != nil || string(data) != "sealed\r\nactive" || len(data) != window.Bytes {
		t.Fatalf("replay changed across restart: %q, %+v, %v", data, window, err)
	}
	if err = window.Reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err = window.Reader.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
}
