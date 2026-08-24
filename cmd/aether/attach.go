package main

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/cli"
)

func init() {
	register(command{
		name:  "attach",
		short: "attach to a run's PTY",
		run:   runAttach,
	})
}

func runAttach(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether attach <run>")
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	cols, rows := termSize()
	stream, err := conn.Attach(args[0], cols, rows)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	return copyRaw(stream)
}

// termSizeOf is a seam so the handle probe order can be tested; under `go test`
// neither standard handle is a console.
var termSizeOf = term.GetSize

// termSize prefers stdout because Windows resolves the size with
// GetConsoleScreenBufferInfo, which rejects an input handle. Stdin is the
// fallback for a redirected stdout, and 80x24 covers neither being a console.
func termSize() (cols, rows uint) {
	for _, f := range []*os.File{os.Stdout, os.Stdin} {
		if w, h, err := termSizeOf(int(f.Fd())); err == nil {
			return uint(w), uint(h)
		}
	}
	return 80, 24
}

func copyRaw(stream io.ReadWriteCloser) error {
	// Unconditional: stdout can be a console even when stdin is redirected,
	// and the raw-mode branch below keys off stdin. The Windows
	// implementation no-ops when stdout is not a console.
	defer enableVirtualTerminal(os.Stdout)()

	fd := int(os.Stdin.Fd())
	input := io.Reader(os.Stdin)
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		var restoreOnce sync.Once
		restore := func() {
			restoreOnce.Do(func() { _ = term.Restore(fd, old) })
		}
		defer restore()
		input = &closingReader{
			Reader: os.Stdin,
			close: func() error {
				restore()
				return os.Stdin.Close()
			},
		}
	}
	return copyRawStreams(stream, input, os.Stdout)
}

func copyRawStreams(stream io.ReadWriteCloser, input io.Reader, output io.Writer) error {
	type copyResult struct {
		output   bool
		err      error
		closeErr error
	}
	results := make(chan copyResult, 2)
	go func() {
		_, err := io.Copy(output, stream)
		results <- copyResult{output: true, err: err}
	}()
	go func() {
		_, err := io.Copy(stream, input)
		var closeErr error
		if err == nil {
			if stream, ok := stream.(interface{ CloseWrite() error }); ok {
				closeErr = stream.CloseWrite()
			}
		}
		results <- copyResult{err: err, closeErr: closeErr}
	}()
	inputDone := false
	var closeErr error
	for {
		result := <-results
		if result.output {
			if !inputDone {
				_ = stream.Close()
				if input, ok := input.(io.Closer); ok {
					_ = input.Close()
				}
			}
			if result.err != nil {
				return result.err
			}
			return closeErr
		}
		inputDone = true
		if result.err != nil {
			_ = stream.Close()
			return result.err
		}
		closeErr = result.closeErr
	}
}

type closingReader struct {
	io.Reader
	close func() error
}

func (r *closingReader) Close() error {
	return r.close()
}
