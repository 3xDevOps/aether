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

// Log is a run's append-only JSONL item log. Every Append is one write to
// the file with no fsync, so a crash can lose the last lines but never
// leaves a torn line behind after the next open.
type Log struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	seqs     []int64
	offsets  []int64
	size     int64
	last     itemHeader
	openTurn bool
	readOnly bool
}

type itemHeader struct {
	Seq   int64 `json:"seq"`
	Epoch int64 `json:"epoch"`
	Turn  int64 `json:"turn"`
	Kind  Kind  `json:"kind"`
}

// OpenLog opens or creates the item log at path. A torn final line from an
// earlier crash is cut off; any other unreadable line is an error.
func OpenLog(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("acphost: create item log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acphost: open item log: %w", err)
	}
	l := &Log{path: path, f: f}
	if err := l.index(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

// OpenLogReadOnly opens the item log at path for reading while another Log
// may be appending to it: a final line without its newline is ignored, not
// cut off.
func OpenLogReadOnly(path string) (*Log, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("acphost: open item log: %w", err)
	}
	l := &Log{path: path, f: f, readOnly: true}
	if err := l.index(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

func (l *Log) index() error {
	r := bufio.NewReaderSize(l.f, 64<<10)
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
		var h itemHeader
		if err := json.Unmarshal(line, &h); err != nil {
			return fmt.Errorf("acphost: item log %s line at byte %d: %w", l.path, off, err)
		}
		l.note(h, off)
		off += int64(len(line))
	}
	l.size = off
	return nil
}

func (l *Log) note(h itemHeader, off int64) {
	l.seqs = append(l.seqs, h.Seq)
	l.offsets = append(l.offsets, off)
	l.last = h
	switch h.Kind {
	case KindTurnStart:
		l.openTurn = true
	case KindTurnEnd, KindReset:
		l.openTurn = false
	}
}

// Append assigns the item its sequence number and epoch, stamps the time if
// unset, and writes it.
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
	b = append(b, '\n')
	if _, err := l.f.Write(b); err != nil {
		err = fmt.Errorf("acphost: append item: %w", err)
		// A short write leaves part of a line behind; the next append would
		// land after it and every later line would sit at the wrong offset.
		if terr := l.f.Truncate(l.size); terr != nil {
			_ = l.f.Close()
			l.f = nil
			return errors.Join(err, fmt.Errorf("acphost: cut partial item log line, log closed: %w", terr))
		}
		return err
	}
	l.note(itemHeader{Seq: it.Seq, Epoch: it.Epoch, Turn: it.Turn, Kind: it.Kind}, l.size)
	l.size += int64(len(b))
	return nil
}

// ReadAfter returns up to limit items with Seq greater than seq, oldest
// first. A limit of zero or less means no limit.
func (l *Log) ReadAfter(seq int64, limit int) ([]Item, error) {
	l.mu.Lock()
	start := sort.Search(len(l.seqs), func(i int) bool { return l.seqs[i] > seq })
	end := len(l.seqs)
	if limit > 0 && end-start > limit {
		end = start + limit
	}
	f, from, to := l.span(start, end)
	l.mu.Unlock()
	return readItems(f, from, to)
}

// ReadBefore returns up to limit items with Seq less than seq, oldest
// first. A limit of zero or less means no limit.
func (l *Log) ReadBefore(seq int64, limit int) ([]Item, error) {
	l.mu.Lock()
	end := sort.Search(len(l.seqs), func(i int) bool { return l.seqs[i] >= seq })
	start := 0
	if limit > 0 && end > limit {
		start = end - limit
	}
	f, from, to := l.span(start, end)
	l.mu.Unlock()
	return readItems(f, from, to)
}

// span locates the lines of items start to end. Lines never change once
// appended, so they are read after l.mu is released: a long replay then
// holds up neither appends nor the session lock an append runs under.
func (l *Log) span(start, end int) (f *os.File, from, to int64) {
	if l.f == nil || start >= end {
		return l.f, 0, 0
	}
	from, to = l.offsets[start], l.size
	if end < len(l.offsets) {
		to = l.offsets[end]
	}
	return l.f, from, to
}

func readItems(f *os.File, from, to int64) ([]Item, error) {
	if f == nil {
		return nil, ErrLogClosed
	}
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

// LastSeq returns the sequence number of the newest item, or zero.
func (l *Log) LastSeq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last.Seq
}

func (l *Log) state() (last itemHeader, size int64, openTurn bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last, l.size, l.openTurn
}

// Close closes the file. Reads and appends fail afterwards.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	if err != nil {
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
