package acpkeeper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// ackInterval paces acks of agent output the attacher handed on. A crash
// replays at most this much output twice; a detach acks it all.
const ackInterval = 100 * time.Millisecond

// ErrLost ends a client whose keeper connection closed without the
// adapter's exit or a detach: the keeper may still run.
var ErrLost = errors.New("acpkeeper: the keeper connection closed")

// ErrPreempted ends a client a newer attacher replaced.
var ErrPreempted = errors.New("acpkeeper: a newer server attached to the keeper")

// Hello is what an attacher announces.
type Hello struct {
	// Gen orders attachers: a keeper refuses a generation no newer than
	// the newest it saw, and a newer one preempts the attached one.
	Gen   uint64
	Token string
}

// Client is a server's attachment to a keeper. Reader and Writer carry the
// ACP stream for acphost.
type Client struct {
	conn  io.ReadWriteCloser
	br    *bufio.Reader
	state State
	out   *io.PipeReader
	in    *io.PipeWriter
	kick  chan struct{}
	done  chan struct{}

	wmu sync.Mutex

	mu        sync.Mutex
	seq       uint64
	acked     uint64
	detaching bool
	partial   []byte
	exit      *Exit
	err       error
}

// Attach sends hello over conn and returns the attachment once the keeper
// welcomed it, or the keeper's *RefusedError. It closes conn on error.
func Attach(ctx context.Context, conn io.ReadWriteCloser, hello Hello) (*Client, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	c, err := attach(conn, hello)
	if !stop() || err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("acpkeeper: attach: %w", ctx.Err())
		}
		return nil, err
	}
	go c.read()
	go c.ackLoop()
	return c, nil
}

func attach(conn io.ReadWriteCloser, hello Hello) (*Client, error) {
	if err := encodeFrame(conn, frame{T: frameHello, V: Version, Gen: hello.Gen, Token: hello.Token}); err != nil {
		return nil, fmt.Errorf("acpkeeper: send hello: %w", err)
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(br)
	if err != nil {
		return nil, fmt.Errorf("acpkeeper: read the keeper's answer: %w", err)
	}
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		return nil, fmt.Errorf("acpkeeper: decode the keeper's answer: %w", err)
	}
	switch {
	case f.T == frameRefused:
		return nil, &RefusedError{Code: f.Code, Message: f.Error, Exit: f.Exit}
	case f.T != frameWelcome || f.State == nil:
		return nil, fmt.Errorf("acpkeeper: unexpected %q frame instead of welcome", f.T)
	case f.V != Version:
		return nil, &RefusedError{Code: RefusedVersion, Message: fmt.Sprintf("the keeper welcomed protocol version %d", f.V)}
	}
	out, in := io.Pipe()
	return &Client{
		conn: conn, br: br, state: *f.State, out: out, in: in,
		kick: make(chan struct{}, 1), done: make(chan struct{}),
	}, nil
}

// State is what the keeper's welcome reported.
func (c *Client) State() State { return c.state }

// Reader yields the agent's ACP messages; it ends when the attachment does.
func (c *Client) Reader() io.Reader { return c.out }

// Writer sends ACP messages to the agent. Closing it closes the adapter's
// stdin.
func (c *Client) Writer() io.WriteCloser { return writer{c} }

// Done is closed once the attachment ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Exit is the adapter's exit, once the keeper reported it.
func (c *Client) Exit() *Exit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit
}

// Err is why the attachment ended: nil after a detach or the adapter's exit,
// ErrLost when the connection closed with neither.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

type writer struct{ c *Client }

func (w writer) Write(p []byte) (int, error) { return w.c.write(p, "") }

func (w writer) Close() error {
	w.c.mu.Lock()
	detaching := w.c.detaching
	w.c.mu.Unlock()
	if detaching {
		return nil
	}
	return w.c.send(frame{T: frameClose})
}

// WriteTagged writes p, whose requests deliver what tag names.
func (w writer) WriteTagged(p []byte, tag string) (int, error) { return w.c.write(p, tag) }

func (c *Client) write(p []byte, tag string) (int, error) {
	c.mu.Lock()
	if c.detaching {
		c.mu.Unlock()
		return len(p), nil
	}
	buf := append(c.partial, p...)
	var lines [][]byte
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(buf[:i]); len(line) > 0 {
			lines = append(lines, line)
		}
		buf = buf[i+1:]
	}
	c.partial = append([]byte(nil), buf...)
	c.mu.Unlock()
	for _, line := range lines {
		if err := c.send(frame{T: frameRPC, M: line, Tag: tag}); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (c *Client) send(f frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := encodeFrame(c.conn, f); err != nil {
		return fmt.Errorf("acpkeeper: write to the keeper: %w", err)
	}
	return nil
}

func (c *Client) read() {
	err := ErrLost
	// After a bye the pipe stays open until Close, so acphost handles all
	// the keeper sent before it sees the stream end.
	bye := false
	defer func() { c.finish(err, !bye) }()
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
		case frameRPC:
			if _, werr := c.in.Write(append(f.M, '\n')); werr != nil {
				err = nil
				return
			}
			c.handed(f.Seq)
		case frameExit:
			c.mu.Lock()
			c.exit = f.Exit
			c.mu.Unlock()
			c.handed(f.Seq)
			c.flushAck()
			err = nil
			return
		case frameBye:
			err, bye = nil, true
			return
		case framePreempted:
			err = ErrPreempted
			return
		}
	}
}

func (c *Client) handed(seq uint64) {
	c.mu.Lock()
	c.seq = max(c.seq, seq)
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *Client) ackLoop() {
	tick := time.NewTicker(ackInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
		}
		select {
		case <-c.kick:
			c.flushAck()
		default:
		}
	}
}

func (c *Client) flushAck() {
	c.mu.Lock()
	seq := c.seq
	due := seq > c.acked
	c.acked = max(c.acked, seq)
	c.mu.Unlock()
	if due {
		_ = c.send(frame{T: frameAck, Seq: seq})
	}
}

func (c *Client) finish(err error, end bool) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	if end {
		_ = c.in.Close()
	}
	close(c.done)
}

// Detach asks the keeper to keep the adapter running without this
// attachment and returns once the keeper confirmed and acphost was handed all
// it sent before. What acphost writes from then on is dropped. Close ends
// the stream acphost reads.
func (c *Client) Detach(ctx context.Context) error {
	c.mu.Lock()
	c.detaching = true
	c.mu.Unlock()
	sent := make(chan error, 1)
	go func() { sent <- c.send(frame{T: frameDetach}) }()
	select {
	case err := <-sent:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("acpkeeper: detach: %w", ctx.Err())
	}
	select {
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return fmt.Errorf("acpkeeper: detach: %w", ctx.Err())
	}
}

// Close acks the output acphost was handed, unless a write to the keeper is
// stuck, and ends the attachment without waiting for the keeper.
func (c *Client) Close() error {
	if c.wmu.TryLock() {
		c.mu.Lock()
		seq, due := c.seq, c.seq > c.acked
		c.acked = max(c.acked, c.seq)
		c.mu.Unlock()
		if due {
			_ = encodeFrame(c.conn, frame{T: frameAck, Seq: seq})
		}
		c.wmu.Unlock()
	}
	err := c.conn.Close()
	_ = c.out.CloseWithError(io.ErrClosedPipe)
	_ = c.in.Close()
	return err
}
