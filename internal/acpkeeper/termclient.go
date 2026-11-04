package acpkeeper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// termWriteTimeout bounds a write to the keeper, and termFenceWait the wait
// for the keeper to confirm a fence, whatever the caller's context allows.
const (
	termWriteTimeout = 5 * time.Second
	termFenceWait    = 2 * time.Second
)

// ContextWriter bounds and interrupts one write without closing the stream.
type ContextWriter interface {
	WriteContext(context.Context, []byte) (int, error)
}

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
	kick  chan struct{}
	wmu   sync.Mutex
	// imu serializes input; inputSeq numbers its frames.
	imu      sync.Mutex
	inputSeq uint64

	mu       sync.Mutex
	exit     *Exit
	err      error
	answered uint64
	// inputDone and fenced are the keeper's newest in_done and fenced
	// frames; inputKick wakes the writer waiting for them.
	inputDone frame
	fenced    uint64
	inputKick chan struct{}
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
		out: out, in: in, done: make(chan struct{}), kick: make(chan struct{}, 1), inputKick: make(chan struct{}, 1)}
	go c.read()
	go c.answerLoop()
	return c, nil
}

// Answered reports that the terminal queries in the output before offset
// seq were answered, their replies written to Stdin. It does not block.
func (c *TermClient) Answered(seq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq <= c.answered {
		return
	}
	c.answered = seq
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// answerLoop tells the keeper what Answered reported, after the replies
// Stdin sent before.
func (c *TermClient) answerLoop() {
	var told uint64
	for {
		select {
		case <-c.done:
			return
		case <-c.kick:
		}
		c.mu.Lock()
		seq := c.answered
		c.mu.Unlock()
		if seq > told && c.send(context.Background(), frame{T: frameAnswered, Seq: seq}) == nil {
			told = seq
		}
	}
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

func (c *TermClient) Resize(ctx context.Context, cols, rows uint) error {
	if cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("acpkeeper: terminal dimensions must be between 1 and 65535")
	}
	return c.send(ctx, frame{T: frameResize, Cols: uint16(cols), Rows: uint16(rows)})
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
	ctx, cancel := context.WithTimeout(context.Background(), termWriteTimeout)
	defer cancel()
	return w.WriteContext(ctx, p)
}

// WriteContext returns once the program took p, or once the keeper confirmed
// that the rest of p never reaches it. When the keeper confirms neither in
// time, the attachment ends, which ends its input.
func (w termInput) WriteContext(ctx context.Context, p []byte) (int, error) {
	c := w.c
	c.imu.Lock()
	defer c.imu.Unlock()
	for i := 0; i < len(p); {
		chunk := p[i:min(i+termChunk, len(p))]
		c.inputSeq++
		seq := c.inputSeq
		if err := c.send(ctx, frame{T: frameInput, Seq: seq, Data: chunk}); err != nil {
			return i, err
		}
		done, err := c.awaitInput(ctx, seq)
		if err != nil {
			return i, err
		}
		i += done.N
		switch {
		case done.Error != "":
			return i, fmt.Errorf("acpkeeper: write to the terminal: %s", done.Error)
		case done.N < len(chunk) && ctx.Err() != nil:
			return i, fmt.Errorf("acpkeeper: write to the terminal: %w", ctx.Err())
		case done.N < len(chunk):
			return i, fmt.Errorf("acpkeeper: write to the terminal: %w", io.ErrShortWrite)
		}
	}
	return len(p), nil
}

// awaitInput waits for the keeper's in_done for input frame seq. When ctx
// ends first, it fences the frame.
func (c *TermClient) awaitInput(ctx context.Context, seq uint64) (frame, error) {
	if done, err := c.waitInput(ctx, seq, false); err == nil || ctx.Err() == nil {
		return done, err
	}
	fenceCtx, cancel := context.WithTimeout(context.Background(), termFenceWait)
	defer cancel()
	err := c.send(fenceCtx, frame{T: frameFence, Seq: seq})
	if err == nil {
		var done frame
		if done, err = c.waitInput(fenceCtx, seq, true); err == nil {
			if done.Seq == seq {
				return done, nil
			}
			return frame{}, fmt.Errorf("acpkeeper: write to the terminal: %w", ctx.Err())
		}
	}
	_ = c.Close()
	return frame{}, fmt.Errorf("acpkeeper: the keeper did not confirm the terminal input stopped, so the attachment ended: %w", errors.Join(ctx.Err(), err))
}

// waitInput returns the in_done for frame seq or, with fenced set, an empty
// frame once the keeper confirmed it fenced seq.
func (c *TermClient) waitInput(ctx context.Context, seq uint64, fenced bool) (frame, error) {
	for {
		c.mu.Lock()
		done, fence := c.inputDone, c.fenced
		c.mu.Unlock()
		switch {
		case done.Seq == seq:
			return done, nil
		case fenced && fence >= seq:
			return frame{}, nil
		}
		select {
		case <-c.inputKick:
		case <-c.done:
			return frame{}, fmt.Errorf("acpkeeper: write to the terminal: %w", c.Err())
		case <-ctx.Done():
			return frame{}, fmt.Errorf("acpkeeper: write to the terminal: %w", ctx.Err())
		}
	}
}

// Close leaves the PTY open for later input, as a runtime attachment does.
func (termInput) Close() error { return nil }

// send writes f within ctx and termWriteTimeout. A write cut off part way
// leaves the stream unframed, so it ends the attachment.
func (c *TermClient) send(ctx context.Context, f frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("acpkeeper: write to the keeper: %w", err)
	}
	b = append(b, '\n')
	ctx, cancel := context.WithTimeout(ctx, termWriteTimeout)
	defer cancel()
	if w, ok := c.conn.(ContextWriter); ok {
		_, err = w.WriteContext(ctx, b)
	} else {
		_, err = c.conn.Write(b)
	}
	if err != nil {
		_ = c.Close()
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
		case frameInputDone, frameFenced:
			c.mu.Lock()
			if f.T == frameFenced {
				c.fenced = max(c.fenced, f.Seq)
			} else {
				c.inputDone = f
			}
			c.mu.Unlock()
			select {
			case c.inputKick <- struct{}{}:
			default:
			}
		}
	}
}
