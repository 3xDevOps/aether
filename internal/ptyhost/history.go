package ptyhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	defaultHistoryLimit     = 100
	maxHistoryLimit         = 200
	maxHistoryQueryBytes    = 256
	maxHistoryLineBytes     = 16 << 10
	maxHistoryEventBytes    = 1 << 20
	maxHistoryPageRawRead   = 8 << 20
	maxHistorySearchRawRead = 2 << 20
	// Allow bounded headroom for slow disks and race instrumentation.
	maxHistorySearchTime       = 1 * time.Second
	maxHistoryEvents           = 4096
	maxHistoryDecodeBytes      = 3 << 19
	maxHistoryPageTime         = 1 * time.Second
	maxHistoryDiscoveryTime    = 250 * time.Millisecond
	maxHistoryDirectoryEntries = 4096
	historyReverseBlock        = 64 << 10
	maxHistorySegmentsPerPage  = 128
)

var (
	ErrInvalidHistoryCursor  = errors.New("ptyhost: invalid terminal history cursor")
	ErrHistoryQueryTooLong   = errors.New("ptyhost: terminal history query is too long")
	ErrHistoryDiscoveryLimit = errors.New("ptyhost: terminal history discovery limit exceeded")
	errHistoryWorkDeadline   = errors.New("ptyhost: terminal history work deadline reached")

	// Search work is bounded per request and globally. Waiting callers stay
	// cancelable through their request context.
	historyReadSlots   = make(chan struct{}, 4)
	historySearchSlots = make(chan struct{}, 2)
)

type historyClock func() time.Time

type historyClockContextKey struct{}

type historyWorkDeadline struct {
	at      time.Time
	now     historyClock
	elapsed bool
}

func newHistoryWorkDeadline(ctx context.Context, window time.Duration) *historyWorkDeadline {
	now, _ := ctx.Value(historyClockContextKey{}).(historyClock)
	if now == nil {
		now = time.Now
	}
	return &historyWorkDeadline{at: now().Add(window), now: now}
}

func (d *historyWorkDeadline) check(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if d.elapsed {
		return true, nil
	}
	if !d.now().Before(d.at) {
		d.elapsed = true
		return true, nil
	}
	return false, nil
}

type HistoryLine struct {
	Cursor string
	Time   int64
	Text   string
}

type HistoryPage struct {
	Lines      []HistoryLine
	NextCursor string
	HasMore    bool
}

type historySegment struct {
	path        string
	stableID    string
	fileBytes   int64
	incarnation int64
	startedMS   int64
	current     bool
	loaded      bool
}
type historyPosition struct {
	segment int
	event   int64
	byte    int
	scan    bool
}

type historyCursor struct {
	Version     int    `json:"v"`
	RunID       string `json:"r"`
	SegmentID   string `json:"s"`
	Current     bool   `json:"c,omitempty"`
	Incarnation int64  `json:"i"`
	Event       int64  `json:"e"`
	Byte        int    `json:"b"`
	Query       string `json:"q"`
	Scan        bool   `json:"x,omitempty"`
}
type historyEvent struct {
	position historyPosition
	timeMS   int64
	data     []byte
}
type historyDecodedLine struct {
	origin     historyPosition
	timeMS     int64
	text       string
	suffixText string
}

// History returns a bounded chronological page from the newest matching
// terminal output. Both ordinary pages and searches reverse-read fixed disk
// windows; search cursors resume at the older boundary of the prior window.
func (h *Host) History(ctx context.Context, run domain.RunID, before, query string, limit int) (HistoryPage, error) {
	if err := validateRunID(run); err != nil {
		return HistoryPage{}, fmt.Errorf("%w: %q", err, run)
	}
	if len(query) > maxHistoryQueryBytes {
		return HistoryPage{}, ErrHistoryQueryTooLong
	}
	if limit == 0 {
		limit = defaultHistoryLimit
	}
	if limit < 0 {
		return HistoryPage{}, errors.New("ptyhost: terminal history limit must not be negative")
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	path := h.transcriptPath(RunSession(run))
	var supplied *historyCursor
	if before != "" {
		cursor, err := authenticateHistoryCursor(h.historyCursorKey[:], before, run, query, path)
		if err != nil {
			return HistoryPage{}, err
		}
		supplied = &cursor
	}
	select {
	case historyReadSlots <- struct{}{}:
		defer func() { <-historyReadSlots }()
	case <-ctx.Done():
		return HistoryPage{}, ctx.Err()
	}
	if query != "" {
		select {
		case historySearchSlots <- struct{}{}:
			defer func() { <-historySearchSlots }()
		case <-ctx.Done():
			return HistoryPage{}, ctx.Err()
		}
	}
	lifecycle := acquireTranscriptLifecycle(path)
	lifecycle.entry.mu.RLock()
	defer func() {
		lifecycle.entry.mu.RUnlock()
		lifecycle.release()
	}()
	if session := h.lookup(RunSession(run)); session != nil {
		if err := session.flushLiveTranscript(); err != nil {
			return HistoryPage{}, err
		}
	}
	set := ""
	var segments []historySegment
	discovery := historySegmentDiscovery{}
	var cutoff historyPosition
	var err error
	if supplied == nil {
		segments, err = loadNewestHistorySegments(ctx, path, &discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		cutoff = historyPosition{segment: 0, event: segments[0].fileBytes}
	} else {
		var segment historySegment
		cutoff, segment, err = resolveHistoryCursor(ctx, *supplied, path)
		if err != nil {
			return HistoryPage{}, err
		}
		segments = []historySegment{segment}
		cutoff.segment = 0
	}
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	if query != "" {
		return searchHistory(ctx, h.historyCursorKey[:], run, query, limit, cutoff, set, path, segments, &discovery)
	}
	return pageHistory(ctx, h.historyCursorKey[:], run, limit, cutoff, set, path, segments, &discovery)
}

func loadNewestHistorySegments(ctx context.Context, path string, discovery *historySegmentDiscovery) ([]historySegment, error) {
	segment := historySegment{path: path, current: true}
	if err := loadHistorySegment(&segment); err == nil {
		return []historySegment{segment}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	segment, ok, err := discovery.nextSegment(ctx, nil, path, path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	return []historySegment{segment}, nil
}

func historySegmentIncarnation(path, name string) (int64, bool) {
	return castSegmentIncarnation(path, name)
}

func historySegmentName(path, name string) bool {
	if name == filepath.Base(path) {
		return true
	}
	_, ok := historySegmentIncarnation(path, name)
	return ok
}

func historySegmentOrder(rootPath, name string) (int64, bool) {
	if order, ok := historySegmentIncarnation(rootPath, name); ok {
		return order, true
	}
	return legacyCastSegmentIncarnation(rootPath, name)
}

// historySegmentOlder reports whether left sorts before right in the newest-first
// archive walk. Equal timestamps use the path so two archives rotated in the
// same nanosecond are both reachable.
func historySegmentOlder(order int64, path string, before int64, beforePath string) bool {
	if order != before {
		return order < before
	}
	return path < beforePath
}

// readableLegacyHistoryArchive accepts a pre-delimiter decimal archive only
// when its header is readable and time-ordered. Unreadable names are skipped
// so a stray file cannot block older history; a sibling run's current cast
// fails the ordering check and is not treated as this run's archive.
func readableLegacyHistoryArchive(path string, rotation, currentStart int64) bool {
	header, err := newestCastHeader(path)
	if err != nil {
		return false
	}
	start := castHeaderStart(header)
	if start == 0 {
		return false
	}
	return start <= rotation && (currentStart == 0 || rotation <= currentStart)
}

func stableHistorySegmentID(path string, incarnation int64) string {
	return stableCastSegmentName(path, incarnation)
}

type historyDiscoveredSegment struct {
	path  string
	order int64
}

type historySegmentDiscovery struct {
	initialized bool
	paths       []string
	next        int
}

func (d *historySegmentDiscovery) initialize(ctx context.Context, work *historyWorkDeadline, rootPath, currentPath string) error {
	if d.initialized {
		return nil
	}
	dir, err := os.Open(filepath.Dir(rootPath))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	deadline := time.Now().Add(maxHistoryDiscoveryTime)
	before, bounded := historySegmentOrder(rootPath, filepath.Base(currentPath))
	if filepath.Base(currentPath) != filepath.Base(rootPath) && !bounded {
		return ErrInvalidHistoryCursor
	}
	currentStart := currentCastStart(rootPath)
	discovered := make([]historyDiscoveredSegment, 0, 16)
	entriesSeen := 0
	legacyHeaders := 0
	for {
		if work != nil {
			stopped, err := work.check(ctx)
			if err != nil {
				return err
			}
			if stopped {
				return errHistoryWorkDeadline
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return ErrHistoryDiscoveryLimit
		}
		batch := 64
		if remaining := maxHistoryDirectoryEntries - entriesSeen; remaining < batch {
			batch = remaining
		}
		if batch == 0 {
			entries, readErr := dir.ReadDir(1)
			if len(entries) != 0 || readErr == nil {
				return ErrHistoryDiscoveryLimit
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
		entries, readErr := dir.ReadDir(batch)
		entriesSeen += len(entries)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			order, stable := historySegmentIncarnation(rootPath, entry.Name())
			if !stable {
				rotation, legacy := legacyCastSegmentIncarnation(rootPath, entry.Name())
				if !legacy {
					continue
				}
				legacyHeaders++
				if legacyHeaders > maxLegacyCastHeaderInspections {
					return ErrHistoryDiscoveryLimit
				}
				full := filepath.Join(filepath.Dir(rootPath), entry.Name())
				if !readableLegacyHistoryArchive(full, rotation, currentStart) {
					continue
				}
				order = rotation
			}
			full := filepath.Join(filepath.Dir(rootPath), entry.Name())
			if bounded && !historySegmentOlder(order, full, before, currentPath) {
				continue
			}
			discovered = append(discovered, historyDiscoveredSegment{path: full, order: order})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	sort.Slice(discovered, func(i, j int) bool {
		if discovered[i].order != discovered[j].order {
			return discovered[i].order > discovered[j].order
		}
		return discovered[i].path > discovered[j].path
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Now().After(deadline) {
		return ErrHistoryDiscoveryLimit
	}
	d.paths = make([]string, len(discovered))
	for i := range discovered {
		d.paths[i] = discovered[i].path
	}
	d.initialized = true
	return nil
}

func (d *historySegmentDiscovery) nextSegment(ctx context.Context, work *historyWorkDeadline, rootPath, currentPath string) (historySegment, bool, error) {
	if err := d.initialize(ctx, work, rootPath, currentPath); err != nil {
		return historySegment{}, false, err
	}
	if d.next == len(d.paths) {
		return historySegment{}, false, nil
	}
	if work != nil {
		stopped, err := work.check(ctx)
		if err != nil {
			return historySegment{}, false, err
		}
		if stopped {
			return historySegment{}, false, errHistoryWorkDeadline
		}
	}
	segment := historySegment{path: d.paths[d.next]}
	name := filepath.Base(segment.path)
	expectedIncarnation, stable := historySegmentIncarnation(rootPath, name)
	rotation, legacy := int64(0), false
	if !stable {
		rotation, legacy = legacyCastSegmentIncarnation(rootPath, name)
	}
	d.next++
	if !stable && !legacy {
		return historySegment{}, false, errors.New("ptyhost: invalid transcript segment identity")
	}
	if err := loadHistorySegment(&segment); err != nil {
		return historySegment{}, false, err
	}
	if stable && segment.incarnation != expectedIncarnation {
		return historySegment{}, false, errors.New("ptyhost: transcript segment identity mismatch")
	}
	if legacy && !readableLegacyHistoryArchive(segment.path, rotation, currentCastStart(rootPath)) {
		return historySegment{}, false, errors.New("ptyhost: transcript segment identity mismatch")
	}
	return segment, true, nil
}

func loadHistorySegment(segment *historySegment) error {
	if segment.loaded {
		return nil
	}
	f, err := os.Open(segment.path)
	if err != nil {
		return fmt.Errorf("ptyhost: open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("ptyhost: inspect transcript: %w", err)
	}
	header, err := readCastHeader(f)
	if err != nil {
		return err
	}
	if header.Incarnation == 0 {
		header.Incarnation = legacyCastIncarnation(info, header)
	}
	segment.fileBytes = info.Size()
	segment.incarnation = header.Incarnation
	segment.startedMS = header.Timestamp * 1000
	if segment.current {
		segment.stableID = stableHistorySegmentID(segment.path, segment.incarnation)
	} else {
		segment.stableID = filepath.Base(segment.path)
	}
	segment.loaded = true
	return nil
}
func historyQueryHash(query string) string {
	sum := sha256.Sum256([]byte(query))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func foldHistoryString(value string) string {
	value = norm.NFC.String(value)
	return norm.NFC.String(cases.Fold().String(value))
}

func encodeHistoryCursor(key []byte, run domain.RunID, query, _ string, segments []historySegment, p historyPosition) string {
	segment := segments[p.segment]
	payload, _ := json.Marshal(historyCursor{
		Version: 5, RunID: string(run), SegmentID: segment.stableID, Current: segment.current,
		Incarnation: segment.incarnation, Event: p.event, Byte: p.byte,
		Query: historyQueryHash(query), Scan: p.scan,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
}

// authenticateHistoryCursor rejects forged cursors before any filesystem
// lookup. In particular, attacker-chosen segment names cannot trigger archive
// discovery or header reads.
func authenticateHistoryCursor(key []byte, encoded string, run domain.RunID, query, path string) (historyCursor, error) {
	if encoded == "" || base64.RawURLEncoding.DecodedLen(len(encoded)) > 1024 {
		return historyCursor{}, ErrInvalidHistoryCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) <= sha256.Size {
		return historyCursor{}, ErrInvalidHistoryCursor
	}
	payload, signature := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return historyCursor{}, ErrInvalidHistoryCursor
	}
	var cursor historyCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return historyCursor{}, ErrInvalidHistoryCursor
	}
	expectedQuery := historyQueryHash(query)
	segmentIncarnation, stableSegment := historySegmentIncarnation(path, cursor.SegmentID)
	_, legacySegment := legacyCastSegmentIncarnation(path, cursor.SegmentID)
	// A legacy decimal name is not the header incarnation. The signature still
	// binds the header incarnation, which resolveHistoryCursor checks after
	// this filename-only authentication.
	if cursor.Version != 5 || cursor.RunID != string(run) ||
		len(cursor.Query) != len(expectedQuery) || subtle.ConstantTimeCompare([]byte(cursor.Query), []byte(expectedQuery)) != 1 ||
		cursor.Incarnation <= 0 || cursor.Event < 0 || cursor.Byte < 0 ||
		(cursor.Scan && cursor.Byte != 0) ||
		(!stableSegment && (!legacySegment || cursor.Current)) ||
		(stableSegment && segmentIncarnation != cursor.Incarnation) {
		return historyCursor{}, ErrInvalidHistoryCursor
	}
	if cursor.Current && cursor.SegmentID != stableHistorySegmentID(path, cursor.Incarnation) {
		return historyCursor{}, ErrInvalidHistoryCursor
	}
	return cursor, nil
}

func resolveHistoryCursor(ctx context.Context, cursor historyCursor, path string) (historyPosition, historySegment, error) {
	invalid := func() (historyPosition, historySegment, error) {
		return historyPosition{}, historySegment{}, ErrInvalidHistoryCursor
	}
	candidates := []string{filepath.Join(filepath.Dir(path), cursor.SegmentID)}
	if cursor.Current {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return historyPosition{}, historySegment{}, err
		}
		segment := historySegment{path: candidate, current: candidate == path}
		if err := loadHistorySegment(&segment); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return invalid()
		}
		if segment.incarnation != cursor.Incarnation || segment.stableID != cursor.SegmentID || cursor.Event > segment.fileBytes {
			return invalid()
		}
		return historyPosition{event: cursor.Event, byte: cursor.Byte, scan: cursor.Scan}, segment, nil
	}
	return invalid()
}

func parseHistoryEvent(raw []byte, segment int, offset int64, startedMS int64) (historyEvent, bool) {
	var fields []json.RawMessage
	if len(raw) > maxHistoryEventBytes || json.Unmarshal(bytes.TrimSpace(raw), &fields) != nil || len(fields) < 3 {
		return historyEvent{}, false
	}
	var seconds float64
	var code string
	if json.Unmarshal(fields[0], &seconds) != nil || json.Unmarshal(fields[1], &code) != nil || code != "o" {
		return historyEvent{}, false
	}
	data, err := decodeCastString(fields[2])
	if err != nil {
		return historyEvent{}, false
	}
	return historyEvent{
		position: historyPosition{segment: segment, event: offset},
		timeMS:   startedMS + int64(seconds*1000),
		data:     data,
	}, true
}

type historyReadCounter struct {
	r io.Reader
	n int
}

func (r *historyReadCounter) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

func readHistoryEventAt(ctx context.Context, segment historySegment, index int, offset int64) (historyEvent, int, error) {
	if err := ctx.Err(); err != nil {
		return historyEvent{}, 0, err
	}
	if offset < 0 || offset >= segment.fileBytes {
		return historyEvent{}, 0, ErrInvalidHistoryCursor
	}
	f, err := os.Open(segment.path)
	if err != nil {
		return historyEvent{}, 0, err
	}
	defer func() { _ = f.Close() }()
	remaining := segment.fileBytes - offset
	if remaining > maxHistoryEventBytes+1 {
		remaining = maxHistoryEventBytes + 1
	}
	counter := historyReadCounter{r: io.NewSectionReader(f, offset, remaining)}
	raw, err := bufio.NewReader(&counter).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return historyEvent{}, counter.n, err
	}
	if len(raw) > maxHistoryEventBytes+1 || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return historyEvent{}, counter.n, ErrInvalidHistoryCursor
	}
	event, ok := parseHistoryEvent(bytes.TrimSuffix(raw, []byte{'\n'}), index, offset, segment.startedMS)
	if !ok {
		return historyEvent{}, counter.n, ErrInvalidHistoryCursor
	}
	return event, counter.n, nil
}

type historyReverseReader struct {
	ctx       context.Context
	work      *historyWorkDeadline
	budget    *int
	maxEvents int
	events    int
	f         *os.File
	path      string
	buf       []byte
	base      int64
	cursor    int64
	oversized bool
}

func (r *historyReverseReader) close() {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
}

func (r *historyReverseReader) deadlineStopped() (bool, error) {
	return r.work.check(r.ctx)
}

func (r *historyReverseReader) stopped() (bool, error) {
	elapsed, err := r.deadlineStopped()
	if err != nil {
		return false, err
	}
	return elapsed || *r.budget <= 0 || r.events >= r.maxEvents, nil
}

func (r *historyReverseReader) reset(segment historySegment, position int64) error {
	r.close()
	f, err := os.Open(segment.path)
	if err != nil {
		return err
	}
	r.f = f
	r.path = segment.path
	r.buf = nil
	r.base = position
	r.cursor = position
	r.oversized = false
	return nil
}

func (r *historyReverseReader) fill() (bool, error) {
	stopped, err := r.stopped()
	if err != nil {
		return false, err
	}
	if r.base == 0 || stopped {
		return false, nil
	}
	want := int64(historyReverseBlock)
	if want > r.base {
		want = r.base
	}
	if int(want) > *r.budget {
		want = int64(*r.budget)
	}
	start := r.base - want
	block := make([]byte, int(want))
	if _, err := r.f.ReadAt(block, start); err != nil {
		return false, err
	}
	*r.budget -= len(block)
	r.buf = append(block, r.buf...)
	r.base = start
	return true, nil
}

// previous returns one raw record while retaining a block full of preceding
// records for subsequent calls. Thus newline-dense casts do not cause one
// open and one block reread per event.
func (r *historyReverseReader) previous(segment historySegment, index int, position *int64) (historyEvent, bool, bool, error) {
	stopped, err := r.stopped()
	if err != nil {
		return historyEvent{}, false, false, err
	}
	if stopped {
		return historyEvent{}, false, true, nil
	}
	if r.f == nil || r.path != segment.path || r.cursor != *position {
		if err = r.reset(segment, *position); err != nil {
			return historyEvent{}, false, false, err
		}
	}
	eventEnd := r.cursor
	if eventEnd <= 0 {
		return historyEvent{}, false, false, nil
	}
	for len(r.buf) == 0 {
		filled, fillErr := r.fill()
		if fillErr != nil {
			return historyEvent{}, false, false, fillErr
		}
		if !filled {
			stopped, err = r.stopped()
			return historyEvent{}, false, stopped, err
		}
	}
	stopped, err = r.deadlineStopped()
	if err != nil {
		return historyEvent{}, false, false, err
	}
	if stopped {
		return historyEvent{}, false, true, nil
	}
	if r.buf[len(r.buf)-1] == '\n' {
		r.buf = r.buf[:len(r.buf)-1]
		r.cursor--
	}
	for {
		stopped, err = r.deadlineStopped()
		if err != nil {
			return historyEvent{}, false, false, err
		}
		if stopped {
			return historyEvent{}, false, true, nil
		}
		if newline := bytes.LastIndexByte(r.buf, '\n'); newline >= 0 {
			lineStart := r.base + int64(newline) + 1
			raw := r.buf[newline+1:]
			r.buf = r.buf[:newline+1]
			r.cursor = lineStart
			*position = lineStart
			r.events++
			if r.oversized || len(raw) > maxHistoryEventBytes {
				r.oversized = false
				return historyEvent{}, false, false, nil
			}
			event, ok := parseHistoryEvent(raw, index, lineStart, segment.startedMS)
			return event, ok, false, nil
		}
		if r.base == 0 {
			raw := r.buf
			r.buf = nil
			r.cursor = 0
			*position = 0
			r.events++
			if r.oversized || len(raw) > maxHistoryEventBytes {
				r.oversized = false
				return historyEvent{}, false, false, nil
			}
			event, ok := parseHistoryEvent(raw, index, 0, segment.startedMS)
			return event, ok, false, nil
		}
		if len(r.buf) > maxHistoryEventBytes {
			r.oversized = true
			r.buf = nil
			r.cursor = r.base
		}
		filled, err := r.fill()
		if err != nil {
			return historyEvent{}, false, false, err
		}
		if !filled {
			if r.oversized {
				*position = r.base
				r.buf = nil
				r.cursor = r.base
				r.oversized = false
			} else {
				*position = eventEnd
			}
			stopped, err := r.stopped()
			return historyEvent{}, false, stopped, err
		}
	}
}

func moveToPreviousHistorySegment(ctx context.Context, work *historyWorkDeadline, path string, segments *[]historySegment, index, discovered int, discovery *historySegmentDiscovery) (int, bool, bool, error) {
	if discovered >= maxHistorySegmentsPerPage {
		return index, false, true, nil
	}
	segment, ok, err := discovery.nextSegment(ctx, work, path, (*segments)[index].path)
	if errors.Is(err, errHistoryWorkDeadline) {
		return index, false, true, nil
	}
	if err != nil || !ok {
		return index, ok, false, err
	}
	*segments = append(*segments, segment)
	return len(*segments) - 1, true, false, nil
}

type historyPageWindow struct {
	decoded      []historyDecodedLine
	decodedCount int
	newlineFree  bool
}

func decodeHistoryPageWindow(ctx context.Context, work *historyWorkDeadline, events []historyEvent, capacity int) (historyPageWindow, error) {
	ring := make([]historyDecodedLine, capacity)
	decodedCount, writeAt := 0, 0
	decoder := newHistoryDecoder(ctx, work, func(line historyDecodedLine) {
		ring[writeAt] = line
		writeAt = (writeAt + 1) % capacity
		decodedCount++
	})
	for i := len(events) - 1; i >= 0; i-- {
		if err := decoder.feed(events[i]); err != nil {
			return historyPageWindow{}, err
		}
	}
	completeLines := decodedCount
	if err := decoder.finish(); err != nil {
		return historyPageWindow{}, err
	}
	retained := decodedCount
	if retained > capacity {
		retained = capacity
	}
	decoded := make([]historyDecodedLine, 0, retained)
	start := 0
	if decodedCount >= capacity {
		start = writeAt
	}
	for i := 0; i < retained; i++ {
		decoded = append(decoded, ring[(start+i)%capacity])
	}
	unterminated := decodedCount > completeLines
	if unterminated && len(decoded) > 0 && decoded[len(decoded)-1].suffixText != "" {
		decoded[len(decoded)-1].text = decoded[len(decoded)-1].suffixText
	}
	newlineFree := completeLines == 0 && unterminated
	return historyPageWindow{
		decoded:      decoded,
		decodedCount: decodedCount,
		newlineFree:  newlineFree,
	}, nil
}

func buildHistoryPage(key []byte, run domain.RunID, limit int, window historyPageWindow, reachedOldest, discoveryStopped, workStopped bool, budget int, segmentIndex int, position int64, set string, segments []historySegment) HistoryPage {
	decoded := window.decoded
	validCount := window.decodedCount
	if !reachedOldest && validCount > 0 && !window.newlineFree {
		validCount--
		if window.decodedCount <= limit+1 && len(decoded) > 0 {
			decoded = decoded[1:]
		}
	}
	if len(decoded) > limit {
		decoded = decoded[len(decoded)-limit:]
	}
	budgetStopped := !reachedOldest && (budget <= 0 || discoveryStopped || workStopped)
	hasMore := validCount > limit || !reachedOldest || budgetStopped
	page := HistoryPage{Lines: make([]HistoryLine, 0, len(decoded)), HasMore: hasMore}
	for _, line := range decoded {
		page.Lines = append(page.Lines, HistoryLine{
			Cursor: encodeHistoryCursor(key, run, "", set, segments, line.origin),
			Time:   line.timeMS,
			Text:   line.text,
		})
	}
	if hasMore && len(page.Lines) > 0 {
		page.NextCursor = page.Lines[0].Cursor
	} else if hasMore {
		page.NextCursor = encodeHistoryCursor(key, run, "", set, segments, historyPosition{
			segment: segmentIndex, event: position, scan: true,
		})
	}
	return page
}

func historyDeadlinePage(key []byte, run domain.RunID, query, set string, segments []historySegment, cutoff historyPosition) HistoryPage {
	return HistoryPage{
		NextCursor: encodeHistoryCursor(key, run, query, set, segments, cutoff),
		HasMore:    true,
	}
}

func pageHistory(ctx context.Context, key []byte, run domain.RunID, limit int, cutoff historyPosition, set, path string, segments []historySegment, discovery *historySegmentDiscovery) (HistoryPage, error) {
	budget := maxHistoryPageRawRead
	work := newHistoryWorkDeadline(ctx, maxHistoryPageTime)
	reader := historyReverseReader{ctx: ctx, work: work, budget: &budget, maxEvents: maxHistoryEvents}
	defer reader.close()
	deadlinePage := func() HistoryPage {
		return historyDeadlinePage(key, run, "", set, segments, cutoff)
	}
	stopped, err := work.check(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	if stopped {
		return deadlinePage(), nil
	}
	events := make([]historyEvent, 0, 128)
	reachedOldest := false
	discoveryStopped := false
	workStopped := false
	discovered := 0

	segmentIndex := cutoff.segment
	position := cutoff.event
	if cutoff.byte > 0 && !cutoff.scan {
		var event historyEvent
		var used int
		event, used, err = readHistoryEventAt(ctx, segments[segmentIndex], segmentIndex, cutoff.event)
		budget -= used
		if budget < 0 {
			budget = 0
		}
		if err != nil || cutoff.byte > len(event.data) {
			if err == nil {
				err = ErrInvalidHistoryCursor
			}
			return HistoryPage{}, err
		}
		event.data = event.data[:cutoff.byte]
		events = append(events, event)
		stopped, err = work.check(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			return deadlinePage(), nil
		}
	}

	// Stop once the newest window can fill the page. A newline-free tail must
	// not drag the whole raw budget through the decoder: the page only keeps a
	// bounded suffix, and the resume cursor continues before that suffix.
	// A finished line does not count toward the byte cap. Otherwise one long
	// line would hide older lines, while a newline-free tail would still force
	// the decoder through the whole raw budget.
	neededNewlines := limit + 1
	outputBytes, newlines := 0, 0
	scanSegment := func() error {
		for {
			event, ok, readStopped, readErr := reader.previous(segments[segmentIndex], segmentIndex, &position)
			if readErr != nil {
				return readErr
			}
			if readStopped {
				workStopped = true
				return nil
			}
			if ok {
				events = append(events, event)
				newlines += bytes.Count(event.data, []byte{'\n'})
				if newlines == 0 {
					outputBytes += len(event.data)
					if outputBytes >= maxHistoryLineBytes {
						return nil
					}
				}
				if newlines >= neededNewlines {
					return nil
				}
			}
			if !ok && position == 0 {
				return nil
			}
		}
	}

	capacity := limit + 1
	if err = scanSegment(); err != nil {
		return HistoryPage{}, err
	}
	if work.elapsed {
		return deadlinePage(), nil
	}
	var window historyPageWindow
	for {
		window, err = decodeHistoryPageWindow(ctx, work, events, capacity)
		if errors.Is(err, errHistoryWorkDeadline) {
			return deadlinePage(), nil
		}
		if err != nil {
			return HistoryPage{}, err
		}
		// position > 0 means this segment still has older output. Return the
		// bounded window instead of skipping ahead to an older incarnation.
		if workStopped || window.decodedCount > limit || position > 0 {
			return buildHistoryPage(key, run, limit, window, false, false, workStopped, budget, segmentIndex, position, set, segments), nil
		}
		var next int
		var found bool
		next, found, stopped, err = moveToPreviousHistorySegment(ctx, work, path, &segments, segmentIndex, discovered, discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
		if stopped {
			discoveryStopped = true
			break
		}
		if !found {
			reachedOldest = true
			break
		}
		discovered++
		segmentIndex = next
		position = segments[segmentIndex].fileBytes
		if err = scanSegment(); err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
	}
	window, err = decodeHistoryPageWindow(ctx, work, events, capacity)
	if errors.Is(err, errHistoryWorkDeadline) {
		return deadlinePage(), nil
	}
	if err != nil {
		return HistoryPage{}, err
	}
	return buildHistoryPage(key, run, limit, window, reachedOldest, discoveryStopped, workStopped, budget, segmentIndex, position, set, segments), nil
}

func searchHistory(ctx context.Context, key []byte, run domain.RunID, query string, limit int, cutoff historyPosition, set, path string, segments []historySegment, discovery *historySegmentDiscovery) (HistoryPage, error) {
	overlapBudget := maxHistoryEventBytes + 1
	budget := maxHistorySearchRawRead - overlapBudget
	work := newHistoryWorkDeadline(ctx, maxHistorySearchTime)
	reader := historyReverseReader{ctx: ctx, work: work, budget: &budget, maxEvents: maxHistoryEvents}
	defer reader.close()
	deadlinePage := func() HistoryPage {
		return historyDeadlinePage(key, run, query, set, segments, cutoff)
	}
	stopped, err := work.check(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	if stopped {
		return deadlinePage(), nil
	}
	events := make([]historyEvent, 0, 128)
	decodedBytes := 0
	segmentIndex := cutoff.segment
	position := cutoff.event
	reachedOldest := false
	discoveryStopped := false
	workStopped := false
	discovered := 0

	if cutoff.byte > 0 && !cutoff.scan {
		event, used, err := readHistoryEventAt(ctx, segments[segmentIndex], segmentIndex, cutoff.event)
		budget -= used
		if budget < 0 {
			overlapBudget += budget
			budget = 0
		}
		if overlapBudget < 0 {
			overlapBudget = 0
		}
		if err != nil || cutoff.byte > len(event.data) {
			if err == nil {
				err = ErrInvalidHistoryCursor
			}
			return HistoryPage{}, err
		}
		event.data = event.data[:cutoff.byte]
		events = append(events, event)
		decodedBytes += len(event.data)
		stopped, err := work.check(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			return deadlinePage(), nil
		}
	}

	for {
		beforeEvent := position
		event, ok, stopped, err := reader.previous(segments[segmentIndex], segmentIndex, &position)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			workStopped = true
			break
		}
		if ok {
			if decodedBytes+len(event.data) > maxHistoryDecodeBytes {
				position = beforeEvent
				workStopped = true
				break
			}
			events = append(events, event)
			decodedBytes += len(event.data)
			continue
		}
		if position > 0 {
			continue
		}
		next, found, stopped, err := moveToPreviousHistorySegment(ctx, work, path, &segments, segmentIndex, discovered, discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
		if stopped {
			discoveryStopped = true
			break
		}
		if !found {
			reachedOldest = true
			break
		}
		discovered++
		segmentIndex = next
		position = segments[segmentIndex].fileBytes
	}
	if work.elapsed {
		return deadlinePage(), nil
	}

	boundary := historyPosition{segment: segmentIndex, event: position, scan: true}
	boundaryComplete := reachedOldest
	reader.budget = &overlapBudget
	for !boundaryComplete && !discoveryStopped {
		stopped, err := work.check(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			return deadlinePage(), nil
		}
		if len(events) > 0 {
			oldest := &events[len(events)-1]
			if newline := bytes.LastIndexByte(oldest.data, '\n'); newline >= 0 {
				boundary = oldest.position
				boundary.byte = newline + 1
				oldest.position.byte = newline + 1
				oldest.data = oldest.data[newline+1:]
				boundaryComplete = true
				break
			}
		}
		beforeEvent := position
		event, ok, stopped, err := reader.previous(segments[segmentIndex], segmentIndex, &position)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			workStopped = true
			break
		}
		if ok {
			if decodedBytes+len(event.data) > maxHistoryDecodeBytes {
				position = beforeEvent
				workStopped = true
				break
			}
			events = append(events, event)
			decodedBytes += len(event.data)
			continue
		}
		if position > 0 {
			continue
		}
		next, found, stopped, err := moveToPreviousHistorySegment(ctx, work, path, &segments, segmentIndex, discovered, discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
		if stopped {
			discoveryStopped = true
			break
		}
		if !found {
			reachedOldest = true
			boundaryComplete = true
			break
		}
		discovered++
		segmentIndex = next
		position = segments[segmentIndex].fileBytes
	}
	if work.elapsed {
		return deadlinePage(), nil
	}
	if !boundaryComplete {
		boundary = historyPosition{segment: segmentIndex, event: position, scan: true}
	}

	needle := foldHistoryString(query)
	matchRing := make([]historyDecodedLine, limit+1)
	matchCount, matchWrite := 0, 0
	decoder := newHistoryDecoder(ctx, work, func(line historyDecodedLine) {
		if !strings.Contains(foldHistoryString(line.text), needle) {
			return
		}
		matchRing[matchWrite] = line
		matchWrite = (matchWrite + 1) % len(matchRing)
		matchCount++
	})
	for i := len(events) - 1; i >= 0; i-- {
		if err := decoder.feed(events[i]); err != nil {
			if errors.Is(err, errHistoryWorkDeadline) {
				return deadlinePage(), nil
			}
			return HistoryPage{}, err
		}
	}
	if err := decoder.finish(); err != nil {
		if errors.Is(err, errHistoryWorkDeadline) {
			return deadlinePage(), nil
		}
		return HistoryPage{}, err
	}
	retained := matchCount
	if retained > len(matchRing) {
		retained = len(matchRing)
	}
	matches := make([]historyDecodedLine, 0, retained)
	start := 0
	if matchCount >= len(matchRing) {
		start = matchWrite
	}
	for i := 0; i < retained; i++ {
		matches = append(matches, matchRing[(start+i)%len(matchRing)])
	}
	returned := matches
	if matchCount > limit {
		returned = matches[len(matches)-limit:]
	}
	page := HistoryPage{
		Lines:   make([]HistoryLine, 0, len(returned)),
		HasMore: matchCount > limit || !reachedOldest || workStopped || discoveryStopped,
	}
	for _, line := range returned {
		page.Lines = append(page.Lines, HistoryLine{
			Cursor: encodeHistoryCursor(key, run, query, set, segments, line.origin),
			Time:   line.timeMS,
			Text:   line.text,
		})
	}
	if matchCount > limit {
		page.NextCursor = page.Lines[0].Cursor
	} else if page.HasMore {
		page.NextCursor = encodeHistoryCursor(key, run, query, set, segments, boundary)
	}
	return page, nil
}

type historyDecoder struct {
	ctx    context.Context
	work   *historyWorkDeadline
	err    error
	onLine func(historyDecodedLine)

	cells          []rune
	column         int
	lineOrigin     *historyPosition
	lineTime       int64
	dirty          bool
	ansi           byte
	csi            [64]byte
	csiLen         int
	utf8Buf        []byte
	utf8Origin     historyPosition
	utf8Time       int64
	suffixRunes    []rune
	suffixHead     int
	suffixBytes    int
	suffixOverflow bool
}

func newHistoryDecoder(ctx context.Context, work *historyWorkDeadline, onLine func(historyDecodedLine)) *historyDecoder {
	return &historyDecoder{ctx: ctx, work: work, onLine: onLine}
}

func (d *historyDecoder) checkWork(_ int) error {
	if d.err != nil {
		return d.err
	}
	stopped, err := d.work.check(d.ctx)
	if err != nil {
		d.err = err
		return err
	}
	if stopped {
		d.err = errHistoryWorkDeadline
		return d.err
	}
	return nil
}

func (d *historyDecoder) feed(event historyEvent) error {
	if err := d.checkWork(0); err != nil {
		return err
	}
	for i, b := range event.data {
		if i != 0 && i&4095 == 0 {
			if err := d.checkWork(0); err != nil {
				return err
			}
		}
		position := event.position
		position.byte += i
		d.feedByte(b, position, event.timeMS)
		if d.err != nil {
			return d.err
		}
	}
	return nil
}

func (d *historyDecoder) setOrigin(position historyPosition, timeMS int64) {
	if d.lineOrigin == nil {
		copy := position
		d.lineOrigin = &copy
		d.lineTime = timeMS
	}
}

func (d *historyDecoder) startCSI() {
	d.ansi = 2
	d.csiLen = 0
}

func (d *historyDecoder) feedANSI(b byte) {
	switch d.ansi {
	case 1: // ESC followed by intermediates and one final byte.
		switch {
		case b == '[':
			d.startCSI()
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			d.ansi = 3
		case b >= 0x20 && b <= 0x2f:
			d.ansi = 5
		case b == 0x1b:
			d.ansi = 1
		default:
			d.ansi = 0
		}
	case 2:
		switch {
		case b >= 0x40 && b <= 0x7e:
			d.applyCSI(b)
			d.ansi = 0
		case b >= 0x20 && b <= 0x3f:
			if d.csiLen < len(d.csi) {
				d.csi[d.csiLen] = b
				d.csiLen++
			}
		case b == 0x1b:
			d.ansi = 1
		default:
			d.ansi = 0
		}
	case 3: // OSC/DCS/SOS/PM/APC through BEL or ST.
		switch b {
		case 0x07:
			d.ansi = 0
		case 0x1b:
			d.ansi = 4
		case 0xc2:
			d.ansi = 6
		}
	case 4:
		if b == '\\' {
			d.ansi = 0
		} else if b != 0x1b {
			d.ansi = 3
		}
	case 5:
		if b < 0x20 || b > 0x2f {
			d.ansi = 0
		}
	case 6: // UTF-8 encoding of C1 ST while inside a control string.
		if b == 0x9c {
			d.ansi = 0
		} else {
			d.ansi = 3
		}
	}
}

func (d *historyDecoder) feedByte(b byte, position historyPosition, timeMS int64) {
	d.setOrigin(position, timeMS)
	if d.ansi != 0 {
		d.feedANSI(b)
		return
	}
	if len(d.utf8Buf) > 0 {
		if b < utf8.RuneSelf {
			d.flushUTF8(true)
		} else {
			d.utf8Buf = append(d.utf8Buf, b)
			d.flushUTF8(false)
			return
		}
	}
	if b >= utf8.RuneSelf {
		d.utf8Buf = append(d.utf8Buf, b)
		d.utf8Origin = position
		d.utf8Time = timeMS
		d.flushUTF8(false)
		return
	}
	d.feedRune(rune(b), position, timeMS)
}

func (d *historyDecoder) feedRune(r rune, position historyPosition, timeMS int64) {
	switch r {
	case 0x1b:
		d.ansi = 1
	case '\n', 0x84, 0x85:
		d.emitLine()
	case '\r':
		d.column = 0
		d.dirty = true
	case '\b':
		d.column = clampHistoryColumn(d.column)
		if d.column > 0 {
			d.column--
		}
	case '\t':
		d.column = clampHistoryColumn(d.column)
		next := historyColumnEnd(d.column, 8-(d.column&7), maxHistoryLineBytes)
		for d.column < next {
			d.writeRune(' ')
		}
	case 0x9b:
		d.startCSI()
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
		d.ansi = 3
	default:
		if r >= 0x20 && r != 0x7f && !(r >= 0x80 && r <= 0x9f) {
			d.writeRune(r)
		}
	}
}

func (d *historyDecoder) flushUTF8(final bool) {
	for len(d.utf8Buf) > 0 && (final || utf8.FullRune(d.utf8Buf)) {
		r, size := utf8.DecodeRune(d.utf8Buf)
		if r == utf8.RuneError && size == 1 && !final && len(d.utf8Buf) < utf8.UTFMax {
			return
		}
		d.utf8Buf = d.utf8Buf[size:]
		d.feedRune(r, d.utf8Origin, d.utf8Time)
	}
}

func clampHistoryColumn(column int) int {
	if column < 0 {
		return 0
	}
	if column > maxHistoryLineBytes {
		return maxHistoryLineBytes
	}
	return column
}

func historyColumnEnd(column, count, limit int) int {
	column = clampHistoryColumn(column)
	if column >= limit {
		return limit
	}
	if count <= 0 {
		return column
	}
	if count >= limit-column {
		return limit
	}
	return column + count
}

func (d *historyDecoder) csiParam(index, fallback int) int {
	const maxCSIParam = maxHistoryLineBytes + 1
	value, field := 0, 0
	have := false
	for i := 0; i <= d.csiLen; i++ {
		var b byte = ';'
		if i < d.csiLen {
			b = d.csi[i]
		}
		switch {
		case b >= '0' && b <= '9':
			digit := int(b - '0')
			if value > (maxCSIParam-digit)/10 {
				value = maxCSIParam
			} else {
				value = value*10 + digit
			}
			have = true
		case b == ';':
			if field == index {
				if !have || value == 0 {
					return fallback
				}
				return value
			}
			field++
			value, have = 0, false
		default:
			return fallback
		}
	}
	return fallback
}

func (d *historyDecoder) applyCSI(final byte) {
	d.column = clampHistoryColumn(d.column)
	n := d.csiParam(0, 1)
	switch final {
	case 'C', 'a':
		d.column = historyColumnEnd(d.column, n, maxHistoryLineBytes)
	case 'D':
		if n >= d.column {
			d.column = 0
		} else {
			d.column -= n
		}
	case 'G', '`':
		d.column = clampHistoryColumn(n - 1)
	case 'H', 'f':
		d.column = clampHistoryColumn(d.csiParam(1, 1) - 1)
	case 'K':
		d.eraseLine(d.csiParam(0, 0))
	case 'P':
		if d.column < len(d.cells) {
			end := historyColumnEnd(d.column, n, len(d.cells))
			copy(d.cells[d.column:], d.cells[end:])
			d.cells = d.cells[:len(d.cells)-(end-d.column)]
		}
	case 'X':
		if d.column < len(d.cells) {
			end := historyColumnEnd(d.column, n, len(d.cells))
			for i := d.column; i < end; i++ {
				d.cells[i] = ' '
			}
		}
	case '@':
		if d.column < maxHistoryLineBytes {
			for len(d.cells) < d.column {
				d.cells = append(d.cells, ' ')
			}
			n = historyColumnEnd(d.column, n, maxHistoryLineBytes) - d.column
			newLen := historyColumnEnd(len(d.cells), n, maxHistoryLineBytes)
			d.cells = append(d.cells, make([]rune, newLen-len(d.cells))...)
			copy(d.cells[d.column+n:], d.cells[d.column:newLen-n])
			for i, end := d.column, d.column+n; i < end; i++ {
				d.cells[i] = ' '
			}
		}
	}
}

func (d *historyDecoder) eraseLine(mode int) {
	d.dirty = true
	d.column = clampHistoryColumn(d.column)
	switch mode {
	case 0:
		if d.column < len(d.cells) {
			d.cells = d.cells[:d.column]
		}
	case 1:
		end := historyColumnEnd(d.column, 1, len(d.cells))
		for i := 0; i < end; i++ {
			d.cells[i] = ' '
		}
	case 2:
		for i := range d.cells {
			d.cells[i] = ' '
		}
	}
}

func (d *historyDecoder) recordSuffixRune(r rune) {
	size := utf8.RuneLen(r)
	if size < 0 {
		size = len(string(r))
	}
	d.suffixRunes = append(d.suffixRunes, r)
	d.suffixBytes += size
	for d.suffixBytes > maxHistoryLineBytes && d.suffixHead < len(d.suffixRunes) {
		d.suffixOverflow = true
		d.suffixBytes -= utf8.RuneLen(d.suffixRunes[d.suffixHead])
		d.suffixHead++
	}
	if d.suffixHead >= 4096 && d.suffixHead*2 >= len(d.suffixRunes) {
		copy(d.suffixRunes, d.suffixRunes[d.suffixHead:])
		d.suffixRunes = d.suffixRunes[:len(d.suffixRunes)-d.suffixHead]
		d.suffixHead = 0
	}
}

func (d *historyDecoder) writeRune(r rune) {
	d.dirty = true
	d.recordSuffixRune(r)
	d.column = clampHistoryColumn(d.column)
	if d.column >= maxHistoryLineBytes {
		return
	}
	for len(d.cells) < d.column {
		d.cells = append(d.cells, ' ')
	}
	if d.column < len(d.cells) {
		d.cells[d.column] = r
	} else {
		d.cells = append(d.cells, r)
	}
	d.column++
}

func (d *historyDecoder) emitLine() {
	d.flushUTF8(true)
	if d.lineOrigin == nil || d.err != nil {
		return
	}
	if err := d.checkWork(len(d.cells)); err != nil {
		return
	}
	text := strings.TrimRight(string(d.cells), " ")
	if len(text) > maxHistoryLineBytes {
		text = text[:maxHistoryLineBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	suffixText := ""
	if d.suffixOverflow {
		suffixText = strings.TrimRight(string(d.suffixRunes[d.suffixHead:]), " ")
	}
	line := historyDecodedLine{
		origin:     *d.lineOrigin,
		timeMS:     d.lineTime,
		text:       text,
		suffixText: suffixText,
	}
	d.onLine(line)
	d.cells = d.cells[:0]
	d.column = 0
	d.lineOrigin = nil
	d.lineTime = 0
	d.dirty = false
	d.suffixRunes = d.suffixRunes[:0]
	d.suffixHead = 0
	d.suffixBytes = 0
	d.suffixOverflow = false
}

func (d *historyDecoder) finish() error {
	if err := d.checkWork(0); err != nil {
		return err
	}
	d.ansi = 0
	d.flushUTF8(true)
	if d.dirty || len(d.cells) > 0 {
		d.emitLine()
	}
	return d.err
}
