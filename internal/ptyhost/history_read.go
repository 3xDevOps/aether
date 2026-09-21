package ptyhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

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
