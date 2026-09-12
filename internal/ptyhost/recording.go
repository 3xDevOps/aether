package ptyhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

// recordingFile is one immutable-size view of a cast. The file itself may be
// open while the live writer continues, but size bounds the view to the flush
// that Recording observed.
type recordingFile struct {
	path      string
	f         *os.File
	info      os.FileInfo
	header    castHeader
	headerRaw []byte
	br        *bufio.Reader
}

// recordingReader merges timestamped cast rotations without retaining their
// contents. It owns every descriptor in files and closes them on every exit
// path, including a handler abandoning the HTTP response.
type recordingReader struct {
	mu sync.Mutex

	files []*recordingFile
	at    int
	first bool
	base  int64
	buf   []byte
	queue []byte
	// boundary is true until the first event of a rotation is observed. A
	// resize event then separates that incarnation from the preceding one.
	boundary bool
	last     float64
	err      error
	closed   bool
}

// Recording returns a finite asciicast v2 snapshot containing every preserved
// incarnation of run's PTY transcript and the current cast. Active output is
// flushed while the session lock is held, so the snapshot includes recent
// bytes without changing the PTY's geometry or input state. The returned
// reader never follows subsequent writes.
func (h *Host) Recording(run domain.RunID) (io.ReadCloser, error) {
	key := RunSession(run)
	if s := h.lookup(key); s != nil {
		// Flush and bound the files without racing this session's output.
		s.mu.Lock()
		defer s.mu.Unlock()
		if tr := s.tr; tr != nil {
			tr.mu.Lock()
			if !tr.closed {
				if err := tr.bw.Flush(); err != nil {
					tr.mu.Unlock()
					return nil, fmt.Errorf("ptyhost: flush recording: %w", err)
				}
			}
			tr.mu.Unlock()
		}
		return h.openRecording(run)
	}
	return h.openRecording(run)
}

func (h *Host) openRecording(run domain.RunID) (io.ReadCloser, error) {
	currentPath := h.transcriptPath(RunSession(run))
	// Keep the current inode before listing rotations: a restart may rename
	// it and create a new current file while the directory is being read.
	current, openErr := os.Open(currentPath)
	if openErr != nil && !errors.Is(openErr, os.ErrNotExist) {
		return nil, fmt.Errorf("ptyhost: open recording %s: %w", currentPath, openErr)
	}
	defer func() {
		if current != nil {
			_ = current.Close()
		}
	}()
	entries, err := os.ReadDir(h.cfg.TranscriptDir)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: find recording for %s: %w", run, err)
	}
	baseName := filepath.Base(currentPath)
	stem := strings.TrimSuffix(baseName, ".cast")
	type candidate struct {
		name     string
		rotation int64
		current  bool
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == baseName {
			continue
		}
		prefix := stem + "."
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".cast") {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".cast")
		rotation, parseErr := strconv.ParseInt(stamp, 10, 64)
		if parseErr != nil {
			continue
		}
		candidates = append(candidates, candidate{name: name, rotation: rotation})
	}
	if current != nil {
		candidates = append(candidates, candidate{name: baseName, current: true})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("ptyhost: recording for %s: %w", run, os.ErrNotExist)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].current != candidates[j].current {
			return !candidates[i].current
		}
		if candidates[i].rotation != candidates[j].rotation {
			return candidates[i].rotation < candidates[j].rotation
		}
		return candidates[i].name < candidates[j].name
	})

	files := make([]*recordingFile, 0, len(candidates))
	closeFiles := func() {
		for _, file := range files {
			_ = file.f.Close()
		}
	}
	for _, candidate := range candidates {
		path := filepath.Join(h.cfg.TranscriptDir, candidate.name)
		var f *os.File
		var openErr error
		if candidate.current {
			f, current = current, nil
		} else {
			f, openErr = os.Open(path)
		}
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				// A stopped run may be cleaned up concurrently. Preserve
				// any rotations that remain instead of failing the whole
				// snapshot because one directory entry vanished.
				continue
			}
			closeFiles()
			return nil, fmt.Errorf("ptyhost: open recording %s: %w", path, openErr)
		}
		info, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			closeFiles()
			return nil, fmt.Errorf("ptyhost: stat recording %s: %w", path, statErr)
		}
		if info.Size() == 0 {
			_ = f.Close()
			continue
		}
		// A rename race can make two names refer to one incarnation. Keep
		// one descriptor by identity rather than replaying its events twice.
		duplicate := false
		for _, prior := range files {
			if os.SameFile(prior.info, info) {
				duplicate = true
				break
			}
		}
		if duplicate {
			_ = f.Close()
			continue
		}
		br := bufio.NewReader(io.NewSectionReader(f, 0, info.Size()))
		headerLine, readErr := br.ReadString('\n')
		trimmed := bytes.TrimSpace([]byte(headerLine))
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = f.Close()
			closeFiles()
			return nil, fmt.Errorf("ptyhost: read recording header %s: %w", path, readErr)
		}
		var header castHeader
		if len(trimmed) == 0 || json.Unmarshal(trimmed, &header) != nil || header.Version != 2 {
			_ = f.Close()
			closeFiles()
			return nil, fmt.Errorf("ptyhost: malformed recording header %s", path)
		}
		files = append(files, &recordingFile{
			path:      path,
			f:         f,
			info:      info,
			header:    header,
			headerRaw: append([]byte(nil), trimmed...),
			br:        br,
		})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("ptyhost: recording for %s: %w", run, os.ErrNotExist)
	}
	return &recordingReader{files: files, base: files[0].header.Timestamp}, nil
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	for {
		if len(r.buf) > 0 {
			n := copy(p, r.buf)
			r.buf = r.buf[n:]
			return n, nil
		}
		if len(r.queue) > 0 {
			r.buf, r.queue = r.queue, nil
			continue
		}
		if r.err != nil {
			return 0, r.err
		}
		if !r.first {
			r.first = true
			r.buf = append(r.buf, r.files[0].headerRaw...)
			r.buf = append(r.buf, '\n')
			continue
		}
		if r.at >= len(r.files) {
			r.err = io.EOF
			r.closeFilesLocked()
			return 0, io.EOF
		}
		file := r.files[r.at]
		line, readErr := file.br.ReadString('\n')
		atEnd := errors.Is(readErr, io.EOF)
		if readErr != nil && !atEnd {
			r.failLocked(fmt.Errorf("ptyhost: read recording %s: %w", file.path, readErr))
			return 0, r.err
		}
		if len(line) == 0 && atEnd {
			r.advanceLocked()
			continue
		}
		trimmed := bytes.TrimSpace([]byte(line))
		eventTime, timeErr := recordingEventTime(trimmed)
		if timeErr != nil {
			// A process crash can leave only the final JSON event half-written.
			// It is the sole malformed form tolerated; malformed complete lines
			// are real corruption and must not silently truncate history.
			if atEnd && isIncompleteJSON(timeErr) {
				r.advanceLocked()
				continue
			}
			r.failLocked(fmt.Errorf("ptyhost: parse recording event %s: %w", file.path, timeErr))
			return 0, r.err
		}
		previous := r.last
		adjusted := float64(file.header.Timestamp-r.base) + eventTime
		if adjusted < previous {
			adjusted = previous
		}
		event := replaceRecordingTimestamp(trimmed, adjusted)
		if r.at > 0 && r.boundary {
			r.boundary = false
			boundaryTime := float64(file.header.Timestamp - r.base)
			if boundaryTime < previous {
				boundaryTime = previous
			}
			if adjusted < boundaryTime {
				adjusted = boundaryTime
				event = replaceRecordingTimestamp(trimmed, adjusted)
			}
			r.last = adjusted
			r.buf = recordingEventLine(boundaryTime, "r", fmt.Sprintf("%dx%d", file.header.Width, file.header.Height))
			r.queue = event
			continue
		}
		r.last = adjusted
		r.buf = event
	}
}

func (r *recordingReader) advanceLocked() {
	if r.at >= len(r.files) {
		return
	}
	_ = r.files[r.at].f.Close()
	r.files[r.at].f = nil
	r.at++
	if r.at < len(r.files) {
		r.boundary = true
	}
}

func (r *recordingReader) failLocked(err error) {
	r.err = err
	r.closeFilesLocked()
}

func (r *recordingReader) closeFilesLocked() {
	for _, file := range r.files {
		if file.f == nil {
			continue
		}
		_ = file.f.Close()
		file.f = nil
	}
}

func (r *recordingReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var first error
	for _, file := range r.files {
		if file.f == nil {
			continue
		}
		if err := file.f.Close(); err != nil && first == nil {
			first = err
		}
		file.f = nil
	}
	return first
}

func recordingEventTime(line []byte) (float64, error) {
	var event []json.RawMessage
	if err := json.Unmarshal(line, &event); err != nil {
		return 0, err
	}
	if len(event) < 3 {
		return 0, errors.New("malformed event")
	}
	var timestamp float64
	if err := json.Unmarshal(event[0], &timestamp); err != nil || math.IsNaN(timestamp) || math.IsInf(timestamp, 0) {
		if err == nil {
			err = errors.New("non-finite timestamp")
		}
		return 0, err
	}
	return timestamp, nil
}

func replaceRecordingTimestamp(line []byte, timestamp float64) []byte {
	comma := bytes.IndexByte(line[1:], ',')
	comma++
	out := make([]byte, 0, len(line)+12)
	out = append(out, '[')
	out = strconv.AppendFloat(out, timestamp, 'f', 6, 64)
	out = append(out, line[comma:]...)
	out = append(out, '\n')
	return out
}

func recordingEventLine(timestamp float64, code, data string) []byte {
	line := make([]byte, 0, len(data)+32)
	line = append(line, '[')
	line = strconv.AppendFloat(line, timestamp, 'f', 6, 64)
	line = append(line, ',', '"')
	line = append(line, code...)
	line = append(line, '"', ',')
	line = appendCastString(line, []byte(data))
	line = append(line, ']', '\n')
	return line
}
