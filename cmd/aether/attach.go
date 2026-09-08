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
	stream, replay, err := openAttach(conn, runID, cols, rows, *readOnly, *shell)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	return describeAttachEnd(copyRaw(stream, replay))
}

// openAttach asks to steer unless told otherwise, and drops to a read-only
// mirror when the server refuses steer, as the dashboard's terminal does.
// Shell tabs are always writable and report a refusal without downgrading.
// It also returns how many bytes of scrollback replay precede the live
// output, which is what replayGate below mutes over.
func openAttach(conn *cli.Conn, runID string, cols, rows uint, readOnly bool, shell string) (io.ReadWriteCloser, int, error) {
	if shell != "" {
		readOnly = false
	}
	req := protocol.AttachRequest{RunID: runID, Cols: cols, Rows: rows, ReadOnly: readOnly, Shell: shell}
	stream, ack, err := conn.AttachStream(req)
	if err == nil {
		return stream, ack.Replay, nil
	}
	if shell != "" || readOnly || ack.OK || ack.Code != protocol.CodeDenied {
		return nil, 0, err
	}
	req.ReadOnly = true
	stream, ack, err = conn.AttachStream(req)
	if err != nil {
		return nil, 0, err
	}
	fmt.Fprintln(os.Stderr, "aether: you cannot steer this run; attached read-only, keystrokes are ignored")
	return stream, ack.Replay, nil
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

func copyRaw(stream io.ReadWriteCloser, replay int) error {
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
	return copyRawStreams(stream, input, os.Stdout, replay)
}

// replayGate drops what the terminal says while the scrollback replay is
// still being handed to it. A replay carries whatever queries the agent's
// TUI left in the scrollback - device attributes, colour reads, XTVERSION,
// XTGETTCAP, the kitty keyboard probe - and a real terminal answers them
// as if they had just been asked. Those answers travel on the same channel
// as typing, so without this, attaching to another member's run and
// touching nothing would send bytes the server counts as steering it. The
// dashboard mutes the same window (web/src/routes/terminal/attach.ts).
//
// The gate opens once the replay has been written out, which is the last
// moment the client can attribute a byte to the replay rather than to the
// person. Keystrokes during the replay are dropped with it; the dashboard
// drops them too, and the replay of a live run is written in one pass.
type replayGate struct {
	mu        sync.Mutex
	remaining int
}

func (g *replayGate) wrote(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.remaining -= n
}

func (g *replayGate) open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.remaining <= 0
}

// gatedReader reads from r and swallows anything that arrives before the
// gate opens.
type gatedReader struct {
	r    io.Reader
	gate *replayGate
}

func (g *gatedReader) Read(p []byte) (int, error) {
	for {
		n, err := g.r.Read(p)
		if err != nil || n == 0 || g.gate.open() {
			return n, err
		}
	}
}

func (g *gatedReader) Close() error {
	if c, ok := g.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// countingWriter reports what it has written to the gate.
type countingWriter struct {
	w    io.Writer
	gate *replayGate
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.gate.wrote(n)
	return n, err
}

func copyRawStreams(stream io.ReadWriteCloser, input io.Reader, output io.Writer, replay int) error {
	if replay > 0 {
		gate := &replayGate{remaining: replay}
		input = &gatedReader{r: input, gate: gate}
		output = &countingWriter{w: output, gate: gate}
	}
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
