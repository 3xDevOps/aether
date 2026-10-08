package ptyhost

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	screenCheckpointVersion  = 3
	maxScreenCheckpointBytes = 8 << 20
)

type checkpointSegment struct {
	Path        string `json:"path"`
	Incarnation int64  `json:"incarnation"`
	FileBytes   int64  `json:"file_bytes"`
	OutputBytes int    `json:"output_bytes"`
}

type screenCheckpoint struct {
	Version           int              `json:"version"`
	Epoch             TerminalEpoch    `json:"epoch,omitempty"`
	Sequence          TerminalSequence `json:"sequence,omitempty"`
	CastOutputBytes   uint64           `json:"cast_output_bytes,omitempty"`
	PrunedOutputBytes uint64           `json:"pruned_output_bytes,omitempty"`
	Incarnation       int64            `json:"incarnation"`
	CastOffset        int64            `json:"cast_offset"`
	Cols              uint             `json:"cols"`
	Rows              uint             `json:"rows"`
	Data              []byte           `json:"data"`
	// Version 3 stores only the boundary segment; the retained lineage count
	// and absolute output aggregate replace the unbounded v1/v2 segment list.
	SegmentCount int                 `json:"segment_count,omitempty"`
	Segments     []checkpointSegment `json:"segments,omitempty"`
}

func (s *session) captureCheckpointLocked() (*checkpointCapture, error) {
	if s.tr == nil || s.screen == nil {
		return nil, nil
	}
	if s.checkpoint == "" {
		return nil, nil
	}
	snapshot := s.screenSnapshotLocked()
	capture, err := s.tr.captureCheckpoint(s.checkpoint, snapshot)
	if err != nil {
		s.checkpointErr = err
		return nil, err
	}
	s.checkpointSeq++
	capture.seq = s.checkpointSeq
	return &capture, nil
}

func (s *session) persistCheckpoint(capture *checkpointCapture) error {
	if capture == nil {
		return nil
	}
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	s.mu.Lock()
	stale := capture.seq < s.checkpointSeq
	s.mu.Unlock()
	if stale {
		capture.boundary.release()
		return nil
	}
	err := capture.persist()
	s.mu.Lock()
	if capture.seq == s.checkpointSeq {
		s.checkpointErr = err
	}
	s.mu.Unlock()
	// Publication is best-effort. The in-memory screen remains authoritative,
	// and a persistence fault must not fail StopSession or a live attach.
	return nil
}
func (s *session) purgeSnapshot() {
	s.mu.Lock()
	s.finalSnapshot = ScreenSnapshot{}
	s.mu.Unlock()
}
func (s *session) snapshot() (ScreenSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.finalSnapshot.Data) > 0 {
		return cloneScreenSnapshot(s.finalSnapshot), nil
	}
	if s.screen == nil {
		return ScreenSnapshot{}, ErrSnapshotUnavailable
	}
	return s.screenSnapshotLocked(), nil
}

func (s *session) checkpointNow() error {
	s.mu.Lock()
	s.maintainTranscriptLocked()
	capture, err := s.captureCheckpointLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.persistCheckpoint(capture)
}

func (s *session) checkpointLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = s.checkpointNow()
		case <-s.done:
			return
		}
	}
}

func checkpointPath(transcript string) string {
	return strings.TrimSuffix(transcript, ".cast") + ".screen"
}

type castBoundary struct {
	writer  *castWriter
	segment castSegment
}

func (b *castBoundary) release() {
	if b.writer == nil {
		return
	}
	b.writer = nil
}

func (b *castBoundary) flush() error {
	if b.writer == nil {
		return nil
	}
	b.writer.mu.Lock()
	if b.writer.incarnation != b.segment.incarnation || b.writer.closed {
		b.writer.mu.Unlock()
		return errCheckpointSuperseded
	}
	err := b.writer.flushStagedLocked()
	if err == nil {
		err = b.writer.bw.Flush()
	}
	b.writer.mu.Unlock()
	return err
}

func (b *castBoundary) sync() error {
	if b.writer == nil {
		return nil
	}
	b.writer.mu.Lock()
	defer b.writer.mu.Unlock()
	if b.writer.incarnation != b.segment.incarnation || b.writer.closed {
		return errCheckpointSuperseded
	}
	return b.writer.f.Sync()
}

type checkpointCapture struct {
	path              string
	snapshot          ScreenSnapshot
	segments          []checkpointSegment
	segmentCount      int
	castOutputBytes   uint64
	prunedOutputBytes uint64
	boundary          castBoundary
	seq               uint64
}

func (c *checkpointCapture) persist() error {
	lifecycle := acquireTranscriptLifecycle(c.boundary.segment.path)
	lifecycle.entry.mu.Lock()
	defer func() {
		c.boundary.release()
		lifecycle.entry.mu.Unlock()
		lifecycle.release()
	}()
	if err := c.boundary.flush(); err != nil {
		c.boundary.release()
		return fmt.Errorf("ptyhost: flush checkpoint transcript: %w", err)
	}
	if err := c.boundary.sync(); err != nil {
		return fmt.Errorf("ptyhost: sync checkpoint transcript: %w", err)
	}
	return writeCheckpointFile(c.path, screenCheckpoint{
		Version:           screenCheckpointVersion,
		Epoch:             c.snapshot.Position.Epoch,
		Sequence:          c.snapshot.Position.Sequence,
		CastOutputBytes:   c.castOutputBytes,
		PrunedOutputBytes: c.prunedOutputBytes,
		Incarnation:       c.boundary.segment.incarnation,
		CastOffset:        c.boundary.segment.fileBytes,
		Cols:              c.snapshot.Cols,
		Rows:              c.snapshot.Rows,
		Data:              c.snapshot.Data,
		Segments:          c.segments,
		SegmentCount:      c.segmentCount,
	})
}

// captureCheckpoint records the logical cast boundary represented by snap.
// Pending UTF-8 is made an event under the short writer lock, while flushing
// the buffered prefix and syncing the file are deferred until persistence.
func (w *castWriter) captureCheckpoint(path string, snap ScreenSnapshot) (checkpointCapture, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return checkpointCapture{}, errors.New("ptyhost: checkpoint transcript is closed")
	}
	if err := w.loadPrefixLocked(); err != nil {
		w.mu.Unlock()
		return checkpointCapture{}, err
	}
	// A sticky write error must not turn pending tails into an unbounded queue.
	// An empty write checks it without flushing the buffer.
	if _, err := w.bw.Write(nil); err != nil {
		w.mu.Unlock()
		return checkpointCapture{}, fmt.Errorf("ptyhost: checkpoint transcript: %w", err)
	}
	if len(w.pending) > 0 {
		pending := w.pending
		w.pending = nil
		line := castLine(w.start, "o", pending)
		if len(w.staged) == 0 {
			w.staged = line
		} else {
			w.staged = append(w.staged, line...)
		}
		w.logicalBytes += int64(len(line))
		w.outputBytes += len(pending)
	}
	boundary := castSegment{
		path: w.path, fileBytes: w.logicalBytes,
		outputBytes: w.outputBytes, incarnation: w.incarnation,
	}

	if ^uint64(0)-w.prefixOutputBytes < uint64(boundary.outputBytes) {
		w.mu.Unlock()
		return checkpointCapture{}, errors.New("ptyhost: checkpoint output boundary overflow")
	}
	castOutputBytes := w.prefixOutputBytes + uint64(boundary.outputBytes)
	segments := []checkpointSegment{{
		Path: filepath.Base(boundary.path), Incarnation: boundary.incarnation,
		FileBytes: boundary.fileBytes, OutputBytes: boundary.outputBytes,
	}}
	segmentCount := w.prefixSegments + 1
	prunedOutputBytes := w.retention.OutputBytes
	w.mu.Unlock()
	return checkpointCapture{
		path: path, snapshot: cloneScreenSnapshot(snap), segments: segments,
		segmentCount:      segmentCount,
		castOutputBytes:   castOutputBytes,
		prunedOutputBytes: prunedOutputBytes,
		boundary:          castBoundary{writer: w, segment: boundary},
	}, nil
}

var checkpointFileMu sync.Mutex

var errCheckpointSuperseded = errors.New("ptyhost: screen checkpoint superseded")

type checkpointFileIdentity struct {
	exists bool
	sum    [sha256.Size]byte
}

func readCheckpointIdentity(path string) (checkpointFileIdentity, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkpointFileIdentity{}, nil
	}
	if err != nil {
		return checkpointFileIdentity{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return checkpointFileIdentity{}, err
	}
	if info.Size() < 0 || info.Size() > maxScreenCheckpointBytes {
		return checkpointFileIdentity{}, errors.New("ptyhost: invalid screen checkpoint size")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxScreenCheckpointBytes+1))
	if err != nil {
		return checkpointFileIdentity{}, err
	}
	return checkpointFileIdentity{exists: true, sum: sha256.Sum256(data)}, nil
}

func writeCheckpointFile(path string, checkpoint screenCheckpoint) error {
	checkpointFileMu.Lock()
	defer checkpointFileMu.Unlock()
	return writeCheckpointFileLocked(path, checkpoint, nil)
}

func writeCheckpointFileIfUnchanged(path string, checkpoint screenCheckpoint, expected checkpointFileIdentity) error {
	checkpointFileMu.Lock()
	defer checkpointFileMu.Unlock()
	return writeCheckpointFileLocked(path, checkpoint, &expected)
}

func writeCheckpointFileLocked(path string, checkpoint screenCheckpoint, expected *checkpointFileIdentity) error {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("ptyhost: encode screen checkpoint: %w", err)
	}
	if len(encoded) > maxScreenCheckpointBytes {
		return fmt.Errorf("ptyhost: screen checkpoint exceeds %d bytes", maxScreenCheckpointBytes)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".screen-checkpoint-*")
	if err != nil {
		return fmt.Errorf("ptyhost: create screen checkpoint: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, writeErr := tmp.Write(encoded); writeErr != nil {
		return fmt.Errorf("ptyhost: write screen checkpoint: %w", writeErr)
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		return fmt.Errorf("ptyhost: sync screen checkpoint: %w", syncErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return fmt.Errorf("ptyhost: close screen checkpoint: %w", closeErr)
	}
	if expected != nil {
		current, identityErr := readCheckpointIdentity(path)
		if identityErr != nil {
			return fmt.Errorf("ptyhost: inspect screen checkpoint generation: %w", identityErr)
		}
		if current != *expected {
			return errCheckpointSuperseded
		}
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("ptyhost: install screen checkpoint: %w", err)
	}
	// Windows cannot flush the read-only directory handle opened below.
	if runtime.GOOS == "windows" {
		return nil
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("ptyhost: open checkpoint directory: %w", err)
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil {
		return fmt.Errorf("ptyhost: sync checkpoint directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("ptyhost: close checkpoint directory: %w", closeErr)
	}
	return nil
}

func freshTerminalPosition() (TerminalPosition, error) {
	epoch, err := newTerminalEpoch()
	if err != nil {
		return TerminalPosition{}, err
	}
	return TerminalPosition{Epoch: epoch}, nil
}

func checkpointFromSnapshot(snap ScreenSnapshot, segments []castSegment) (screenCheckpoint, error) {
	if snap.Position.Epoch == "" {
		return screenCheckpoint{}, errors.New("ptyhost: screen checkpoint has no terminal epoch")
	}
	if len(segments) == 0 {
		return screenCheckpoint{}, errors.New("ptyhost: screen checkpoint has no segments")
	}
	var outputBytes uint64
	for _, segment := range segments {
		if segment.outputBytes < 0 {
			return screenCheckpoint{}, errors.New("ptyhost: unknown checkpoint output boundary")
		}
		if ^uint64(0)-outputBytes < uint64(segment.outputBytes) {
			return screenCheckpoint{}, errors.New("ptyhost: checkpoint output boundary overflow")
		}
		outputBytes += uint64(segment.outputBytes)
	}
	last := segments[len(segments)-1]
	return screenCheckpoint{
		Version: screenCheckpointVersion, Epoch: snap.Position.Epoch,
		Sequence: snap.Position.Sequence, CastOutputBytes: outputBytes,
		Incarnation: last.incarnation, CastOffset: last.fileBytes,
		Cols: snap.Cols, Rows: snap.Rows, Data: append([]byte(nil), snap.Data...),
		SegmentCount: len(segments),
		Segments: []checkpointSegment{{
			Path: filepath.Base(last.path), Incarnation: last.incarnation,
			FileBytes: last.fileBytes, OutputBytes: last.outputBytes,
		}},
	}, nil
}

func persistColdCheckpoint(transcript string, snap ScreenSnapshot, segments []castSegment, expected checkpointFileIdentity) error {
	checkpoint, err := checkpointFromSnapshot(snap, segments)
	if err != nil {
		return err
	}
	retention, err := readCastRetention(transcript)
	if err != nil {
		return err
	}
	checkpoint.PrunedOutputBytes = retention.OutputBytes
	checkpoint.CastOutputBytes += retention.OutputBytes
	return writeCheckpointFileIfUnchanged(checkpointPath(transcript), checkpoint, expected)
}

func decodeCheckpoint(path string) (screenCheckpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return screenCheckpoint{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return screenCheckpoint{}, err
	}
	if info.Size() <= 0 || info.Size() > maxScreenCheckpointBytes {
		return screenCheckpoint{}, errors.New("ptyhost: invalid screen checkpoint size")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxScreenCheckpointBytes+1))
	if err != nil {
		return screenCheckpoint{}, err
	}
	var checkpoint screenCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return screenCheckpoint{}, fmt.Errorf("ptyhost: decode screen checkpoint: %w", err)
	}
	if checkpoint.Version < 1 || checkpoint.Version > screenCheckpointVersion {
		return screenCheckpoint{}, fmt.Errorf("ptyhost: unsupported screen checkpoint version %d", checkpoint.Version)
	}
	if checkpoint.Incarnation == 0 || checkpoint.CastOffset <= 0 {
		return screenCheckpoint{}, errors.New("ptyhost: invalid screen checkpoint boundary")
	}
	if checkpoint.Version == 1 {
		if checkpoint.Epoch != "" || checkpoint.Sequence != 0 || checkpoint.CastOutputBytes != 0 {
			return screenCheckpoint{}, errors.New("ptyhost: inconsistent v1 screen checkpoint position")
		}
	} else if checkpoint.Epoch == "" {
		return screenCheckpoint{}, errors.New("ptyhost: screen checkpoint has no terminal epoch")
	}
	if err := validateScreenDimensions(checkpoint.Cols, checkpoint.Rows); err != nil {
		return screenCheckpoint{}, err
	}
	if len(checkpoint.Data) == 0 || len(checkpoint.Data) > maxScreenCheckpointBytes {
		return screenCheckpoint{}, errors.New("ptyhost: invalid screen checkpoint data")
	}
	return checkpoint, nil
}

// Compact checkpoints prove the current screen from one incarnation boundary.
// Older immutable files are discovered only for full replay or suffix repair,
// never by ordinary screen/recent reads or live checkpoint publication.
func validateCompactCheckpoint(transcript string, checkpoint screenCheckpoint, exact bool) ([]castSegment, error) {
	if len(checkpoint.Segments) != 1 || checkpoint.SegmentCount < 1 {
		return nil, errors.New("ptyhost: invalid compact checkpoint lineage")
	}
	raw := checkpoint.Segments[0]
	if raw.Path != filepath.Base(transcript) || raw.Incarnation != checkpoint.Incarnation ||
		raw.FileBytes != checkpoint.CastOffset || raw.OutputBytes < 0 {
		return nil, errors.New("ptyhost: invalid compact checkpoint boundary")
	}
	retention, err := readCastRetention(transcript)
	if err != nil {
		return nil, err
	}
	if checkpoint.PrunedOutputBytes != retention.OutputBytes || retention.expired(transcript, stableCastSegmentName(transcript, raw.Incarnation)) {
		return nil, errors.New("ptyhost: checkpoint retention boundary mismatch")
	}
	if checkpoint.CastOutputBytes < checkpoint.PrunedOutputBytes ||
		checkpoint.CastOutputBytes-checkpoint.PrunedOutputBytes < uint64(raw.OutputBytes) ||
		uint64(checkpoint.Sequence) > checkpoint.CastOutputBytes ||
		checkpoint.SegmentCount == 1 && checkpoint.CastOutputBytes-checkpoint.PrunedOutputBytes != uint64(raw.OutputBytes) {
		return nil, errors.New("ptyhost: inconsistent screen checkpoint position")
	}
	segment, err := inspectCastHeader(transcript)
	current := err == nil && segment.incarnation == raw.Incarnation
	if !current {
		if exact {
			return nil, errors.New("ptyhost: screen checkpoint incarnation is stale")
		}
		segment, err = inspectCastHeader(filepath.Join(filepath.Dir(transcript), stableCastSegmentName(transcript, raw.Incarnation)))
	}
	if err != nil {
		return nil, err
	}
	if segment.incarnation != raw.Incarnation {
		return nil, errors.New("ptyhost: checkpoint cast incarnation mismatch")
	}
	if segment.fileBytes < raw.FileBytes {
		return nil, errors.New("ptyhost: checkpoint cast was truncated")
	}
	if exact && segment.fileBytes != raw.FileBytes {
		return nil, errors.New("ptyhost: screen checkpoint is behind transcript")
	}
	segment.fileBytes, segment.outputBytes = raw.FileBytes, raw.OutputBytes
	segments := []castSegment{segment}
	if current {
		return segments, nil
	}
	paths, err := priorCastPaths(transcript)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(transcript); err == nil {
		paths = append(paths, transcript)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	index := -1
	for i, path := range paths {
		if path == segment.path {
			index = i
			break
		}
	}
	if index < 0 || index+1 != checkpoint.SegmentCount {
		return nil, errors.New("ptyhost: compact checkpoint lineage changed")
	}
	for _, path := range paths[index+1:] {
		next, err := inspectCastHeader(path)
		if err != nil {
			return nil, err
		}
		segments = append(segments, next)
	}
	return segments, nil
}

func validateCheckpointSegments(transcript string, checkpoint screenCheckpoint, exact bool) ([]castSegment, error) {
	if checkpoint.Version == screenCheckpointVersion {
		return validateCompactCheckpoint(transcript, checkpoint, exact)
	}
	if len(checkpoint.Segments) == 0 {
		return nil, errors.New("ptyhost: screen checkpoint has no segments")
	}
	retention, err := readCastRetention(transcript)
	if err != nil {
		return nil, err
	}
	if checkpoint.PrunedOutputBytes > retention.OutputBytes {
		return nil, errors.New("ptyhost: checkpoint retention boundary mismatch")
	}
	segments := make([]castSegment, 0, len(checkpoint.Segments))
	index := -1
	outputBytes := checkpoint.PrunedOutputBytes
	seenPaths := make(map[string]struct{}, len(checkpoint.Segments))
	for i, raw := range checkpoint.Segments {
		if raw.Path == "" || filepath.Base(raw.Path) != raw.Path || raw.Incarnation == 0 || raw.FileBytes <= 0 || raw.OutputBytes < 0 {
			return nil, errors.New("ptyhost: invalid checkpoint segment")
		}
		if _, duplicate := seenPaths[raw.Path]; duplicate {
			return nil, errors.New("ptyhost: duplicate checkpoint segment path")
		}
		seenPaths[raw.Path] = struct{}{}
		if ^uint64(0)-outputBytes < uint64(raw.OutputBytes) {
			return nil, errors.New("ptyhost: checkpoint output boundary overflow")
		}
		outputBytes += uint64(raw.OutputBytes)
		name := raw.Path
		if name == filepath.Base(transcript) {
			name = stableCastSegmentName(transcript, raw.Incarnation)
		}
		if retention.expired(transcript, name) {
			continue
		}
		path := filepath.Join(filepath.Dir(transcript), raw.Path)
		segment, segmentErr := inspectCastHeader(path)
		if raw.Path == filepath.Base(transcript) && (errors.Is(segmentErr, os.ErrNotExist) || segmentErr == nil && segment.incarnation != raw.Incarnation) {
			segment, segmentErr = inspectCastHeader(filepath.Join(filepath.Dir(transcript), name))
		}
		if segmentErr != nil {
			return nil, segmentErr
		}
		if segment.incarnation != raw.Incarnation {
			return nil, errors.New("ptyhost: checkpoint cast incarnation mismatch")
		}
		if segment.fileBytes < raw.FileBytes {
			return nil, errors.New("ptyhost: checkpoint cast was truncated")
		}
		if exact && segment.fileBytes != raw.FileBytes {
			return nil, errors.New("ptyhost: screen checkpoint is behind transcript")
		}
		segment.fileBytes = raw.FileBytes
		segment.outputBytes = raw.OutputBytes
		segments = append(segments, segment)
		if raw.Incarnation == checkpoint.Incarnation {
			if index >= 0 {
				return nil, errors.New("ptyhost: duplicate checkpoint incarnation")
			}
			index = i
		}
	}
	if index < 0 || index != len(checkpoint.Segments)-1 || checkpoint.CastOffset != checkpoint.Segments[index].FileBytes {
		return nil, errors.New("ptyhost: checkpoint boundary mismatch")
	}
	if checkpoint.Version >= 2 &&
		(checkpoint.CastOutputBytes != outputBytes || uint64(checkpoint.Sequence) > outputBytes) {
		return nil, errors.New("ptyhost: inconsistent screen checkpoint position")
	}
	paths, err := priorCastPaths(transcript)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(transcript); err == nil {
		paths = append(paths, transcript)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(paths) < len(segments) || exact && len(paths) != len(segments) {
		return nil, errors.New("ptyhost: screen checkpoint segment set is stale")
	}
	for i, segment := range segments {
		if filepath.Clean(paths[i]) != filepath.Clean(segment.path) {
			return nil, errors.New("ptyhost: screen checkpoint segment order mismatch")
		}
	}
	for _, path := range paths[len(segments):] {
		segment, err := inspectCastHeader(path)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func loadCurrentCheckpoint(transcript string, allowV1 bool) (recordedScreen, []castSegment, TerminalPosition, bool, error) {
	checkpoint, err := decodeCheckpoint(checkpointPath(transcript))
	if err != nil {
		return recordedScreen{}, nil, TerminalPosition{}, false, err
	}
	if checkpoint.Version == 1 && !allowV1 {
		return recordedScreen{}, nil, TerminalPosition{}, true, errors.New("ptyhost: v1 screen checkpoint requires migration")
	}
	segments, err := validateCheckpointSegments(transcript, checkpoint, true)
	if err != nil {
		return recordedScreen{}, nil, TerminalPosition{}, checkpoint.Version == 1, err
	}
	recovered, err := restoreCheckpointScreen(checkpoint)
	if err != nil {
		return recordedScreen{}, nil, TerminalPosition{}, checkpoint.Version == 1, err
	}
	position := TerminalPosition{Epoch: checkpoint.Epoch, Sequence: checkpoint.Sequence}
	if checkpoint.Version == 1 {
		position, err = freshTerminalPosition()
		if err != nil {
			recovered.screen.dispose()
			return recordedScreen{}, nil, TerminalPosition{}, true, err
		}
	}
	return recovered, segments, position, checkpoint.Version < screenCheckpointVersion, nil
}

var reconstructSnapshot = readCastScreen

func collectCastHeaders(transcript string) ([]castSegment, error) {
	paths, err := priorCastPaths(transcript)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(transcript); err == nil {
		paths = append(paths, transcript)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, os.ErrNotExist
	}
	segments := make([]castSegment, 0, len(paths))
	for _, path := range paths {
		segment, err := inspectCastHeader(path)
		if err != nil {
			return nil, err
		}
		if incarnation, stable := castSegmentIncarnation(transcript, filepath.Base(path)); stable && incarnation != segment.incarnation {
			return nil, errors.New("ptyhost: transcript segment identity mismatch")
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func collectFullCastSegments(transcript string) ([]castSegment, error) {
	segments, err := priorCastSegments(transcript)
	if err != nil {
		return nil, err
	}
	current, err := inspectCastSegment(transcript)
	if err != nil {
		return nil, err
	}
	return append(segments, current), nil
}

func sameCastBoundaries(left, right []castSegment) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if filepath.Clean(left[i].path) != filepath.Clean(right[i].path) ||
			left[i].fileBytes != right[i].fileBytes ||
			left[i].incarnation != right[i].incarnation {
			return false
		}
	}
	return true
}

func repairColdSnapshot(transcript string) (ScreenSnapshot, error) {
	lifecycle := acquireTranscriptLifecycle(transcript)
	lifecycle.entry.mu.RLock()
	defer func() {
		lifecycle.entry.mu.RUnlock()
		lifecycle.release()
	}()
	return repairColdSnapshotLocked(transcript)
}

// repairColdSnapshotLocked streams one segment at a time. Its caller holds
// the lifecycle lock, including callers preparing a bounded pinned replay.
func repairColdSnapshotLocked(transcript string) (ScreenSnapshot, error) {
	checkpointFile := checkpointPath(transcript)
	expected, err := readCheckpointIdentity(checkpointFile)
	if err != nil {
		return ScreenSnapshot{}, err
	}

	// Exact older checkpoints already describe an authoritative screen. v1
	// gets a fresh epoch; v2 retains its absolute position. Migrate either to
	// the compact boundary without decoding the recorded output again.
	if recovered, segments, position, legacy, loadErr := loadCurrentCheckpoint(transcript, true); loadErr == nil {
		snapshot := makeScreenSnapshot(recovered.screen, recovered.modes, position)
		recovered.screen.dispose()
		if legacy {
			if err = persistColdCheckpoint(transcript, snapshot, segments, expected); err != nil {
				return ScreenSnapshot{}, err
			}
		}
		return snapshot, nil
	}

	before, err := collectCastHeaders(transcript)
	if err != nil {
		return ScreenSnapshot{}, err
	}
	// A stale compact boundary still owns its absolute prefix. Missing files
	// must not be rewritten as a shorter, apparently complete lineage.
	if checkpoint, decodeErr := decodeCheckpoint(checkpointFile); decodeErr == nil && checkpoint.Version == screenCheckpointVersion {
		index := -1
		for i, segment := range before {
			if segment.incarnation == checkpoint.Incarnation {
				index = i
				break
			}
		}
		if index < 0 || index+1 != checkpoint.SegmentCount {
			return ScreenSnapshot{}, errors.New("ptyhost: compact checkpoint lineage changed")
		}
	}
	recovered, err := reconstructSnapshot(transcript)
	if err != nil {
		return ScreenSnapshot{}, err
	}
	defer recovered.screen.dispose()
	segments, err := collectFullCastSegments(transcript)
	if err != nil {
		return ScreenSnapshot{}, err
	}
	after, err := collectCastHeaders(transcript)
	if err != nil {
		return ScreenSnapshot{}, err
	}
	if !sameCastBoundaries(before, segments) || !sameCastBoundaries(segments, after) {
		return ScreenSnapshot{}, errors.New("ptyhost: transcript changed during snapshot repair")
	}
	position := recovered.position
	if position.Epoch == "" {
		position, err = freshTerminalPosition()
		if err != nil {
			return ScreenSnapshot{}, err
		}
	}
	snapshot := makeScreenSnapshot(recovered.screen, recovered.modes, position)
	if err := persistColdCheckpoint(transcript, snapshot, segments, expected); err != nil {
		if errors.Is(err, errCheckpointSuperseded) {
			newer, _, newerPosition, _, loadErr := loadCurrentCheckpoint(transcript, false)
			if loadErr == nil {
				defer newer.screen.dispose()
				return makeScreenSnapshot(newer.screen, newer.modes, newerPosition), nil
			}
		}
		return ScreenSnapshot{}, err
	}
	return snapshot, nil
}

func restoreCheckpointScreen(checkpoint screenCheckpoint) (recordedScreen, error) {
	screen, err := newTerminalScreen(checkpoint.Cols, checkpoint.Rows)
	if err != nil {
		return recordedScreen{}, err
	}
	var modes modeScanner
	screen.write(checkpoint.Data)
	modes.scan(checkpoint.Data)
	return recordedScreen{screen: screen, modes: modes}, nil
}

// recoverCheckpoint restores a snapshot and applies only the cast suffix after
// its durable boundary.
func recoverCheckpoint(transcript string) (recordedScreen, bool, error) {
	checkpoint, err := decodeCheckpoint(checkpointPath(transcript))
	if err != nil {
		return recordedScreen{}, false, err
	}
	segments, err := validateCheckpointSegments(transcript, checkpoint, false)
	if err != nil {
		return recordedScreen{}, false, err
	}
	index := -1
	for i, segment := range segments {
		if segment.incarnation == checkpoint.Incarnation {
			index = i
			break
		}
	}
	recovered, err := restoreCheckpointScreen(checkpoint)
	if err != nil {
		return recordedScreen{}, false, err
	}
	recovered.position = TerminalPosition{Epoch: checkpoint.Epoch, Sequence: checkpoint.Sequence}
	for i := index; i < len(segments); i++ {
		start := int64(0)
		if i == index {
			start = checkpoint.CastOffset
		}
		outputs, suffixErr := applyCastSuffix(segments[i].path, start, recovered.screen, &recovered.modes)
		if suffixErr != nil {
			recovered.screen.dispose()
			return recordedScreen{}, false, suffixErr
		}
		recovered.position.Sequence += TerminalSequence(outputs)
	}
	return recovered, true, nil
}

func applyCastSuffix(path string, offset int64, screen *terminalScreen, modes *modeScanner) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if offset < 0 || offset > info.Size() {
		return 0, errors.New("ptyhost: invalid checkpoint cast offset")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReader(f)
	if offset == 0 {
		if _, err := r.ReadBytes('\n'); err != nil {
			return 0, err
		}
	}
	outputs := 0
	for {
		line, readErr := r.ReadBytes('\n')
		atEnd := errors.Is(readErr, io.EOF)
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var event []json.RawMessage
			if err := json.Unmarshal(line, &event); err != nil || len(event) < 3 {
				if atEnd && isIncompleteJSON(err) {
					break
				}
				if err == nil {
					err = errBadCastString
				}
				return 0, fmt.Errorf("ptyhost: decode checkpoint cast suffix: %w", err)
			}
			var code string
			if err := json.Unmarshal(event[1], &code); err != nil {
				return 0, fmt.Errorf("ptyhost: decode checkpoint cast code: %w", err)
			}
			if err := applyRecordedEvent(screen, modes, code, event[2]); err != nil {
				return 0, err
			}
			if code == "o" {
				data, err := decodeCastString(event[2])
				if err != nil {
					return 0, err
				}
				outputs += len(data)
			}
		}
		if atEnd {
			break
		}
	}
	return outputs, nil
}
