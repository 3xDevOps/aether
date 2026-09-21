package ptyhost

import (
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
	"time"

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
