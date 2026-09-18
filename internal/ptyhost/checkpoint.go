package ptyhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	screenCheckpointVersion  = 1
	maxScreenCheckpointBytes = 8 << 20
)

type checkpointSegment struct {
	Path        string `json:"path"`
	Incarnation int64  `json:"incarnation"`
	FileBytes   int64  `json:"file_bytes"`
	OutputBytes int    `json:"output_bytes"`
}

type screenCheckpoint struct {
	Version     int                 `json:"version"`
	Incarnation int64               `json:"incarnation"`
	CastOffset  int64               `json:"cast_offset"`
	Cols        uint                `json:"cols"`
	Rows        uint                `json:"rows"`
	Data        []byte              `json:"data"`
	Segments    []checkpointSegment `json:"segments,omitempty"`
}

func checkpointPath(transcript string) string {
	return strings.TrimSuffix(transcript, ".cast") + ".screen"
}

// checkpoint flushes all cast bytes represented by snap, then atomically
// records the snapshot and its exact cast boundary. Callers hold session.mu.
func (w *castWriter) checkpoint(path string, snap ScreenSnapshot, history []castSegment) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("ptyhost: checkpoint transcript is closed")
	}
	if len(w.pending) > 0 {
		w.eventLocked("o", w.pending)
		w.outputBytes += len(w.pending)
		w.pending = nil
	}
	if err := w.bw.Flush(); err != nil {
		w.mu.Unlock()
		return fmt.Errorf("ptyhost: flush checkpoint transcript: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		w.mu.Unlock()
		return fmt.Errorf("ptyhost: sync checkpoint transcript: %w", err)
	}
	info, err := w.f.Stat()
	current := castSegment{path: w.path, fileBytes: 0, outputBytes: w.outputBytes, incarnation: w.incarnation}
	if err == nil {
		current.fileBytes = info.Size()
	}
	w.mu.Unlock()
	if err != nil {
		return fmt.Errorf("ptyhost: inspect checkpoint transcript: %w", err)
	}

	segments := make([]checkpointSegment, 0, len(history)+1)
	for _, segment := range history {
		segments = append(segments, checkpointSegment{
			Path: filepath.Base(segment.path), Incarnation: segment.incarnation,
			FileBytes: segment.fileBytes, OutputBytes: segment.outputBytes,
		})
	}
	segments = append(segments, checkpointSegment{
		Path: filepath.Base(current.path), Incarnation: current.incarnation,
		FileBytes: current.fileBytes, OutputBytes: current.outputBytes,
	})
	checkpoint := screenCheckpoint{
		Version: screenCheckpointVersion, Incarnation: current.incarnation,
		CastOffset: current.fileBytes, Cols: snap.Cols, Rows: snap.Rows,
		Data: append([]byte(nil), snap.Data...), Segments: segments,
	}
	return writeCheckpointFile(path, checkpoint)
}

func writeCheckpointFile(path string, checkpoint screenCheckpoint) error {
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

func persistColdCheckpoint(transcript string, snap ScreenSnapshot) error {
	history, err := priorCastSegments(transcript)
	if err != nil {
		return err
	}
	current, err := inspectCastSegment(transcript)
	if err != nil {
		return err
	}
	segments := make([]checkpointSegment, 0, len(history)+1)
	for _, segment := range history {
		segments = append(segments, checkpointSegment{
			Path: filepath.Base(segment.path), Incarnation: segment.incarnation,
			FileBytes: segment.fileBytes, OutputBytes: segment.outputBytes,
		})
	}
	segments = append(segments, checkpointSegment{
		Path: filepath.Base(current.path), Incarnation: current.incarnation,
		FileBytes: current.fileBytes, OutputBytes: current.outputBytes,
	})
	return writeCheckpointFile(checkpointPath(transcript), screenCheckpoint{
		Version: screenCheckpointVersion, Incarnation: current.incarnation,
		CastOffset: current.fileBytes, Cols: snap.Cols, Rows: snap.Rows,
		Data: append([]byte(nil), snap.Data...), Segments: segments,
	})
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
	if checkpoint.Version != screenCheckpointVersion || checkpoint.Incarnation == 0 || checkpoint.CastOffset <= 0 {
		return screenCheckpoint{}, errors.New("ptyhost: unsupported screen checkpoint")
	}
	if err := validateScreenDimensions(checkpoint.Cols, checkpoint.Rows); err != nil {
		return screenCheckpoint{}, err
	}
	if len(checkpoint.Data) == 0 || len(checkpoint.Data) > maxScreenCheckpointBytes {
		return screenCheckpoint{}, errors.New("ptyhost: invalid screen checkpoint data")
	}
	return checkpoint, nil
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
	if len(checkpoint.Segments) == 0 {
		return recordedScreen{}, nil, false, errors.New("ptyhost: screen checkpoint has no segments")
	}
	segments := make([]castSegment, len(checkpoint.Segments))
	index := -1
	for i, raw := range checkpoint.Segments {
		if raw.Path == "" || filepath.Base(raw.Path) != raw.Path || raw.Incarnation == 0 || raw.FileBytes <= 0 || raw.OutputBytes < 0 {
			return recordedScreen{}, nil, false, errors.New("ptyhost: invalid checkpoint segment")
		}
		path := filepath.Join(filepath.Dir(transcript), raw.Path)
		segment, segmentErr := inspectCastHeader(path)
		if segmentErr != nil || segment.incarnation != raw.Incarnation {
			if segmentErr == nil {
				segmentErr = errors.New("ptyhost: checkpoint cast incarnation mismatch")
			}
			return recordedScreen{}, nil, false, segmentErr
		}
		if segment.fileBytes < raw.FileBytes {
			return recordedScreen{}, nil, false, errors.New("ptyhost: checkpoint cast was truncated")
		}
		segment.outputBytes = raw.OutputBytes
		segments[i] = segment
		if raw.Incarnation == checkpoint.Incarnation {
			if index >= 0 {
				return recordedScreen{}, nil, false, errors.New("ptyhost: duplicate checkpoint incarnation")
			}
			index = i
		}
	}
	if index < 0 || checkpoint.CastOffset != checkpoint.Segments[index].FileBytes {
		return recordedScreen{}, nil, false, errors.New("ptyhost: checkpoint boundary mismatch")
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
		delta, err := applyCastSuffix(segments[i].path, start, recovered.screen, &recovered.modes)
		if err != nil {
			recovered.screen.dispose()
			return recordedScreen{}, nil, false, err
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
