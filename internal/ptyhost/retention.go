package ptyhost

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const castSegmentBytes = 16 << 20

// castRetention reads the frontier left by older releases that expired history.
// Before is an archive filename order, not a terminal sequence. OutputBytes
// preserves absolute accounting for the already-expired prefix.
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
	return w.persistRotatedCheckpointLocked(snap)
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
