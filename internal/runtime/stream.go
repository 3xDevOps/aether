package runtime

import (
	"bytes"
	"io"
	"sync"
)

const maxStreamBuffer = 8 << 20

// StreamBuffer writes never block, since blocking the demux goroutine would
// stall the sibling stream; past maxStreamBuffer the oldest bytes are
// dropped. A lossless buffer blocks at the cap instead, for protocol streams
// that cannot lose a byte.
type StreamBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      bytes.Buffer
	lossless bool
	// limit is where a lossless buffer blocks its writer.
	limit  int
	closed bool
	err    error
}

func NewStreamBuffer() *StreamBuffer {
	b := &StreamBuffer{limit: maxStreamBuffer}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func newLosslessStreamBuffer() *StreamBuffer {
	b := NewStreamBuffer()
	b.lossless = true
	return b
}

func (b *StreamBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.lossless && !b.closed && b.buf.Len() >= b.limit {
		b.cond.Wait()
	}
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	n, _ := b.buf.Write(p) // bytes.Buffer.Write cannot fail
	if b.lossless {
		b.cond.Broadcast()
		return n, nil
	}
	if over := b.buf.Len() - maxStreamBuffer; over > 0 {
		b.buf.Next(over)
	}
	b.cond.Broadcast()
	return n, nil
}

func (b *StreamBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.buf.Len() > 0 {
		if b.lossless {
			b.cond.Broadcast()
		}
		return b.buf.Read(p)
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

// CloseWithError leaves buffered data readable first. Only the first close
// takes effect.
func (b *StreamBuffer) CloseWithError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.err = err
	b.cond.Broadcast()
}
