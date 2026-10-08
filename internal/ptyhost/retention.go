package ptyhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	castSegmentBytes     = 16 << 20
	castRetainedBytes    = 128 << 20
	castRetainedSegments = 8
	castRetainedAge      = 7 * 24 * time.Hour
)

// Before is an archive filename order, not a terminal sequence. Publishing it
// before unlinking makes interrupted pruning recoverable without invalidating
// the compact screen. OutputBytes accounts for the expired absolute prefix.
type castRetention struct {
	Before      int64  `json:"before"`
	OutputBytes uint64 `json:"output_bytes"`
}

func castRetentionPath(path string) string {
	return strings.TrimSuffix(path, ".cast") + ".retention"
}

func readCastRetention(path string) (castRetention, error) {
	f, err := os.Open(castRetentionPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return castRetention{}, nil
	}
	if err != nil {
		return castRetention{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return castRetention{}, err
	}
	var retention castRetention
	if len(data) > 1024 || json.Unmarshal(data, &retention) != nil || retention.Before <= 0 {
		return castRetention{}, errors.New("ptyhost: invalid transcript retention metadata")
	}
	return retention, nil
}

func (r castRetention) expired(path, name string) bool {
	order, archive := historySegmentOrder(path, name)
	return archive && r.Before != 0 && order < r.Before
}

func writeCastRetention(path string, retention castRetention) error {
	data, err := json.Marshal(retention)
	if err != nil {
		return err
	}
	tmp, err := os.OpenFile(castRetentionPath(path)+".next", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), castRetentionPath(path)); err != nil {
		return err
	}
	return syncHistoryCursorKeyDirectory(filepath.Dir(path))
}

// maintainTranscriptLocked runs at complete session output boundaries. Lock
// order is session -> lifecycle -> writer; no lifecycle holder acquires the
// session lock. Captures from an older incarnation are superseded, never allowed
// to publish a checkpoint referring to files that rotation has retired.
func (s *session) maintainTranscriptLocked() {
	if s.tr == nil || s.screen == nil {
		return
	}
	s.tr.mu.Lock()
	rotate := s.tr.logicalBytes >= castSegmentBytes
	s.tr.mu.Unlock()
	if !rotate {
		return
	}
	if err := s.tr.rotate(s.screenSnapshotLocked()); err != nil {
		s.checkpointErr = err
	}
}

func (w *castWriter) rotate(snap ScreenSnapshot) error {
	lifecycle := acquireTranscriptLifecycle(w.path)
	lifecycle.entry.mu.Lock()
	defer func() {
		lifecycle.entry.mu.Unlock()
		lifecycle.release()
	}()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.logicalBytes < castSegmentBytes {
		return nil
	}
	// A compact seed includes pending decoder bytes. Record those bytes before
	// sealing so the seed and the next output agree on the exact raw boundary.
	if len(w.pending) > 0 {
		w.eventLocked("o", w.pending)
		w.outputBytes += len(w.pending)
		w.pending = nil
	}
	if err := w.flushStagedLocked(); err != nil {
		return err
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	if w.history == nil {
		var err error
		w.history, err = priorCastSegments(w.path)
		if err != nil {
			return err
		}
	}
	old := castSegment{path: w.path, fileBytes: w.logicalBytes, outputBytes: w.outputBytes, incarnation: w.incarnation}
	// Publish a usable screen before changing any filenames. Recovery can
	// relocate this final boundary if the process stops during the rotation.
	checkpoint, err := checkpointFromSnapshot(snap, append(w.history, old))
	if err != nil {
		return err
	}
	checkpoint.PrunedOutputBytes = w.retention.OutputBytes
	checkpoint.CastOutputBytes += checkpoint.PrunedOutputBytes
	if err = writeCheckpointFile(checkpointPath(w.path), checkpoint); err != nil {
		return err
	}
	now := time.Now()
	incarnation := now.UnixNano()
	if incarnation <= w.incarnation {
		incarnation = w.incarnation + 1
	}
	header, err := json.Marshal(castHeader{Version: 2, Width: snap.Cols, Height: snap.Rows, Timestamp: now.Unix(), Incarnation: incarnation, Env: map[string]string{"TERM": "xterm-256color"}})
	if err != nil {
		return err
	}
	tmp, err := os.OpenFile(w.path+".next", os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	line := append(header, '\n')
	seed := castLine(now, "s", snap.Data)
	if _, err = tmp.Write(line); err == nil {
		_, err = tmp.Write(seed)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = syncHistoryCursorKeyDirectory(filepath.Dir(w.path))
	}
	if err != nil {
		_ = tmp.Close()
		return err
	}
	old.path = filepath.Join(filepath.Dir(w.path), stableCastSegmentName(w.path, old.incarnation))
	if err = os.Rename(w.path, old.path); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = os.Rename(tmp.Name(), w.path); err != nil {
		_ = tmp.Close()
		return errors.Join(err, os.Rename(old.path, w.path))
	}
	_ = w.f.Close()
	w.f, w.bw = tmp, bufio.NewWriterSize(tmp, 32*1024)
	w.start, w.incarnation = now, incarnation
	w.logicalBytes, w.outputBytes = int64(len(line)+len(seed)), 0
	w.history = append(w.history, old)
	// Sealed age starts at rotation, not the last output of an idle segment.
	if err = os.Chtimes(old.path, now, now); err != nil {
		return err
	}
	if err = w.persistRotatedCheckpointLocked(snap); err != nil {
		return err
	}
	return w.pruneLocked(now)
}

func (w *castWriter) persistRotatedCheckpointLocked(snap ScreenSnapshot) error {
	segments := append(w.history, castSegment{path: w.path, fileBytes: w.logicalBytes, outputBytes: w.outputBytes, incarnation: w.incarnation})
	checkpoint, err := checkpointFromSnapshot(snap, segments)
	if err != nil {
		return err
	}
	checkpoint.PrunedOutputBytes = w.retention.OutputBytes
	checkpoint.CastOutputBytes += checkpoint.PrunedOutputBytes
	return writeCheckpointFile(checkpointPath(w.path), checkpoint)
}

// pruneLocked only expires a prefix and always keeps the current segment. The
// caller has durably published its compact screen and holds lifecycle + writer.
func (w *castWriter) pruneLocked(now time.Time) error {
	// A previous unlink may have failed after publication. The marker remains
	// the durable retry key; never make hidden expired files undiscoverable.
	if w.retention.Before != 0 {
		paths, err := removablePriorCastPaths(context.Background(), w.path)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if w.retention.expired(w.path, filepath.Base(path)) {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
	}
	total := w.logicalBytes
	for _, segment := range w.history {
		total += segment.fileBytes
	}
	remove := 0
	retention := w.retention
	for i, segment := range w.history {
		info, err := os.Stat(segment.path)
		if err != nil {
			return err
		}
		if len(w.history)-i+1 <= castRetainedSegments && total <= castRetainedBytes && now.Sub(info.ModTime()) <= castRetainedAge {
			break
		}
		remove++
		total -= segment.fileBytes
		retention.OutputBytes += uint64(segment.outputBytes)
	}
	if remove == 0 {
		return nil
	}
	retention.Before = w.incarnation
	if remove < len(w.history) {
		var ok bool
		retention.Before, ok = historySegmentOrder(w.path, filepath.Base(w.history[remove].path))
		if !ok {
			return fmt.Errorf("ptyhost: invalid retained transcript segment")
		}
	}
	if err := writeCastRetention(w.path, retention); err != nil {
		return err
	}
	removed := w.history[:remove]
	w.history = append([]castSegment(nil), w.history[remove:]...)
	w.retention = retention
	var err error
	for _, segment := range removed {
		if removeErr := os.Remove(segment.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

// Cold maintenance repairs the latest screen before expiring any prefix.
// The caller holds the lifecycle write lock through replay descriptor pinning.
func pruneColdCast(path string, now time.Time) error {
	checkpoint, err := decodeCheckpoint(checkpointPath(path))
	var segments []castSegment
	if err == nil && checkpoint.Version == screenCheckpointVersion {
		segments, err = validateCheckpointSegments(path, checkpoint, true)
	}
	if err != nil || checkpoint.Version != screenCheckpointVersion {
		if _, err = repairColdSnapshotLocked(path); err != nil {
			return err
		}
		checkpoint, err = decodeCheckpoint(checkpointPath(path))
		if err != nil {
			return err
		}
		segments, err = validateCheckpointSegments(path, checkpoint, true)
		if err != nil {
			return err
		}
	}
	retention, err := readCastRetention(path)
	if err != nil {
		return err
	}
	current := segments[len(segments)-1]
	w := castWriter{
		path: path, incarnation: current.incarnation, logicalBytes: current.fileBytes,
		history: segments[:len(segments)-1], retention: retention,
	}
	return w.pruneLocked(now)
}

// PruneTranscripts is called by the scheduler's existing startup/hourly
// maintenance. It includes stopped run, member-terminal and run-shell stems.
func (h *Host) PruneTranscripts(ctx context.Context) error {
	entries, err := os.ReadDir(h.cfg.TranscriptDir)
	if err != nil {
		return err
	}
	paths := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".cast") {
			paths[filepath.Join(h.cfg.TranscriptDir, entry.Name())] = struct{}{}
		}
	}
	var result error
	for path := range paths {
		if err = ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		stem := strings.TrimSuffix(filepath.Base(path), ".cast")
		// Stable archives contain '~', which is not a valid owned stem.
		if strings.Contains(stem, "~") {
			continue
		}
		if dot := strings.LastIndexByte(stem, '.'); dot >= 0 {
			root := filepath.Join(h.cfg.TranscriptDir, stem[:dot]+".cast")
			if _, exists := paths[root]; exists {
				if order, legacy := legacyCastSegmentIncarnation(root, filepath.Base(path)); legacy && legacyCastArchive(path, order, currentCastStart(root)) {
					continue
				}
			}
		}
		if pruneErr := h.pruneStoppedTranscript(path, time.Now()); pruneErr != nil && !errors.Is(pruneErr, os.ErrNotExist) {
			result = errors.Join(result, pruneErr)
		}
	}
	return result
}

func (h *Host) pruneStoppedTranscript(path string, now time.Time) error {
	h.mu.Lock()
	var observed *session
	for key, s := range h.sessions {
		if h.transcriptPath(key) == path {
			observed = s
			break
		}
	}
	h.mu.Unlock()
	// Match the output lock order: session -> lifecycle. Do not prune while
	// final checkpoint publication or writer close is still outstanding.
	if observed != nil {
		observed.mu.Lock()
		defer observed.mu.Unlock()
		if !observed.stopped && !observed.ended {
			return nil
		}
		if observed.finishDone != nil {
			select {
			case <-observed.finishDone:
			default:
				return nil
			}
		}
	}
	lifecycle := acquireTranscriptLifecycle(path)
	lifecycle.entry.mu.Lock()
	defer func() {
		lifecycle.entry.mu.Unlock()
		lifecycle.release()
	}()
	h.mu.Lock()
	busy := false
	for key := range h.starting {
		if h.transcriptPath(key) == path {
			busy = true
		}
	}
	for key, s := range h.sessions {
		if h.transcriptPath(key) == path && s != observed {
			busy = true
		}
	}
	h.mu.Unlock()
	if busy {
		return nil
	}
	return pruneColdCast(path, now)
}

// A crash between the two rotation renames leaves an owned .next file. Its
// complete-event seed was synced before the old current file was renamed.
func recoverCastRotation(path string) error {
	lifecycle := acquireTranscriptLifecycle(path)
	lifecycle.entry.mu.Lock()
	defer func() {
		lifecycle.entry.mu.Unlock()
		lifecycle.release()
	}()
	next := path + ".next"
	if _, err := os.Stat(path); err == nil {
		if err = os.Remove(next); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := inspectCastHeader(next); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Rename(next, path); err != nil {
		return err
	}
	return syncHistoryCursorKeyDirectory(filepath.Dir(path))
}
