package ptyhost

import "bytes"

// ring keeps the last max bytes of client-visible terminal output. highWater
// is the absolute position immediately after buf's final byte.
type ring struct {
	max       int
	buf       []byte
	dropped   bool
	highWater TerminalPosition
}

func newRingAt(max int, position TerminalPosition) *ring {
	return &ring{max: max, highWater: position}
}

func (r *ring) write(p []byte) {
	if len(p) == 0 {
		return
	}
	r.highWater.Sequence += TerminalSequence(len(p))
	if len(p) >= r.max {
		if len(p) > r.max || len(r.buf) > 0 {
			r.dropped = true
		}
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return
	}
	if len(r.buf)+len(p) > r.max {
		r.dropped = true
	}
	r.buf = append(r.buf, p...)
	if n := len(r.buf) - r.max; n > 0 {
		r.buf = append(r.buf[:0], r.buf[n:]...)
	}
}

// seed restores retained bytes ending exactly at position. The caller must
// supply bytes from the same epoch and immediately preceding Sequence.
func (r *ring) seed(p []byte, position TerminalPosition) {
	r.highWater = position
	if TerminalSequence(len(p)) > position.Sequence {
		p = p[len(p)-int(position.Sequence):]
	}
	if len(p) > r.max {
		p = p[len(p)-r.max:]
		r.dropped = true
	} else {
		r.dropped = position.Sequence > TerminalSequence(len(p))
	}
	r.buf = append(r.buf[:0], p...)
}

// since returns bytes strictly after position when it belongs to this epoch
// and the complete suffix is still retained. Wrong-epoch, future, and evicted
// positions are rejected.
func (r *ring) since(position TerminalPosition) ([]byte, bool) {
	if position.Epoch != r.highWater.Epoch || position.Sequence > r.highWater.Sequence {
		return nil, false
	}
	behind := r.highWater.Sequence - position.Sequence
	if behind > TerminalSequence(len(r.buf)) {
		return nil, false
	}
	return append([]byte(nil), r.buf[len(r.buf)-int(behind):]...), true
}

func (r *ring) position() TerminalPosition { return r.highWater }

func (r *ring) bytes() []byte {
	start := 0
	if r.dropped {
		if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
			start = i + 1
		}
	}
	return append([]byte(nil), r.buf[start:]...)
}
