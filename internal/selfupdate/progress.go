package selfupdate

import (
	"fmt"
	"io"
	"time"
)

const progressEvery = 5 * time.Second

type progress struct {
	out         io.Writer
	total, done int64
	every       time.Duration
	now         func() time.Time
	last        time.Time
}

// NewProgress returns a writer that counts the bytes written to it and, at
// most every five seconds, prints one newline-terminated line such as
// "  42 of 104 MB (40%)" to out. total is the expected size, or <= 0 when
// unknown. Lines end in \n, never \r, because out is often a pipe. A nil
// out makes the writer silent.
func NewProgress(out io.Writer, total int64) io.Writer {
	if out == nil {
		return io.Discard
	}
	return newProgress(out, total, progressEvery, time.Now)
}

func newProgress(out io.Writer, total int64, every time.Duration, now func() time.Time) *progress {
	return &progress{out: out, total: total, every: every, now: now, last: now()}
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if t := p.now(); t.Sub(p.last) >= p.every {
		p.last = t
		const mb = 1 << 20
		if p.total > 0 {
			_, _ = fmt.Fprintf(p.out, "  %d of %d MB (%d%%)\n", p.done/mb, (p.total+mb/2)/mb, p.done*100/p.total)
		} else {
			_, _ = fmt.Fprintf(p.out, "  %d MB\n", p.done/mb)
		}
	}
	return len(b), nil
}
