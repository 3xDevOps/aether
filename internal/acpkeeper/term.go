package acpkeeper

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// DefaultTermRetain bounds the terminal output a keeper retains for a server
// that attaches later.
const DefaultTermRetain = 2 << 20

// termChunk bounds the data of one output or input frame.
const termChunk = 32 << 10

// termInputQueue bounds the input an attacher queued for a program that does
// not read it; a client sends one input frame at a time.
const termInputQueue = 4 * termChunk

// ErrInputFull is a TermConfig.Input that cannot take more input yet.
var ErrInputFull = errors.New("the terminal's input is full")

// TermConfig describes the terminal a keeper relays.
type TermConfig struct {
	// Token authenticates attachers.
	Token string
	// Input is the PTY master the program reads its input from. It must not
	// block: it writes what fits and fails with ErrInputFull when nothing
	// does.
	Input io.Writer
	// Resize sets the PTY's geometry.
	Resize     func(cols, rows uint16) error
	Cols, Rows uint16
	// Retain bounds the retained output; zero means DefaultTermRetain.
	Retain int
	Logger *slog.Logger
}

// Term relays one PTY to at most one attacher at a time. It retains the
// newest output so a newer attacher replays what it missed.
type Term struct {
	cfg TermConfig

	mu   sync.Mutex
	cond *sync.Cond
	gen  uint64
	att  *termAttacher
	// buf holds the output from offset end-len(buf) to end.
	buf []byte
	end uint64
	// answered is the offset up to which an attacher answered the
	// terminal queries in the output.
	answered   uint64
	cols, rows uint16
	exit       *Exit
	ended      chan struct{}
	endOnce    sync.Once
}

type termAttacher struct {
	gen    uint64
	conn   io.ReadWriteCloser
	cursor uint64
	ctrl   []frame
	// input is the input frames the program has yet to take, oldest
	// first; none at or below fence reaches it.
	input     []frame
	inputSize int
	fence     uint64
	// exited is set once the attacher was sent the program's exit.
	exited, preempted, gone bool
}

func NewTerm(cfg TermConfig) *Term {
	if cfg.Retain <= 0 {
		cfg.Retain = DefaultTermRetain
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	t := &Term{cfg: cfg, cols: cfg.Cols, rows: cfg.Rows, ended: make(chan struct{})}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// Ended is closed once an attacher was sent the program's exit.
func (t *Term) Ended() <-chan struct{} { return t.ended }

// Write records the program's output.
func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// An attached server that falls behind slows the program, as a
	// terminal nobody reads does, rather than losing output it was sent.
	for t.att != nil && t.end-t.att.cursor > uint64(t.cfg.Retain/2) {
		t.cond.Wait()
	}
	t.buf = append(t.buf, p...)
	t.end += uint64(len(p))
	if len(t.buf) > t.cfg.Retain+t.cfg.Retain/4 {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.cfg.Retain:]...)
	}
	t.cond.Broadcast()
	return len(p), nil
}

// Exited records the program's exit, which follows all its output.
func (t *Term) Exited(code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.exit = &Exit{Code: code}
	t.cond.Broadcast()
}

func (t *Term) start() uint64 { return t.end - uint64(len(t.buf)) }

// ServeConn serves one attacher until it disconnects or a newer one
// preempts it.
func (t *Term) ServeConn(conn io.ReadWriteCloser) {
	br := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(br)
	var hello frame
	if err == nil {
		err = json.Unmarshal(line, &hello)
	}
	if err != nil || hello.T != frameHello {
		_ = conn.Close()
		return
	}
	a, refusal := t.attach(conn, hello)
	if refusal != nil {
		_ = encodeFrame(conn, *refusal)
		_ = conn.Close()
		return
	}
	go t.send(a)
	go t.feed(a)
	for {
		line, err := readLine(br)
		if errors.Is(err, errLineTooLong) {
			continue
		}
		if err != nil {
			break
		}
		var f frame
		if json.Unmarshal(line, &f) != nil {
			continue
		}
		switch f.T {
		case frameInput:
			t.queueInput(a, f)
		case frameFence:
			t.fenceInput(a, f.Seq)
		case frameResize:
			t.resize(a, f.Cols, f.Rows)
		case frameAnswered:
			t.mu.Lock()
			if t.att == a {
				t.answered = max(t.answered, min(f.Seq, t.end))
			}
			t.mu.Unlock()
		}
	}
	t.mu.Lock()
	t.dropLocked(a)
	t.mu.Unlock()
}

func (t *Term) attach(conn io.ReadWriteCloser, h frame) (*termAttacher, *frame) {
	t.mu.Lock()
	defer t.mu.Unlock()
	refuse := func(code, message string) *frame {
		return &frame{T: frameRefused, V: Version, Code: code, Error: message, Exit: t.exit}
	}
	if code, message := checkHello(h, t.cfg.Token, t.gen); code != "" {
		return nil, refuse(code, message)
	}
	select {
	case <-t.ended:
		return nil, refuse(RefusedExited, "")
	default:
	}
	if old := t.att; old != nil {
		old.preempted = true
		old.ctrl = append(old.ctrl, frame{T: framePreempted, Gen: h.Gen})
		t.att = nil
	}
	t.gen = h.Gen
	// Ack names the offset the attacher replays from.
	cursor := min(max(h.Ack, t.start()), t.end)
	state := State{Term: &TermState{Start: t.start(), End: t.end, Answered: t.answered, Cols: t.cols, Rows: t.rows}}
	a := &termAttacher{gen: h.Gen, conn: conn, cursor: cursor, ctrl: []frame{{T: frameWelcome, V: Version, Gen: h.Gen, State: &state}}}
	t.att = a
	t.cond.Broadcast()
	return a, nil
}

func (t *Term) queueInput(a *termAttacher, f frame) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.att != a || f.Seq <= a.fence {
		return
	}
	if a.inputSize+len(f.Data) > termInputQueue {
		t.cfg.Logger.Warn("acpkeeper: the attacher queued more terminal input than the program takes; dropping it", "limit", termInputQueue)
		t.dropLocked(a)
		return
	}
	a.input = append(a.input, f)
	a.inputSize += len(f.Data)
	t.cond.Broadcast()
}

// fenceInput drops the attacher's input up to frame seq that has not
// reached the program, and tells the attacher none of it will.
func (t *Term) fenceInput(a *termAttacher, seq uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a.fence = max(a.fence, seq)
	kept := a.input[:0]
	for _, f := range a.input {
		if f.Seq > a.fence {
			kept = append(kept, f)
			continue
		}
		a.inputSize -= len(f.Data)
	}
	clear(a.input[len(kept):])
	a.input = kept
	a.ctrl = append(a.ctrl, frame{T: frameFenced, Seq: seq})
	t.cond.Broadcast()
}

// feed writes the attacher's input to the program, one frame at a time, and
// reports how much of each reached it. Input never blocks, so the checks
// hold across each write: no input of a preempted attacher, or fenced
// input, reaches the program afterwards.
func (t *Term) feed(a *termAttacher) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		for len(a.input) == 0 && !a.gone {
			t.cond.Wait()
		}
		if a.gone {
			return
		}
		f := a.input[0]
		var n int
		var err error
		for n < len(f.Data) && t.att == a && !a.gone && f.Seq > a.fence {
			var w int
			w, err = t.cfg.Input.Write(f.Data[n:])
			n += max(w, 0)
			if !errors.Is(err, ErrInputFull) {
				if err != nil {
					t.cfg.Logger.Warn("acpkeeper: write to the terminal", "error", err)
					break
				}
				continue
			}
			t.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			t.mu.Lock()
		}
		if len(a.input) > 0 && a.input[0].Seq == f.Seq {
			a.input = a.input[1:]
			a.inputSize -= len(f.Data)
		}
		done := frame{T: frameInputDone, Seq: f.Seq, N: n}
		if err != nil && !errors.Is(err, ErrInputFull) {
			done.Error = err.Error()
		}
		a.ctrl = append(a.ctrl, done)
		t.cond.Broadcast()
	}
}

func (t *Term) resize(a *termAttacher, cols, rows uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.att != a || cols == 0 || rows == 0 {
		return
	}
	if err := t.cfg.Resize(cols, rows); err != nil {
		t.cfg.Logger.Warn("acpkeeper: resize the terminal", "error", err)
		return
	}
	t.cols, t.rows = cols, rows
}

func (t *Term) dropLocked(a *termAttacher) {
	if a.gone {
		return
	}
	a.gone = true
	_ = a.conn.Close()
	if t.att == a {
		t.att = nil
	}
	t.cond.Broadcast()
}

// send writes the attacher's frames in order until it is gone.
func (t *Term) send(a *termAttacher) {
	w := bufio.NewWriterSize(a.conn, 64<<10)
	for {
		t.mu.Lock()
		var batch []frame
		for {
			if a.gone {
				t.mu.Unlock()
				return
			}
			if batch = t.pendingLocked(a); len(batch) > 0 {
				break
			}
			t.cond.Wait()
		}
		t.mu.Unlock()
		var err error
		for _, f := range batch {
			if err = encodeFrame(w, f); err != nil {
				break
			}
		}
		if err == nil {
			err = w.Flush()
		}
		t.mu.Lock()
		if a.exited && err == nil {
			t.endOnce.Do(func() { close(t.ended) })
		}
		if err != nil || a.exited || (a.preempted && len(a.ctrl) == 0) {
			t.dropLocked(a)
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
	}
}

func (t *Term) pendingLocked(a *termAttacher) []frame {
	batch := a.ctrl
	a.ctrl = nil
	if a.preempted || a.exited {
		return batch
	}
	for a.cursor < t.end {
		from := a.cursor - t.start()
		n := min(uint64(len(t.buf))-from, termChunk)
		batch = append(batch, frame{T: frameOutput, Seq: a.cursor, Data: append([]byte(nil), t.buf[from:from+n]...)})
		a.cursor += n
	}
	t.cond.Broadcast()
	if t.exit != nil {
		a.exited = true
		batch = append(batch, frame{T: frameExit, Exit: t.exit})
	}
	return batch
}
