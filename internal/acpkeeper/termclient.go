package acpkeeper

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// TermClient is a server's attachment to a terminal keeper. It serves as the
// terminal's runtime attachment: Stdout yields the PTY's output from the
// offset the attach replayed from, and Stdin writes the PTY's input.
type TermClient struct {
	conn  io.ReadWriteCloser
	br    *bufio.Reader
	state TermState
	from  uint64
	out   *io.PipeReader
	in    *io.PipeWriter
	done  chan struct{}
	wmu   sync.Mutex

	mu   sync.Mutex
	exit *Exit
	err  error
}

// AttachTerm sends hello over conn, asking to replay output from offset
// from, and returns the attachment once the keeper welcomed it, or the
// keeper's *RefusedError. It closes conn on error.
func AttachTerm(ctx context.Context, conn io.ReadWriteCloser, hello Hello, from uint64) (*TermClient, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	br, state, err := handshake(conn, frame{T: frameHello, V: Version, Gen: hello.Gen, Token: hello.Token, Ack: from})
	if err == nil && state.Term == nil {
		err = fmt.Errorf("acpkeeper: the keeper is not a terminal's")
	}
	if !stop() || err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("acpkeeper: attach: %w", ctx.Err())
		}
		return nil, err
	}
	out, in := io.Pipe()
	c := &TermClient{conn: conn, br: br, state: *state.Term, from: min(max(from, state.Term.Start), state.Term.End),
		out: out, in: in, done: make(chan struct{})}
	go c.read()
	return c, nil
}

// State is what the keeper's welcome reported.
func (c *TermClient) State() TermState { return c.state }

// From is the offset of the first byte Stdout yields.
func (c *TermClient) From() uint64 { return c.from }

func (c *TermClient) Stdin() io.WriteCloser { return termInput{c} }

// Stdout yields the PTY's output. A read never spans the offset the keeper
// had reached when it welcomed this attachment, State().End. It ends at the
// program's exit, or with ErrLost or ErrPreempted.
func (c *TermClient) Stdout() io.Reader { return c.out }

func (c *TermClient) Stderr() io.Reader { return strings.NewReader("") }

func (c *TermClient) Resize(_ context.Context, cols, rows uint) error {
	if cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("acpkeeper: terminal dimensions must be between 1 and 65535")
	}
	return c.send(frame{T: frameResize, Cols: uint16(cols), Rows: uint16(rows)})
}

// Close ends the attachment; the keeper keeps the terminal running.
func (c *TermClient) Close() error {
	err := c.conn.Close()
	_ = c.out.CloseWithError(io.ErrClosedPipe)
	return err
}

// Done is closed once the attachment ended.
func (c *TermClient) Done() <-chan struct{} { return c.done }

// Exit is the program's exit, once the keeper reported it.
func (c *TermClient) Exit() *Exit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit
}

// Err is why the attachment ended: nil after the program's exit.
func (c *TermClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

type termInput struct{ c *TermClient }

func (w termInput) Write(p []byte) (int, error) {
	for i := 0; i < len(p); i += termChunk {
		if err := w.c.send(frame{T: frameInput, Data: p[i:min(i+termChunk, len(p))]}); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

// Close leaves the PTY open for later input, as a runtime attachment does.
func (termInput) Close() error { return nil }

func (c *TermClient) send(f frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := encodeFrame(c.conn, f); err != nil {
		return fmt.Errorf("acpkeeper: write to the keeper: %w", err)
	}
	return nil
}

func (c *TermClient) read() {
	err := ErrLost
	defer func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		_ = c.in.CloseWithError(err)
		close(c.done)
	}()
	for {
		line, readErr := readLine(c.br)
		if readErr != nil {
			return
		}
		var f frame
		if json.Unmarshal(line, &f) != nil {
			continue
		}
		switch f.T {
		case frameOutput:
			data := f.Data
			if f.Seq < c.state.End && f.Seq+uint64(len(data)) > c.state.End {
				split := c.state.End - f.Seq
				if _, werr := c.in.Write(data[:split]); werr != nil {
					return
				}
				data = data[split:]
			}
			if _, werr := c.in.Write(data); werr != nil {
				return
			}
		case frameExit:
			c.mu.Lock()
			c.exit = f.Exit
			c.mu.Unlock()
			err = nil
			return
		case framePreempted:
			err = ErrPreempted
			return
		}
	}
}
