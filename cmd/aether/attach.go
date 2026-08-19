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

func termSize() (cols, rows uint) {
	cols, rows = 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		cols, rows = uint(w), uint(h)
	}
	return
}

func copyRaw(stream io.ReadWriteCloser) error {
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
