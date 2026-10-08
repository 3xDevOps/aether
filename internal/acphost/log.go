package acphost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var ErrLogClosed = errors.New("acphost: item log closed")
var ErrLogReadOnly = errors.New("acphost: item log opened read-only")
var ErrHistoryExpired = errors.New("acphost: requested session history has expired")

// HistoryPage describes one atomic snapshot of the retained public items.
// OldestSeq is the first retained sequence, not necessarily on this page.
type HistoryPage struct {
	Items           []Item
	OldestSeq       int64
	TruncatedBefore bool
}

// SubscriptionReplay is the replay boundary captured before live delivery starts.
type SubscriptionReplay struct {
	Items           []Item
	OldestSeq       int64
	TruncatedBefore bool
	Reset           bool
	Epoch           int64
	Seq             int64
}

// Log retains the latest MaxRunBytes of JSONL history. Compaction atomically
// replaces the file, leaving pinned readers on their original inode. The private
// prefix checkpoint is never exposed as an Item. Atomic copies temporarily need
// another retained window of disk space; open readers can also pin old inodes.
type Log struct {
	mu       sync.Mutex
	path     string
	f        *logFile
	seqs     []int64
	offsets  []int64
	size     int64
	last     itemHeader
	openTurn bool
	mode     string
	prefix   logCheckpoint
	readOnly bool
}

type logFile struct {
	*os.File
	readers int
	retired bool
}

type itemHeader struct {
	Seq   int64 `json:"seq"`
	Epoch int64 `json:"epoch"`
	Turn  int64 `json:"turn"`
	Kind  Kind  `json:"kind"`
}

// The checkpoint describes state immediately before the retained public suffix.
// Its reserved key cannot be produced by Item.encode.
type logCheckpoint struct {
	Last     itemHeader `json:"last"`
	OpenTurn bool       `json:"open_turn"`
	Mode     string     `json:"mode,omitempty"`
}

type logRecord struct {
	itemHeader
	Mode       string         `json:"mode,omitempty"`
	Checkpoint *logCheckpoint `json:"_acphost_checkpoint,omitempty"`
}

func (c *logCheckpoint) note(h itemHeader, mode string) {
	c.Last = h
	switch h.Kind {
	case KindTurnStart:
		c.OpenTurn = true
	case KindTurnEnd, KindReset:
		c.OpenTurn = false
	}
	if h.Kind == KindModeChange {
		c.Mode = mode
	}
}

// OpenLog opens or creates the item log. A torn final line is cut off; any
// other unreadable line is an error. Oversized old-format logs roll on open.
// As with appends, the caller must own the run's sole writer.
func OpenLog(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("acphost: create item log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acphost: open item log: %w", err)
	}
	l := &Log{path: path, f: &logFile{File: f}}
	if err := l.index(); err != nil {
		_ = f.Close()
		return nil, err
	}
	// An interrupted atomic copy was never published. Reclaim only this
	// writer-owned scratch path, leaving the canonical file authoritative.
	if err := os.Remove(path + ".compact"); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = f.Close()
		return nil, fmt.Errorf("acphost: remove interrupted compaction: %w", err)
	}
	if l.size > MaxRunBytes {
		if err := l.compact(MaxRunBytes - (8 << 20)); err != nil {
			_ = l.f.Close()
			return nil, err
		}
	}
	return l, nil
}

// OpenLogReadOnly pins a snapshot of the current file and its byte boundary.
// A final line without its newline is ignored, never cut off.
func OpenLogReadOnly(path string) (*Log, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("acphost: open item log: %w", err)
	}
	l := &Log{path: path, f: &logFile{File: f}, readOnly: true}
	if err := l.index(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

func (l *Log) index() error {
	info, err := l.f.Stat()
	if err != nil {
		return fmt.Errorf("acphost: stat item log: %w", err)
	}
	r := bufio.NewReaderSize(io.NewSectionReader(l.f, 0, info.Size()), 64<<10)
	var off int64
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 && !l.readOnly {
				if terr := l.f.Truncate(off); terr != nil {
					return fmt.Errorf("acphost: cut torn item log line: %w", terr)
				}
			}
			break
		}
		if err != nil {
			return fmt.Errorf("acphost: read item log: %w", err)
		}
		var record logRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("acphost: item log %s line at byte %d: %w", l.path, off, err)
		}
		if record.Checkpoint != nil {
			if off != 0 || record.Checkpoint.Last.Seq <= 0 {
				return fmt.Errorf("acphost: invalid item log checkpoint at byte %d", off)
			}
			l.prefix = *record.Checkpoint
			l.last, l.openTurn, l.mode = l.prefix.Last, l.prefix.OpenTurn, l.prefix.Mode
		} else {
			if record.Seq <= l.last.Seq {
				return fmt.Errorf("acphost: non-increasing item sequence at byte %d", off)
			}
			l.note(record.itemHeader, record.Mode, off)
		}
		off += int64(len(line))
	}
	l.size = off
	return nil
}

func (l *Log) note(h itemHeader, mode string, off int64) {
	l.seqs = append(l.seqs, h.Seq)
	l.offsets = append(l.offsets, off)
	state := logCheckpoint{Last: l.last, OpenTurn: l.openTurn, Mode: l.mode}
	state.note(h, mode)
	l.last, l.openTurn, l.mode = state.Last, state.OpenTurn, state.Mode
}

// compact keeps a contiguous suffix, with headroom to avoid rewriting the whole
// window on every append. No accepted record is modified in place.
func (l *Log) compact(target int64) error {
	start := sort.Search(len(l.offsets), func(i int) bool { return l.size-l.offsets[i] <= target })
	if start == 0 {
		return nil
	}
	from := l.size
	if start < len(l.offsets) {
		from = l.offsets[start]
	}
	prefix := l.prefix
	r := bufio.NewReaderSize(io.NewSectionReader(l.f, l.offsets[0], from-l.offsets[0]), 64<<10)
	for range start {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("acphost: read compacted prefix: %w", err)
		}
		var record logRecord
		if decodeErr := json.Unmarshal(line, &record); decodeErr != nil {
			return fmt.Errorf("acphost: decode compacted prefix: %w", decodeErr)
		}
		prefix.note(record.itemHeader, record.Mode)
	}
	checkpoint, err := json.Marshal(struct {
		Checkpoint logCheckpoint `json:"_acphost_checkpoint"`
	}{prefix})
	if err != nil {
		return err
	}
	checkpoint = append(checkpoint, '\n')
	info, err := l.f.Stat()
	if err != nil {
		return err
	}
	name := l.path + ".compact"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("acphost: create compacted log: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(name)
	}()
	if chmodErr := f.Chmod(info.Mode().Perm()); chmodErr != nil {
		return chmodErr
	}
	if _, writeErr := f.Write(checkpoint); writeErr != nil {
		return fmt.Errorf("acphost: write checkpoint: %w", writeErr)
	}
	if _, copyErr := io.Copy(f, io.NewSectionReader(l.f, from, l.size-from)); copyErr != nil {
		return fmt.Errorf("acphost: copy retained items: %w", copyErr)
	}
	if syncErr := f.Sync(); syncErr != nil {
		return fmt.Errorf("acphost: sync compacted log: %w", syncErr)
	}
	// Open with O_APPEND before publishing, so a failed reopen leaves the old
	// file authoritative. This descriptor pins exactly the inode being renamed.
	next, err := os.OpenFile(name, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if renameErr := os.Rename(name, l.path); renameErr != nil {
		_ = next.Close()
		return fmt.Errorf("acphost: replace item log: %w", renameErr)
	}
	old := l.f
	l.f = &logFile{File: next}
	old.retired = true
	if old.readers == 0 {
		_ = old.Close()
	}
	// Copy the bounded suffix indexes rather than retaining an arbitrarily
	// large backing array from a pre-retention log.
	seqs := append([]int64(nil), l.seqs[start:]...)
	offsets := make([]int64, len(seqs))
	for i := range offsets {
		offsets[i] = l.offsets[start+i] - from + int64(len(checkpoint))
	}
	l.seqs, l.offsets = seqs, offsets
	l.size = l.size - from + int64(len(checkpoint))
	l.prefix = prefix
	dir, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return fmt.Errorf("acphost: open item log directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if syncErr := dir.Sync(); syncErr != nil {
		return fmt.Errorf("acphost: sync item log directory: %w", syncErr)
	}
	return nil
}

// Append assigns a globally increasing sequence, epoch and timestamp. Every
// kind participates in the same rolling window; live control state is separate.
func (l *Log) Append(it *Item) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return ErrLogClosed
	}
	if l.readOnly {
		return ErrLogReadOnly
	}
	it.Seq = l.last.Seq + 1
	it.Epoch = l.last.Epoch
	if it.Kind == KindReset {
		it.Epoch++
	}
	if it.Time.IsZero() {
		it.Time = time.Now().UTC()
	}
	b, err := it.encode()
	if err != nil {
		return fmt.Errorf("acphost: encode item: %w", err)
	}
	if len(b) > MaxItemBytes {
		return fmt.Errorf("acphost: encoded item exceeds %d bytes", MaxItemBytes)
	}
	b = append(b, '\n')
	if l.size+int64(len(b)) > MaxRunBytes {
		if compactErr := l.compact(MaxRunBytes - (8 << 20)); compactErr != nil {
			return compactErr
		}
	}
	if n, appendErr := l.f.Write(b); appendErr != nil || n != len(b) {
		if appendErr == nil {
			appendErr = io.ErrShortWrite
		}
		appendErr = fmt.Errorf("acphost: append item: %w", appendErr)
		if terr := l.f.Truncate(l.size); terr != nil {
			appendErr = errors.Join(appendErr, fmt.Errorf("acphost: cut partial item log line, log closed: %w", terr))
			if closeErr := l.retire(); closeErr != nil {
				appendErr = errors.Join(appendErr, fmt.Errorf("acphost: close poisoned item log: %w", closeErr))
			}
			return appendErr
		}
		return appendErr
	}
	l.note(itemHeader{Seq: it.Seq, Epoch: it.Epoch, Turn: it.Turn, Kind: it.Kind}, it.Mode, l.size)
	l.size += int64(len(b))
	return nil
}

type logRead struct {
	log      *Log
	file     *logFile
	from, to int64
}

// span pins the file while holding mu; reads never hold up appends or compaction.
func (l *Log) span(start, end int) logRead {
	r := logRead{log: l, file: l.f}
	if l.f != nil {
		l.f.readers++
	}
	if start < end {
		r.from, r.to = l.offsets[start], l.size
		if end < len(l.offsets) {
			r.to = l.offsets[end]
		}
	}
	return r
}

func (r logRead) items() ([]Item, error) {
	if r.file == nil {
		return nil, ErrLogClosed
	}
	defer func() {
		r.log.mu.Lock()
		defer r.log.mu.Unlock()
		r.file.readers--
		if r.file.retired && r.file.readers == 0 {
			_ = r.file.Close()
		}
	}()
	return readItems(r.file.File, r.from, r.to)
}

// ReadAfter returns retained items with Seq greater than seq, oldest first.
// A limit of zero or less means no limit. Use History or Replay for gap metadata.
func (l *Log) ReadAfter(seq int64, limit int) ([]Item, error) {
	l.mu.Lock()
	start := sort.Search(len(l.seqs), func(i int) bool { return l.seqs[i] > seq })
	end := len(l.seqs)
	if limit > 0 && end-start > limit {
		end = start + limit
	}
	r := l.span(start, end)
	l.mu.Unlock()
	return r.items()
}

// ReadBefore returns retained items with Seq less than seq, oldest first.
func (l *Log) ReadBefore(seq int64, limit int) ([]Item, error) {
	page, err := l.History(seq, limit)
	return page.Items, err
}

// History captures page contents and the retention boundary under the same lock.
func (l *Log) History(seq int64, limit int) (HistoryPage, error) {
	l.mu.Lock()
	end := sort.Search(len(l.seqs), func(i int) bool { return l.seqs[i] >= seq })
	start := 0
	if limit > 0 && end > limit {
		start = end - limit
	}
	page := HistoryPage{TruncatedBefore: l.prefix.Last.Seq > 0}
	if len(l.seqs) > 0 {
		page.OldestSeq = l.seqs[0]
	}
	r := l.span(start, end)
	l.mu.Unlock()
	var err error
	page.Items, err = r.items()
	return page, err
}

// Item distinguishes an expired identity from an identity never recorded.
func (l *Log) Item(seq int64) (Item, bool, error) {
	l.mu.Lock()
	if l.f == nil {
		l.mu.Unlock()
		return Item{}, false, ErrLogClosed
	}
	if seq > 0 && seq <= l.prefix.Last.Seq {
		l.mu.Unlock()
		return Item{}, false, fmt.Errorf("%w: %d", ErrHistoryExpired, seq)
	}
	start := sort.Search(len(l.seqs), func(i int) bool { return l.seqs[i] >= seq })
	end := start
	if start < len(l.seqs) && l.seqs[start] == seq {
		end++
	}
	r := l.span(start, end)
	l.mu.Unlock()
	items, err := r.items()
	if err != nil || len(items) == 0 {
		return Item{}, false, err
	}
	return items[0], true, nil
}

func (l *Log) beginReplay(afterSeq int64) (SubscriptionReplay, logRead) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := SubscriptionReplay{Epoch: l.last.Epoch, Seq: l.last.Seq, TruncatedBefore: l.prefix.Last.Seq > 0}
	out.Reset = afterSeq > l.last.Seq || (afterSeq > 0 && afterSeq < l.prefix.Last.Seq)
	if out.Reset {
		afterSeq = 0
	}
	startSeq := ReplayStart(afterSeq, l.last.Seq)
	start := sort.Search(len(l.seqs), func(i int) bool { return l.seqs[i] > startSeq })
	if start < len(l.seqs) && (afterSeq <= 0 || l.seqs[start] > afterSeq+1) {
		out.OldestSeq = l.seqs[start]
	}
	return out, l.span(start, len(l.seqs))
}

// Replay is a read-only subscription snapshot without a live channel.
func (l *Log) Replay(afterSeq int64) (SubscriptionReplay, error) {
	out, r := l.beginReplay(afterSeq)
	var err error
	out.Items, err = r.items()
	return out, err
}

func readItems(f *os.File, from, to int64) ([]Item, error) {
	if from >= to {
		return nil, nil
	}
	buf := make([]byte, to-from)
	if _, err := f.ReadAt(buf, from); err != nil {
		if errors.Is(err, os.ErrClosed) {
			return nil, ErrLogClosed
		}
		return nil, fmt.Errorf("acphost: read item log: %w", err)
	}
	var items []Item
	for line := range bytes.Lines(buf) {
		var it Item
		if err := json.Unmarshal(line, &it); err != nil {
			return nil, fmt.Errorf("acphost: decode item: %w", err)
		}
		items = append(items, it)
	}
	return items, nil
}

func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seqs)
}

func (l *Log) LastSeq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last.Seq
}

// Mode preserves the latest mode even when its public item expires.
func (l *Log) Mode() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mode
}

func (l *Log) state() (last itemHeader, size int64, openTurn bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last, l.size, l.openTurn
}

func (l *Log) retire() error {
	f := l.f
	l.f = nil
	f.retired = true
	if f.readers == 0 {
		return f.Close()
	}
	return nil
}

// Close prevents new reads and appends; already pinned reads finish normally.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	if err := l.retire(); err != nil {
		return fmt.Errorf("acphost: close item log: %w", err)
	}
	return nil
}

func (l *Log) Delete() error {
	if err := l.Close(); err != nil {
		return err
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("acphost: delete item log: %w", err)
	}
	return nil
}
