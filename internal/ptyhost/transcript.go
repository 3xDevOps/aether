package ptyhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	transcriptFlushInterval        = 2 * time.Second
	maxLegacyCastHeaderInspections = 128
)

type transcriptLifecycleEntry struct {
	mu   sync.RWMutex
	refs int
}

type transcriptLifecycleRef struct {
	path  string
	entry *transcriptLifecycleEntry
}

var transcriptLifecycleRegistry = struct {
	sync.Mutex
	entries map[string]*transcriptLifecycleEntry
}{
	entries: make(map[string]*transcriptLifecycleEntry),
}

func acquireTranscriptLifecycle(path string) transcriptLifecycleRef {
	transcriptLifecycleRegistry.Lock()
	entry := transcriptLifecycleRegistry.entries[path]
	if entry == nil {
		entry = new(transcriptLifecycleEntry)
		transcriptLifecycleRegistry.entries[path] = entry
	}
	entry.refs++
	transcriptLifecycleRegistry.Unlock()
	return transcriptLifecycleRef{path: path, entry: entry}
}

func (r transcriptLifecycleRef) release() {
	transcriptLifecycleRegistry.Lock()
	r.entry.refs--
	if r.entry.refs == 0 {
		delete(transcriptLifecycleRegistry.entries, r.path)
	}
	transcriptLifecycleRegistry.Unlock()
}

type castHeader struct {
	Version     int               `json:"version"`
	Width       uint              `json:"width"`
	Height      uint              `json:"height"`
	Timestamp   int64             `json:"timestamp"`
	Incarnation int64             `json:"incarnation,omitempty"`
	Env         map[string]string `json:"env"`
}

// castWriter appends asciinema cast v2 events (output, resize, marker) to
// the run's transcript file. Output only - PTY input is never recorded.
// Output chunks that end mid-rune hold the incomplete UTF-8 tail back until
// the next chunk so runes are recorded whole; bytes that are not valid
// UTF-8 are escaped losslessly (see appendCastString), so replay through
// decodeCastString reproduces the live byte stream exactly.
type castWriter struct {
	mu           sync.Mutex
	lifetimeMu   sync.RWMutex
	f            *os.File
	bw           *bufio.Writer
	start        time.Time
	incarnation  int64
	pending      []byte
	staged       []byte
	outputBytes  int
	logicalBytes int64
	closed       bool
	stop         chan struct{}
	// path is kept so a marker can still be appended after close, for a
	// delivery that raced the session's end.
	path string
}

func newCastWriter(path string, cols, rows uint) (*castWriter, error) {
	lifecycle := acquireTranscriptLifecycle(path)
	lifecycle.entry.mu.Lock()
	defer func() {
		lifecycle.entry.mu.Unlock()
		lifecycle.release()
	}()
	if err := renameAsideTranscript(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: create transcript: %w", err)
	}
	start := time.Now()
	w := &castWriter{
		f:           f,
		bw:          bufio.NewWriterSize(f, 32*1024),
		start:       start,
		incarnation: start.UnixNano(),
		stop:        make(chan struct{}),
		path:        path,
	}
	hdr, err := json.Marshal(castHeader{
		Version:     2,
		Width:       cols,
		Height:      rows,
		Timestamp:   w.start.Unix(),
		Incarnation: w.incarnation,
		Env:         map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ptyhost: transcript header: %w", err)
	}
	headerLine := append(hdr, '\n')
	if n, err := w.bw.Write(headerLine); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ptyhost: transcript header: %w", err)
	} else {
		w.logicalBytes = int64(n)
	}
	if err := w.bw.Flush(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ptyhost: flush transcript header: %w", err)
	}
	go w.flushLoop()
	return w, nil
}

// renameAsideTranscript preserves an existing non-empty transcript (a prior
// incarnation of the run, e.g. before a reboot-recovery restart) under the
// stable incarnation name that history cursors can resolve directly.
func renameAsideTranscript(path string) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return nil
	}
	segment, err := inspectCastHeader(path)
	if err != nil {
		return err
	}
	aside := filepath.Join(filepath.Dir(path), stableCastSegmentName(path, segment.incarnation))
	if _, err := os.Stat(aside); err == nil {
		return fmt.Errorf("ptyhost: preserve prior transcript: stable segment already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ptyhost: inspect prior transcript target: %w", err)
	}
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("ptyhost: preserve prior transcript: %w", err)
	}
	return nil
}

// readCastTail decodes output events from the bounded tail of an asciinema
// v2 transcript. A partial first line is discarded because the read window
// may begin in the middle of a JSON event.
func readCastTail(path string, maxBytes int) ([]byte, error) {
	out, _, err := readCastTailWindow(path, maxBytes, maxBytes*8)
	return out, err
}

// readCastTailWindow bounds both decoded output and bytes read from disk.
// JSON escaping expands one terminal byte to at most six cast bytes; the
// small extra margin covers event framing without making a giant event a
// request-path scan.
func readCastTailWindow(path string, maxBytes, maxRawBytes int) ([]byte, int, error) {
	if maxBytes <= 0 || maxRawBytes <= 0 {
		return nil, 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	window := int64(maxRawBytes)
	if window > info.Size() {
		window = info.Size()
	}
	if window == 0 {
		return nil, 0, nil
	}
	raw := make([]byte, int(window))
	if _, err := io.ReadFull(io.NewSectionReader(f, info.Size()-window, window), raw); err != nil {
		return nil, 0, err
	}
	if info.Size() > window {
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			return nil, int(window), nil
		}
		raw = raw[i+1:]
	}

	out := make([]byte, 0, maxBytes)
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event []json.RawMessage
		if err := json.Unmarshal(line, &event); err != nil || len(event) < 3 {
			continue
		}
		var code string
		if err := json.Unmarshal(event[1], &code); err != nil || code != "o" {
			continue
		}
		data, err := decodeCastString(event[2])
		if err != nil {
			continue
		}
		out = append(out, data...)
	}
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
		// Discard the first clipped output line, as the live ring does.
		if i := bytes.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out, int(window), nil
}

// readRecentCast returns a bounded output suffix from the newest transcript
// incarnations. Legacy names receive a header identity check, but output
// counts and decoded tail bytes remain bounded regardless of retained age.
func readRecentCast(path string, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, nil
	}
	paths, err := priorCastPaths(path)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		paths = append(paths, path)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if len(paths) == 0 {
		return nil, os.ErrNotExist
	}

	// Six bytes cover the worst JSON escape expansion. Two more bytes per
	// output byte leave framing/headroom while keeping total I/O fixed.
	rawBudget := maxBytes * 8
	if rawBudget < 0 {
		rawBudget = int(^uint(0) >> 1)
	}
	var chunks [][]byte
	total := 0
	for i := len(paths) - 1; i >= 0 && total < maxBytes && rawBudget > 0; i-- {
		want := maxBytes - total
		chunk, used, readErr := readCastTailWindow(paths[i], want, rawBudget)
		if readErr != nil {
			return nil, readErr
		}
		rawBudget -= used
		if len(chunk) == 0 {
			continue
		}
		chunks = append(chunks, chunk)
		total += len(chunk)
	}
	out := make([]byte, 0, total)
	for i := len(chunks) - 1; i >= 0; i-- {
		out = append(out, chunks[i]...)
	}
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	return out, nil
}

func (w *castWriter) flushLoop() {
	t := time.NewTicker(transcriptFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.mu.Lock()
			if !w.closed {
				_ = w.flushStagedLocked()
				_ = w.bw.Flush()
			}
			w.mu.Unlock()
		}
	}
}

// flush makes complete output events visible to read-only history requests.
func (w *castWriter) flush() error {
	w.lifetimeMu.RLock()
	defer w.lifetimeMu.RUnlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if err := w.flushStagedLocked(); err != nil {
		return err
	}
	return w.bw.Flush()
}

func (w *castWriter) output(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	data := append(w.pending, p...)
	cut := utf8Boundary(data)
	w.pending = append([]byte(nil), data[cut:]...)
	if cut > 0 {
		w.eventLocked("o", data[:cut])
		w.outputBytes += cut
	}
}

// seed records a compact recovered screen for the next restart. It is applied
// during screen reconstruction but excluded from raw transcript replay, where
// emitting it would duplicate output already present in older cast segments.
func (w *castWriter) seed(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || len(p) == 0 {
		return
	}
	w.eventLocked("s", p)
}

func (w *castWriter) resize(cols, rows uint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	// A resize is an ordering boundary. Do not leave an incomplete UTF-8
	// prefix stranded across it: output the raw prefix first, then record
	// the geometry event, so cold reconstruction sees the same byte stream
	// and grid transition as the live emulator.
	if len(w.pending) > 0 {
		w.eventLocked("o", w.pending)
		w.outputBytes += len(w.pending)
		w.pending = nil
	}
	w.eventLocked("r", fmt.Appendf(nil, "%dx%d", cols, rows))
}

func (w *castWriter) marker(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.eventLocked("m", []byte(text))
}

// lateMarker records an attribution event after close, for a delivery that
// raced the session's end: the cast file is reopened in append mode for the
// single event so replay keeps the attribution the scheduler records. On an
// open writer it behaves like marker.
func (w *castWriter) lateMarker(text string) {
	w.mu.Lock()
	if !w.closed {
		w.eventLocked("m", []byte(text))
		w.mu.Unlock()
		return
	}
	path, start := w.path, w.start
	w.mu.Unlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(castLine(start, "m", []byte(text)))
	_ = f.Sync()
	_ = f.Close()
}
func (w *castWriter) flushStagedLocked() error {
	if len(w.staged) == 0 {
		return nil
	}
	n, err := w.bw.Write(w.staged)
	if n > 0 {
		w.staged = w.staged[n:]
	}
	if err == nil && len(w.staged) > 0 {
		return io.ErrShortWrite
	}
	return err
}

func (w *castWriter) eventLocked(code string, data []byte) {
	line := castLine(w.start, code, data)
	if len(w.staged) > 0 {
		if err := w.flushStagedLocked(); err != nil {
			return
		}
	}
	if n, _ := w.bw.Write(line); n > 0 {
		w.logicalBytes += int64(n)
	}
}

type castSegment struct {
	path        string
	fileBytes   int64
	outputBytes int
	incarnation int64
}

// priorCastSegments returns every immutable incarnation renamed aside before
// the current transcript was opened, oldest first.
func priorCastSegments(path string) ([]castSegment, error) {
	paths, err := priorCastPaths(path)
	if err != nil {
		return nil, err
	}
	currentStart := currentCastStart(path)
	segments := make([]castSegment, 0, len(paths))
	for _, segmentPath := range paths {
		segment, err := inspectCastSegment(segmentPath)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(segmentPath)
		incarnation, stable := castSegmentIncarnation(path, name)
		if stable {
			if incarnation != segment.incarnation {
				return nil, errors.New("ptyhost: transcript segment identity mismatch")
			}
		} else {
			rotation, legacy := legacyCastSegmentIncarnation(path, name)
			if !legacy || !legacyCastArchive(segmentPath, rotation, currentStart) {
				return nil, errors.New("ptyhost: transcript segment identity mismatch")
			}
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

// priorCastPaths accepts the unambiguous current grammar without I/O. In the
// legacy decimal grammar the suffix was the rotation time, not the header's
// incarnation: a valid archive therefore starts no later than its suffix,
// which in turn is no later than the current cast. That ordering excludes a
// readable current cast for a dotted run. Unreadable candidates remain visible
// so repair reports corruption instead of silently dropping recorded history.
func priorCastPaths(path string) ([]string, error) {
	return discoverPriorCastPaths(context.Background(), path, maxLegacyCastHeaderInspections)
}

// removablePriorCastPaths performs the same identity-safe discovery without
// the request-path inspection cap. Removal is maintenance work and must drain
// arbitrarily long retained histories, while remaining cancellable.
func removablePriorCastPaths(ctx context.Context, path string) ([]string, error) {
	return discoverPriorCastPaths(ctx, path, 0)
}

func discoverPriorCastPaths(ctx context.Context, path string, maxLegacy int) ([]string, error) {
	matches, err := filepath.Glob(strings.TrimSuffix(path, ".cast") + ".*.cast")
	if err != nil {
		return nil, fmt.Errorf("ptyhost: find transcript history: %w", err)
	}
	type candidate struct {
		path  string
		order int64
	}
	candidates := make([]candidate, 0, len(matches))
	legacyHeaders := 0
	currentStart := currentCastStart(path)
	for _, match := range matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := filepath.Base(match)
		order, ok := castSegmentIncarnation(path, name)
		if !ok {
			order, ok = legacyCastSegmentIncarnation(path, name)
			if !ok {
				continue
			}
			legacyHeaders++
			if maxLegacy > 0 && legacyHeaders > maxLegacy {
				return nil, errors.New("ptyhost: legacy transcript segment discovery limit exceeded")
			}
			if !legacyCastArchive(match, order, currentStart) {
				continue
			}
		}
		candidates = append(candidates, candidate{path: match, order: order})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].order == candidates[j].order {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].order < candidates[j].order
	})
	paths := make([]string, len(candidates))
	for i := range candidates {
		paths[i] = candidates[i].path
	}
	return paths, nil
}

func currentCastStart(path string) int64 {
	header, err := newestCastHeader(path)
	if err != nil {
		return 0
	}
	return castHeaderStart(header)
}

func castHeaderStart(header castHeader) int64 {
	if header.Incarnation > 0 {
		return header.Incarnation
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if header.Timestamp > 0 && header.Timestamp <= maxInt64/int64(time.Second) {
		return header.Timestamp * int64(time.Second)
	}
	return 0
}

func legacyCastArchive(path string, rotation, currentStart int64) bool {
	header, err := newestCastHeader(path)
	if err != nil {
		// A syntactically valid but corrupt legacy candidate must reach replay
		// and repair so callers learn that retained history is unavailable.
		return true
	}
	start := castHeaderStart(header)
	if start == 0 {
		return true
	}
	return start <= rotation && (currentStart == 0 || rotation <= currentStart)
}

// Stable cast segments use a delimiter that cannot occur in a run ID. A
// decimal suffix alone is ambiguous with the current cast of a dotted run
// whose ID ends in that suffix.
func stableCastSegmentName(path string, incarnation int64) string {
	stem := strings.TrimSuffix(filepath.Base(path), ".cast")
	return stem + ".~" + strconv.FormatInt(incarnation, 10) + ".cast"
}

func castSegmentIncarnation(path, name string) (int64, bool) {
	if filepath.Base(name) != name {
		return 0, false
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".cast")
	if !strings.HasPrefix(name, stem+".~") || !strings.HasSuffix(name, ".cast") {
		return 0, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, stem+".~"), ".cast")
	incarnation, err := strconv.ParseInt(id, 10, 64)
	if err != nil || incarnation <= 0 || id != strconv.FormatInt(incarnation, 10) {
		return 0, false
	}
	return incarnation, true
}

// Legacy segment names predate the unambiguous .~ delimiter. They are used
// only on compatibility paths that inspect the header before accepting them;
// history discovery must never classify them by filename alone.
func legacyCastSegmentIncarnation(path, name string) (int64, bool) {
	if filepath.Base(name) != name {
		return 0, false
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".cast")
	if !strings.HasPrefix(name, stem+".") || !strings.HasSuffix(name, ".cast") {
		return 0, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, stem+"."), ".cast")
	incarnation, err := strconv.ParseInt(id, 10, 64)
	if err != nil || incarnation <= 0 || id != strconv.FormatInt(incarnation, 10) {
		return 0, false
	}
	return incarnation, true
}

func openFullCastReplay(path string) (io.ReadCloser, int, error) {
	if replay, total, ok := openCheckpointCastReplay(path); ok {
		return replay, total, nil
	}
	segments, err := priorCastSegments(path)
	if err != nil {
		return nil, 0, err
	}
	current, err := inspectCastSegment(path)
	if err != nil {
		return nil, 0, err
	}
	segments = append(segments, current)
	total := 0
	for _, segment := range segments {
		total += segment.outputBytes
	}
	return openCastReplay(segments, nil), total, nil
}

func openCheckpointCastReplay(path string) (io.ReadCloser, int, bool) {
	checkpoint, err := decodeCheckpoint(checkpointPath(path))
	if err != nil || checkpoint.Version != screenCheckpointVersion {
		return nil, 0, false
	}
	segments, err := validateCheckpointSegments(path, checkpoint, true)
	if err != nil || checkpoint.CastOutputBytes > uint64(^uint(0)>>1) {
		return nil, 0, false
	}
	return openCastReplay(segments, nil), int(checkpoint.CastOutputBytes), true
}

func newestCastHeader(path string) (castHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return castHeader{}, err
	}
	defer func() { _ = f.Close() }()
	return readCastHeader(f)
}
func legacyCastIncarnation(info os.FileInfo, header castHeader) int64 {
	id := legacyCastFileID(info)
	id ^= uint64(header.Timestamp) * 0x9e3779b97f4a7c15
	id ^= uint64(header.Width)<<32 | uint64(header.Height)
	id &^= uint64(1) << 63
	if id == 0 {
		id = 1
	}
	return int64(id)
}

func inspectCastSegment(path string) (castSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return castSegment{}, fmt.Errorf("ptyhost: open transcript: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return castSegment{}, fmt.Errorf("ptyhost: inspect transcript: %w", err)
	}
	header, err := readCastHeader(f)
	if err != nil {
		_ = f.Close()
		return castSegment{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return castSegment{}, fmt.Errorf("ptyhost: seek transcript: %w", err)
	}
	r := newReplayReader(f, info.Size())
	n, readErr := io.Copy(io.Discard, r)
	closeErr := r.Close()
	if readErr != nil {
		return castSegment{}, readErr
	}
	if closeErr != nil {
		return castSegment{}, fmt.Errorf("ptyhost: close transcript: %w", closeErr)
	}
	if n > int64(^uint(0)>>1) {
		return castSegment{}, errors.New("ptyhost: transcript output exceeds platform limits")
	}
	if header.Incarnation == 0 {
		header.Incarnation = legacyCastIncarnation(info, header)
	}
	return castSegment{path: path, fileBytes: info.Size(), outputBytes: int(n), incarnation: header.Incarnation}, nil
}

func inspectCastHeader(path string) (castSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return castSegment{}, fmt.Errorf("ptyhost: open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return castSegment{}, fmt.Errorf("ptyhost: inspect transcript: %w", err)
	}
	header, err := readCastHeader(f)
	if err != nil {
		return castSegment{}, err
	}
	if header.Incarnation == 0 {
		header.Incarnation = legacyCastIncarnation(info, header)
	}
	return castSegment{path: path, fileBytes: info.Size(), incarnation: header.Incarnation}, nil
}

func readCastHeader(f *os.File) (castHeader, error) {
	line, err := bufio.NewReader(io.LimitReader(f, 1<<20)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return castHeader{}, fmt.Errorf("ptyhost: read transcript header: %w", err)
	}
	var header castHeader
	decodeErr := json.Unmarshal(bytes.TrimSpace(line), &header)
	if decodeErr != nil {
		return castHeader{}, fmt.Errorf("ptyhost: decode transcript header: %w", decodeErr)
	}
	if header.Version != 2 {
		return castHeader{}, fmt.Errorf("ptyhost: unsupported transcript version %d", header.Version)
	}
	if err == io.EOF && len(bytes.TrimSpace(line)) == 0 {
		return castHeader{}, errors.New("ptyhost: transcript has no header")
	}
	return header, nil
}

// snapshot flushes the complete output events already accepted by the session
// and opens an immutable replay through that boundary. The caller holds the
// session lock, so output arriving after the boundary is queued as live data.
func (w *castWriter) snapshot(prior []castSegment) (io.ReadCloser, int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, 0, errors.New("ptyhost: transcript is closed")
	}
	if err := w.flushStagedLocked(); err != nil {
		return nil, 0, fmt.Errorf("ptyhost: flush transcript replay: %w", err)
	}
	if err := w.bw.Flush(); err != nil {
		return nil, 0, fmt.Errorf("ptyhost: flush transcript replay: %w", err)
	}
	info, err := w.f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("ptyhost: inspect transcript replay: %w", err)
	}
	segments := append([]castSegment(nil), prior...)
	segments = append(segments, castSegment{
		path:        w.path,
		fileBytes:   info.Size(),
		outputBytes: w.outputBytes,
	})
	total := len(w.pending)
	for _, segment := range segments {
		total += segment.outputBytes
	}
	return openCastReplay(segments, w.pending), total, nil
}

// castReplay holds at most the segment currently being read open. Retained
// runs may span many server restarts, so opening every cast up front would let
// one attach consume an unbounded number of file descriptors.
type castReplay struct {
	segments []castSegment
	index    int
	current  *replayReader
	tail     *bytes.Reader
}

func openCastReplay(segments []castSegment, tail []byte) *castReplay {
	return &castReplay{
		segments: append([]castSegment(nil), segments...),
		tail:     bytes.NewReader(append([]byte(nil), tail...)),
	}
}
func (r *castReplay) Read(p []byte) (int, error) {
	for r.index < len(r.segments) {
		if r.current == nil {
			segment := r.segments[r.index]
			f, err := os.Open(segment.path)
			if err != nil {
				return 0, fmt.Errorf("ptyhost: open transcript: %w", err)
			}
			r.current = newReplayReader(f, segment.fileBytes)
		}
		n, err := r.current.Read(p)
		if n > 0 {
			return n, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		_ = r.current.Close()
		r.current = nil
		r.index++
	}
	return r.tail.Read(p)
}
func (r *castReplay) Close() error {
	if r.current == nil {
		return nil
	}
	err := r.current.Close()
	r.current = nil
	r.index = len(r.segments)
	return err
}

// castLine renders one asciicast event line relative to the recording's start.
func castLine(start time.Time, code string, data []byte) []byte {
	line := make([]byte, 0, len(data)+32)
	line = append(line, '[')
	line = strconv.AppendFloat(line, time.Since(start).Seconds(), 'f', 6, 64)
	line = append(line, ',', '"')
	line = append(line, code...)
	line = append(line, '"', ',')
	line = appendCastString(line, data)
	line = append(line, ']', '\n')
	return line
}
func (w *castWriter) close() error {
	w.lifetimeMu.Lock()
	defer w.lifetimeMu.Unlock()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.stop)
	if len(w.pending) > 0 {
		w.eventLocked("o", w.pending)
		w.pending = nil
	}
	_ = w.flushStagedLocked()
	_ = w.bw.Flush()
	w.mu.Unlock()
	_ = w.f.Sync()
	return w.f.Close()
}

// utf8Boundary returns the length of the longest prefix of p that does not
// end in the middle of a (possibly still incoming) UTF-8 sequence. Invalid
// sequences pass through whole.
func utf8Boundary(p []byte) int {
	n := len(p)
	for i := 1; i <= 3 && i <= n; i++ {
		b := p[n-i]
		if b < 0x80 {
			return n
		}
		if b >= 0xC0 { // leading byte: is the sequence complete?
			var need int
			switch {
			case b >= 0xF0:
				need = 4
			case b >= 0xE0:
				need = 3
			default:
				need = 2
			}
			if i < need {
				return n - i
			}
			return n
		}
	}
	return n
}

const hexDigits = "0123456789abcdef"

// appendCastString appends p to dst as a JSON string. Valid UTF-8 passes
// through unchanged; every byte that is not part of a valid UTF-8 sequence
// is written as the lone low surrogate escape \udcXX (0xDC00+byte,
// surrogate-escape convention), which keeps arbitrary binary PTY output
// lossless: decodeCastString maps the escapes back to the original bytes.
// Standard cast players render such escapes as replacement characters.
func appendCastString(dst, p []byte) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(p); {
		b := p[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\':
				dst = append(dst, '\\', b)
			case b >= 0x20:
				dst = append(dst, b)
			case b == '\n':
				dst = append(dst, '\\', 'n')
			case b == '\r':
				dst = append(dst, '\\', 'r')
			case b == '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = appendUnicodeEscape(dst, rune(b))
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			dst = appendUnicodeEscape(dst, 0xDC00+rune(b))
			i++
			continue
		}
		dst = append(dst, p[i:i+size]...)
		i += size
	}
	return append(dst, '"')
}

func appendUnicodeEscape(dst []byte, r rune) []byte {
	return append(dst, '\\', 'u',
		hexDigits[r>>12&0xF], hexDigits[r>>8&0xF], hexDigits[r>>4&0xF], hexDigits[r&0xF])
}

var errBadCastString = errors.New("ptyhost: malformed cast string")

// decodeCastString decodes a raw JSON string token as written by
// appendCastString back into the exact recorded bytes: lone low surrogate
// escapes \udc80–\udcff become the raw bytes they stand for, surrogate
// pairs and all standard escapes decode as usual.
func decodeCastString(tok []byte) ([]byte, error) {
	if len(tok) < 2 || tok[0] != '"' || tok[len(tok)-1] != '"' {
		return nil, errBadCastString
	}
	tok = tok[1 : len(tok)-1]
	out := make([]byte, 0, len(tok))
	for i := 0; i < len(tok); {
		b := tok[i]
		if b != '\\' {
			out = append(out, b)
			i++
			continue
		}
		if i+1 >= len(tok) {
			return nil, errBadCastString
		}
		switch e := tok[i+1]; e {
		case '"', '\\', '/':
			out = append(out, e)
			i += 2
		case 'b':
			out = append(out, '\b')
			i += 2
		case 'f':
			out = append(out, '\f')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'u':
			r, n, err := decodeUnicodeEscape(tok[i:])
			if err != nil {
				return nil, err
			}
			if r >= 0xDC80 && r <= 0xDCFF {
				out = append(out, byte(r&0xFF)) // surrogate-escaped raw byte
			} else {
				out = utf8.AppendRune(out, r)
			}
			i += n
		default:
			return nil, errBadCastString
		}
	}
	return out, nil
}

// decodeUnicodeEscape decodes a \uXXXX escape (combining surrogate pairs)
// at the start of tok, returning the rune and the bytes consumed.
func decodeUnicodeEscape(tok []byte) (rune, int, error) {
	r, err := hex4(tok, 2)
	if err != nil {
		return 0, 0, err
	}
	if r >= 0xD800 && r < 0xDC00 { // high surrogate: needs a low surrogate
		if len(tok) < 12 || tok[6] != '\\' || tok[7] != 'u' {
			return 0, 0, errBadCastString
		}
		lo, err := hex4(tok, 8)
		if err != nil || lo < 0xDC00 || lo >= 0xE000 {
			return 0, 0, errBadCastString
		}
		return 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00), 12, nil
	}
	return r, 6, nil
}

func hex4(tok []byte, at int) (rune, error) {
	if len(tok) < at+4 {
		return 0, errBadCastString
	}
	var r rune
	for _, c := range tok[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, errBadCastString
		}
	}
	return r, nil
}

// replayReader decodes a transcript cast file back into the raw terminal
// bytes: each [time,"o",<string>] line yields the exact bytes
// appendCastString recorded; the header, resize, and marker events are
// skipped. Decoding is line-incremental so a large transcript streams
// instead of loading whole.
type replayReader struct {
	f   *os.File
	br  *bufio.Reader
	buf []byte
	err error
}

func newReplayReader(f *os.File, fileBytes int64) *replayReader {
	return &replayReader{f: f, br: bufio.NewReader(io.LimitReader(f, fileBytes))}
}

func (r *replayReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 && r.err == nil {
		r.err = r.next()
	}
	if len(r.buf) == 0 {
		return 0, r.err
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// next decodes the next event line into r.buf (empty for non-output
// events), returning io.EOF once the transcript is exhausted.
func (r *replayReader) next() error {
	line, err := r.br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("ptyhost: read transcript: %w", err)
	}
	atEnd := errors.Is(err, io.EOF)
	done := func() error {
		if atEnd {
			return io.EOF
		}
		return nil
	}
	line = bytes.TrimSpace(line)
	// Blank tail or the cast v2 header object: nothing to emit.
	if len(line) == 0 || line[0] == '{' {
		return done()
	}
	var event []json.RawMessage
	if uerr := json.Unmarshal(line, &event); uerr != nil || len(event) < 3 {
		if atEnd {
			return io.EOF
		}
		return fmt.Errorf("ptyhost: malformed transcript event: %w", errBadCastString)
	}
	if string(event[1]) != `"o"` {
		return done()
	}
	data, derr := decodeCastString(event[2])
	if derr != nil {
		return fmt.Errorf("ptyhost: decode transcript output: %w", derr)
	}
	r.buf = data
	return done()
}

func (r *replayReader) Close() error { return r.f.Close() }
