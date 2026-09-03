package runtime

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// execAttachment adapts a hijacked Docker exec connection to Attachment.
// Exec streams use a TTY, so stdout carries the merged raw stream and stderr
// is empty.
type execAttachment struct {
	cli  *client.Client
	id   string
	resp types.HijackedResponse

	stdout    *streamBuffer
	closeOnce sync.Once
}

func newExecAttachment(cli *client.Client, id string, resp types.HijackedResponse) *execAttachment {
	a := &execAttachment{
		cli:    cli,
		id:     id,
		resp:   resp,
		stdout: newStreamBuffer(),
	}
	go func() {
		_, err := io.Copy(a.stdout, resp.Reader)
		a.stdout.CloseWithError(err)
	}()
	return a
}

func (a *execAttachment) Stdin() io.WriteCloser { return hijackStdin{a.resp} }
func (a *execAttachment) Stdout() io.Reader     { return a.stdout }
func (a *execAttachment) Stderr() io.Reader     { return emptyReader{} }

func (a *execAttachment) Resize(ctx context.Context, cols, rows uint) error {
	if err := a.cli.ContainerExecResize(ctx, a.id, container.ResizeOptions{Width: cols, Height: rows}); err != nil {
		return fmt.Errorf("runtime: exec resize: %w", err)
	}
	return nil
}

func (a *execAttachment) Close() error {
	a.closeOnce.Do(func() {
		a.stdout.CloseWithError(nil)
		a.resp.Close()
	})
	return nil
}

// emptyReader gives each Stderr call an independent empty stream.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
