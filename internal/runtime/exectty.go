package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// execAttachment adapts a hijacked Docker exec connection to Attachment.
// With a TTY, stdout carries the merged raw stream and stderr is empty;
// without one, Docker multiplexes both and they are demuxed here.
type execAttachment struct {
	cli  *client.Client
	id   string
	tty  bool
	resp client.HijackedResponse

	stdout    *streamBuffer
	stderr    *streamBuffer
	done      chan struct{}
	closeOnce sync.Once
}

func newExecAttachment(cli *client.Client, id string, tty bool, resp client.HijackedResponse) *execAttachment {
	a := &execAttachment{
		cli:    cli,
		id:     id,
		tty:    tty,
		resp:   resp,
		stdout: newStreamBuffer(),
		stderr: newStreamBuffer(),
		done:   make(chan struct{}),
	}
	go func() {
		var err error
		if tty {
			_, err = io.Copy(a.stdout, resp.Reader)
		} else {
			_, err = stdcopy.StdCopy(a.stdout, a.stderr, resp.Reader)
		}
		a.finish(err)
	}()
	return a
}

func (a *execAttachment) Stdin() io.WriteCloser { return hijackStdin{a.resp} }
func (a *execAttachment) Stdout() io.Reader     { return a.stdout }

func (a *execAttachment) Stderr() io.Reader {
	if a.tty {
		return emptyReader{}
	}
	return a.stderr
}

func (a *execAttachment) Resize(ctx context.Context, cols, rows uint) error {
	if !a.tty {
		return errors.New("runtime: exec resize: attachment has no TTY")
	}
	if _, err := a.cli.ExecResize(ctx, a.id, client.ExecResizeOptions{Width: cols, Height: rows}); err != nil {
		return fmt.Errorf("runtime: exec resize: %w", err)
	}
	return nil
}

func (a *execAttachment) Close() error {
	a.finish(nil)
	return nil
}

// finish preserves unread output and closes the transport exactly once, whether
// the remote side reaches EOF or a caller detaches concurrently with the pump.
func (a *execAttachment) finish(err error) {
	a.closeOnce.Do(func() {
		a.stdout.CloseWithError(err)
		a.stderr.CloseWithError(err)
		close(a.done)
		a.resp.Close()
	})
}

func (a *execAttachment) connected() bool {
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

// emptyReader gives each Stderr call an independent empty stream.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
