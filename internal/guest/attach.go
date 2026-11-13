package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
)

// Attachment is a live stdio stream to one process. The helper keeps each
// stream's recent output under byte offsets, so an attachment rides through
// a VM save and restore, and a new server can attach where an old one left.
type Attachment struct {
	p      *Process
	ctx    context.Context
	cancel context.CancelFunc
	stdout *io.PipeReader
	stderr *io.PipeReader
	stdin  *attachInput
	wg     sync.WaitGroup
}

// Attach streams the process's output: from the first byte the helper still
// holds with fromStart, otherwise from now on.
func (p *Process) Attach(fromStart bool) *Attachment {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Attachment{p: p, ctx: ctx, cancel: cancel}
	a.stdin = &attachInput{a: a}
	offset := int64(-1)
	if fromStart {
		offset = 0
	}
	var stdoutW, stderrW *io.PipeWriter
	a.stdout, stdoutW = io.Pipe()
	a.stderr, stderrW = io.Pipe()
	a.wg.Add(2)
	go a.pump(guestpb.Stream_STREAM_STDOUT, stdoutW, offset)
	go a.pump(guestpb.Stream_STREAM_STDERR, stderrW, offset)
	return a
}

// pump copies one stream until it ends or the attachment closes. A pipe
// without a buffer is the backpressure: an unread stream stops asking the
// helper for more, which then holds a lossless process or drops a lossy
// process's oldest bytes.
func (a *Attachment) pump(stream guestpb.Stream, w *io.PipeWriter, offset int64) {
	defer a.wg.Done()
	for {
		reply, err := retry(a.ctx, func() (*guestpb.ReadReply, error) {
			return a.p.c.api.Read(a.ctx, &guestpb.ReadRequest{Id: a.p.id, Stream: stream, Offset: offset, MaxBytes: readChunk})
		})
		if a.ctx.Err() != nil {
			_ = w.CloseWithError(io.ErrClosedPipe)
			return
		}
		if err != nil {
			_ = w.CloseWithError(a.p.err("read", err))
			return
		}
		data := reply.GetData()
		if reply.GetOffset() < max(offset, 0) || len(data) > readChunk {
			_ = w.CloseWithError(fmt.Errorf("guest: read %s: helper returned %d bytes at offset %d for offset %d", a.p.id, len(data), reply.GetOffset(), offset))
			return
		}
		if len(data) > 0 {
			if _, err := w.Write(data); err != nil {
				return
			}
		}
		offset = reply.GetOffset() + int64(len(data))
		if reply.GetEof() {
			_ = w.Close()
			return
		}
	}
}

// Stdin writes to the process's input. Closing it ends only this
// attachment's input: the process's input stays open for a later one.
func (a *Attachment) Stdin() io.WriteCloser { return a.stdin }

func (a *Attachment) Stdout() io.Reader { return a.stdout }

// Stderr is empty for a process on a terminal, whose output all arrives on
// Stdout.
func (a *Attachment) Stderr() io.Reader { return a.stderr }

// Resize sets the geometry of the process's terminal.
func (a *Attachment) Resize(ctx context.Context, cols, rows uint) error {
	if cols > 65535 || rows > 65535 {
		return errors.New("guest: terminal dimensions must be at most 65535")
	}
	return a.p.Resize(ctx, Size{Cols: uint16(cols), Rows: uint16(rows)})
}

// Close detaches the streams; the process keeps running.
func (a *Attachment) Close() error {
	a.cancel()
	_ = a.stdout.Close()
	_ = a.stderr.Close()
	a.wg.Wait()
	return nil
}

type attachInput struct {
	a      *Attachment
	mu     sync.Mutex
	closed bool
}

func (in *attachInput) Write(b []byte) (int, error) {
	return in.WriteContext(in.a.ctx, b)
}

// WriteContext bounds one write by ctx as well as the attachment's life.
func (in *attachInput) WriteContext(ctx context.Context, b []byte) (int, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		return 0, io.ErrClosedPipe
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(in.a.ctx, cancel)
	defer stop()
	return in.a.p.Write(ctx, b)
}

func (in *attachInput) Close() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.closed = true
	return nil
}
