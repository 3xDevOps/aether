package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "attach",
		short: "attach to a run's PTY",
		run:   runAttach,
	})
}

func runAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	readOnly := fs.Bool("read-only", false, "watch the terminal without being able to type into it")
	shell := fs.String("shell", "", "open a writable shell tab inside the run")
	runID, err := parseLeadingArg(fs, args)
	if err != nil || runID == "" {
		return fmt.Errorf("usage: aether attach [--read-only] [--shell <tab>] <run>")
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
	stream, err := openAttach(conn, runID, cols, rows, *readOnly, *shell)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	return describeAttachEnd(copyRaw(stream))
}

// openAttach asks to steer unless told otherwise, and drops to a read-only
// mirror when the server refuses steer, as the dashboard's terminal does.
// Shell tabs are always writable and report a refusal without downgrading.
func openAttach(conn *cli.Conn, runID string, cols, rows uint, readOnly bool, shell string) (io.ReadWriteCloser, error) {
	if shell != "" {
		readOnly = false
	}
	req := protocol.AttachRequest{RunID: runID, Cols: cols, Rows: rows, ReadOnly: readOnly, Shell: shell}
	stream, ack, err := conn.AttachStream(req)
	if err == nil {
		return stream, nil
	}
	if shell != "" || readOnly || ack.OK || ack.Code != protocol.CodeDenied {
		return nil, err
	}
	req.ReadOnly = true
	stream, _, err = conn.AttachStream(req)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "aether: you cannot steer this run; attached read-only, keystrokes are ignored")
	return stream, nil
}

// describeAttachEnd turns the server dropping a live attach into the
// reason, instead of a bare exit status. Every other end passes through.
func describeAttachEnd(err error) error {
	var exit *cli.RemoteExitError
	if !errors.As(err, &exit) {
		return err
	}
	switch exit.Status {
	case protocol.AttachExitSteerRevoked:
		return errors.New("detached: you can no longer steer this run (its owner, protection, the workspace policy, or your role changed); aether attach --read-only still shows it")
	case protocol.AttachExitMembershipRevoked:
		return errors.New("detached: your membership was removed or is pending approval again")
	}
	return err
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
