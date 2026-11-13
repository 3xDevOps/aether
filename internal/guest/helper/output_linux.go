package helper

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// outputLimit is how much of a stream the helper keeps: the newest bytes of
// a lossy stream, or the unread window of a lossless one.
const outputLimit = 256 << 10

// errOffset reports a read past the end of a stream.
var errOffset = errors.New("offset is past the end of the stream")

// output holds one stream of a process under offsets counted from its first
// byte. data holds the bytes from start to end.
type output struct {
	mu       sync.Mutex
	data     []byte
	start    int64
	read     int64
	lossless bool
	closed   bool
	// changed is closed and replaced whenever data, read or closed change.
	changed chan struct{}
}

func newOutput(lossless bool) *output {
	return &output{lossless: lossless, changed: make(chan struct{})}
}

func (o *output) end() int64 { return o.start + int64(len(o.data)) }

func (o *output) notify() {
	close(o.changed)
	o.changed = make(chan struct{})
}

// room waits until the stream may take more bytes and says how many: a
// lossy stream always may, a lossless one while its unread bytes are under
// outputLimit. A closed stream takes none.
func (o *output) room() int {
	for {
		o.mu.Lock()
		if o.closed {
			o.mu.Unlock()
			return 0
		}
		if !o.lossless {
			o.mu.Unlock()
			return 32 << 10
		}
		free := outputLimit - int(o.end()-o.read)
		changed := o.changed
		o.mu.Unlock()
		if free > 0 {
			return min(free, 32<<10)
		}
		<-changed
	}
}

func (o *output) append(b []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.data = append(o.data, b...)
	if drop := len(o.data) - outputLimit; drop > 0 {
		o.data = append(o.data[:0], o.data[drop:]...)
		o.start += int64(drop)
	}
	o.notify()
}

func (o *output) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.closed {
		o.closed = true
		o.notify()
	}
}

// readAt waits for bytes at or after offset, a negative offset meaning the
// current end, and returns where they start. Reading at offset acknowledges
// every byte before it: a lossless stream drops them and takes more.
func (o *output) readAt(ctx context.Context, offset int64, limit int) (int64, []byte, bool, error) {
	o.mu.Lock()
	if offset < 0 {
		offset = o.end()
	}
	if offset > o.end() {
		o.mu.Unlock()
		return 0, nil, false, errOffset
	}
	if offset > o.read {
		o.read = offset
		if o.lossless {
			drop := int(offset - o.start)
			o.data = append(o.data[:0], o.data[drop:]...)
			o.start = offset
		}
		o.notify()
	}
	o.mu.Unlock()
	for {
		o.mu.Lock()
		from := max(offset, o.start)
		if from < o.end() {
			n := min(int(o.end()-from), limit)
			data := make([]byte, n)
			copy(data, o.data[from-o.start:])
			o.mu.Unlock()
			return from, data, false, nil
		}
		if o.closed {
			o.mu.Unlock()
			return from, nil, true, nil
		}
		changed := o.changed
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, nil, false, ctx.Err()
		case <-changed:
		}
	}
}

// pump copies f into o until f ends, then closes both. A pty master's EIO
// once its terminal closes is its end.
func (o *output) pump(f *os.File, done <-chan struct{}) {
	defer o.close()
	defer func() { _ = f.Close() }()
	go drainAfter(f, done)
	buf := make([]byte, 32<<10)
	for {
		room := o.room()
		if room == 0 {
			return
		}
		n, err := f.Read(buf[:room])
		if n > 0 {
			o.append(buf[:n])
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			o.drain(f, buf)
			return
		}
		if err != nil {
			return
		}
	}
}

// drain takes what f still held when its time ran out: the deadline fails
// a read before it looks, so a lossless stream that waited for its reader
// past the deadline would lose those bytes.
func (o *output) drain(f *os.File, buf []byte) {
	raw, err := f.SyscallConn()
	if err != nil || f.SetReadDeadline(time.Time{}) != nil {
		return
	}
	left := 0
	_ = raw.Control(func(fd uintptr) { left, _ = unix.IoctlGetInt(int(fd), unix.TIOCINQ) })
	for left > 0 {
		room := min(o.room(), left)
		if room == 0 {
			return
		}
		n, readErr := 0, error(nil)
		err := raw.Read(func(fd uintptr) bool {
			n, readErr = unix.Read(int(fd), buf[:room])
			return true
		})
		if err != nil || readErr != nil || n <= 0 {
			return
		}
		o.append(buf[:n])
		left -= n
	}
}

// exitDrain is how long a stream outlives its process: a descendant that
// still holds it open must not keep the stream from ending.
const exitDrain = time.Second

func drainAfter(f *os.File, done <-chan struct{}) {
	<-done
	_ = f.SetReadDeadline(time.Now().Add(exitDrain))
}

// last returns the newest limit bytes the stream holds and whether it ever
// held more.
func (o *output) last(limit int) ([]byte, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data := o.data[max(len(o.data)-limit, 0):]
	return append([]byte(nil), data...), o.end() > int64(len(data))
}

func (o *output) waitClosed() {
	for {
		o.mu.Lock()
		closed, changed := o.closed, o.changed
		o.mu.Unlock()
		if closed {
			return
		}
		<-changed
	}
}
