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
	screenCheckpointVersion  = 2
	maxScreenCheckpointBytes = 8 << 20
)

type checkpointSegment struct {
	Path        string `json:"path"`
	Incarnation int64  `json:"incarnation"`
	FileBytes   int64  `json:"file_bytes"`
	OutputBytes int    `json:"output_bytes"`
}

type screenCheckpoint struct {
	Version         int                 `json:"version"`
	Epoch           TerminalEpoch       `json:"epoch,omitempty"`
	Sequence        TerminalSequence    `json:"sequence,omitempty"`
	CastOutputBytes uint64              `json:"cast_output_bytes,omitempty"`
	Incarnation     int64               `json:"incarnation"`
	CastOffset      int64               `json:"cast_offset"`
	Cols            uint                `json:"cols"`
	Rows            uint                `json:"rows"`
	Data            []byte              `json:"data"`
	Segments        []checkpointSegment `json:"segments,omitempty"`
}

func (s *session) captureCheckpointLocked() (*checkpointCapture, error) {
	if s.tr == nil || s.screen == nil {
		return nil, nil
	}
	if _, isRun := s.run.Run(); !isRun || s.checkpoint == "" {
		return nil, nil
	}
	snapshot := s.screenSnapshotLocked()
	capture, err := s.tr.captureCheckpoint(s.checkpoint, snapshot, s.history)
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
	file    *os.File
	segment castSegment
}

func (b *castBoundary) release() {
	if b.writer == nil {
		return
	}
	b.writer.lifetimeMu.RUnlock()
	b.writer = nil
	b.file = nil
}

func (b *castBoundary) flush() error {
	if b.writer == nil {
		return nil
	}
	b.writer.mu.Lock()
	err := b.writer.flushStagedLocked()
	if err == nil {
		err = b.writer.bw.Flush()
	}
	b.writer.mu.Unlock()
	return err
}

func (b *castBoundary) sync() error {
	if b.writer == nil || b.file == nil {
		return nil
	}
	err := b.file.Sync()
	b.release()
	return err
}

type checkpointCapture struct {
	path            string
	snapshot        ScreenSnapshot
	segments        []checkpointSegment
	castOutputBytes uint64
	boundary        castBoundary
	seq             uint64
}

func (c *checkpointCapture) persist() error {
	if err := c.boundary.flush(); err != nil {
		c.boundary.release()
		return fmt.Errorf("ptyhost: flush checkpoint transcript: %w", err)
	}
	if err := c.boundary.sync(); err != nil {
		return fmt.Errorf("ptyhost: sync checkpoint transcript: %w", err)
	}
	return writeCheckpointFile(c.path, screenCheckpoint{
		Version:         screenCheckpointVersion,
		Epoch:           c.snapshot.Position.Epoch,
		Sequence:        c.snapshot.Position.Sequence,
		CastOutputBytes: c.castOutputBytes,
		Incarnation:     c.boundary.segment.incarnation,
		CastOffset:      c.boundary.segment.fileBytes,
		Cols:            c.snapshot.Cols,
		Rows:            c.snapshot.Rows,
		Data:            c.snapshot.Data,
		Segments:        c.segments,
	})
}

// captureCheckpoint records the logical cast boundary represented by snap.
// Pending UTF-8 is made an event under the short writer lock, while flushing
// the buffered prefix and syncing the file are deferred until persistence.
func (w *castWriter) captureCheckpoint(path string, snap ScreenSnapshot, history []castSegment) (checkpointCapture, error) {
	w.lifetimeMu.RLock()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.lifetimeMu.RUnlock()
		return checkpointCapture{}, errors.New("ptyhost: checkpoint transcript is closed")
	}
	// A sticky write error must not turn pending tails into an unbounded queue.
	// An empty write checks it without flushing the buffer.
	if _, err := w.bw.Write(nil); err != nil {
		w.mu.Unlock()
		w.lifetimeMu.RUnlock()
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
	writerFile := w.f
	w.mu.Unlock()

	segments := make([]checkpointSegment, 0, len(history)+1)
	var castOutputBytes uint64
	for _, segment := range history {
		if segment.outputBytes < 0 {
			w.lifetimeMu.RUnlock()
			return checkpointCapture{}, errors.New("ptyhost: unknown historical output boundary")
		}
		castOutputBytes += uint64(segment.outputBytes)
		segments = append(segments, checkpointSegment{
			Path: filepath.Base(segment.path), Incarnation: segment.incarnation,
			FileBytes: segment.fileBytes, OutputBytes: segment.outputBytes,
		})
	}
	castOutputBytes += uint64(boundary.outputBytes)
	segments = append(segments, checkpointSegment{
		Path: filepath.Base(boundary.path), Incarnation: boundary.incarnation,
		FileBytes: boundary.fileBytes, OutputBytes: boundary.outputBytes,
	})
	return checkpointCapture{
		path: path, snapshot: cloneScreenSnapshot(snap), segments: segments,
		castOutputBytes: castOutputBytes,
		boundary:        castBoundary{writer: w, file: writerFile, segment: boundary},
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
	raw := make([]checkpointSegment, 0, len(segments))
	var outputBytes uint64
	for _, segment := range segments {
		if segment.outputBytes < 0 {
			return screenCheckpoint{}, errors.New("ptyhost: unknown checkpoint output boundary")
		}
		if ^uint64(0)-outputBytes < uint64(segment.outputBytes) {
			return screenCheckpoint{}, errors.New("ptyhost: checkpoint output boundary overflow")
		}
		outputBytes += uint64(segment.outputBytes)
		raw = append(raw, checkpointSegment{
			Path: filepath.Base(segment.path), Incarnation: segment.incarnation,
			FileBytes: segment.fileBytes, OutputBytes: segment.outputBytes,
		})
	}
	last := segments[len(segments)-1]
	return screenCheckpoint{
		Version: screenCheckpointVersion, Epoch: snap.Position.Epoch,
		Sequence: snap.Position.Sequence, CastOutputBytes: outputBytes,
		Incarnation: last.incarnation, CastOffset: last.fileBytes,
		Cols: snap.Cols, Rows: snap.Rows, Data: append([]byte(nil), snap.Data...),
		Segments: raw,
	}, nil
}

func persistColdCheckpoint(transcript string, snap ScreenSnapshot, segments []castSegment, expected checkpointFileIdentity) error {
	checkpoint, err := checkpointFromSnapshot(snap, segments)
	if err != nil {
		return err
	}
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
	if checkpoint.Version != 1 && checkpoint.Version != screenCheckpointVersion {
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

func validateCheckpointSegments(transcript string, checkpoint screenCheckpoint, exact bool) ([]castSegment, error) {
	if len(checkpoint.Segments) == 0 {
		return nil, errors.New("ptyhost: screen checkpoint has no segments")
	}
	segments := make([]castSegment, len(checkpoint.Segments))
	index := -1
	var outputBytes uint64
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
		path := filepath.Join(filepath.Dir(transcript), raw.Path)
		segment, segmentErr := inspectCastHeader(path)
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
		segments[i] = segment
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
	if checkpoint.Version == screenCheckpointVersion &&
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
	if len(paths) != len(segments) {
		return nil, errors.New("ptyhost: screen checkpoint segment set is stale")
	}
	for i := range paths {
		if filepath.Clean(paths[i]) != filepath.Clean(segments[i].path) {
			return nil, errors.New("ptyhost: screen checkpoint segment order mismatch")
		}
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
	return recovered, segments, position, checkpoint.Version == 1, nil
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
	checkpointFile := checkpointPath(transcript)
	expected, err := readCheckpointIdentity(checkpointFile)
	if err != nil {
		return ScreenSnapshot{}, err
	}

	// An exact v1 checkpoint is already a bounded, authoritative screen. Give
	// it a fresh epoch because old clients carried no continuity proof, then
	// atomically migrate it without decoding the cast.
	if recovered, segments, position, legacy, loadErr := loadCurrentCheckpoint(transcript, true); loadErr == nil && legacy {
		snapshot := makeScreenSnapshot(recovered.screen, recovered.modes, position)
		recovered.screen.dispose()
		if err := persistColdCheckpoint(transcript, snapshot, segments, expected); err != nil {
			return ScreenSnapshot{}, err
		}
		return snapshot, nil
	}

	before, err := collectCastHeaders(transcript)
	if err != nil {
		return ScreenSnapshot{}, err
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
	position, err := freshTerminalPosition()
	if err != nil {
		return ScreenSnapshot{}, err
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
// its durable boundary. The returned segments retain output counts for the
// next checkpoint, avoiding a historical transcript scan on ordinary restart.
func recoverCheckpoint(transcript string) (recordedScreen, []castSegment, bool, error) {
	checkpoint, err := decodeCheckpoint(checkpointPath(transcript))
	if err != nil {
		return recordedScreen{}, nil, false, err
	}
	segments, err := validateCheckpointSegments(transcript, checkpoint, false)
	if err != nil {
		return recordedScreen{}, nil, false, err
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
		return recordedScreen{}, nil, false, err
	}
	for i := index; i < len(segments); i++ {
		start := int64(0)
		if i == index {
			start = checkpoint.CastOffset
		}
		delta, suffixErr := applyCastSuffix(segments[i].path, start, recovered.screen, &recovered.modes)
		if suffixErr != nil {
			recovered.screen.dispose()
			return recordedScreen{}, nil, false, suffixErr
		}
		if i == index {
			segments[i].outputBytes += delta
		} else if delta > 0 {
			segments[i].outputBytes = delta
		}
		if info, statErr := os.Stat(segments[i].path); statErr == nil {
			segments[i].fileBytes = info.Size()
		}
	}
	return recovered, segments, true, nil
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

func relocateCheckpointSegments(segments []castSegment, transcript string) {
	paths, err := priorCastPaths(transcript)
	if err != nil {
		return
	}
	for i := range segments {
		if segments[i].path != transcript {
			continue
		}
		for _, path := range paths {
			candidate, err := inspectCastHeader(path)
			if err == nil && candidate.incarnation == segments[i].incarnation {
				segments[i].path = path
				segments[i].fileBytes = candidate.fileBytes
				break
			}
		}
	}
}
