package acpkeeper

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// DefaultRetain bounds the agent output a keeper holds for an attacher that
// has not acked it, such as while no server is attached.
const DefaultRetain = 32 << 20

// Config describes the adapter a keeper relays.
type Config struct {
	// Token authenticates attachers. Only the server that started the
	// keeper knows it; the agent shares the keeper's user.
	Token string
	// AgentIn is the adapter's stdin. Closing it asks the adapter to exit.
	AgentIn io.WriteCloser
	// Retain bounds unacked agent output; zero means DefaultRetain.
	Retain int
	Logger *slog.Logger
}

// Keeper relays one adapter's stdio to at most one attacher at a time.
type Keeper struct {
	cfg Config

	// agentMu orders writes to the adapter's stdin.
	agentMu sync.Mutex

	mu   sync.Mutex
	cond *sync.Cond
	// gen is the newest attacher generation; att is that attacher while it
	// is connected.
	gen        uint64
	att        *attacher
	seq, acked uint64
	frames     []*retained
	size       int
	overflow   bool
	unanswered map[string]bool
	out        map[int64]*outgoing
	nextID     int64
	init       json.RawMessage
	session    json.RawMessage
	sessionID  string
	exit       *Exit
	stderr     []byte
	ended      chan struct{}
	endOnce    sync.Once
}

// retained is one agent-to-client frame: an ACP message, or the adapter's
// exit.
type retained struct {
	seq  uint64
	line []byte
	// request is the id of an agent request, which stays retained until it
	// is answered.
	request string
	// owner is the generation of the attacher a response answers, and out
	// the keeper id of its request; both zero for what the agent sent on
	// its own.
	owner uint64
	out   int64
	exit  *Exit
}

// outgoing is a client request the keeper forwarded under its own id.
type outgoing struct {
	gen       uint64
	id        json.RawMessage
	method    string
	tag       string
	sessionID string
	answer    []byte
}

type attacher struct {
	gen    uint64
	conn   io.ReadWriteCloser
	cursor uint64
	resend []*retained
	ctrl   []frame
	// detaching is set once the attacher asked to detach: it gets no more
	// agent output and its messages no longer reach the agent.
	detaching bool
	// preempted marks an attacher a newer one replaced; it gets only its
	// control frames before it is dropped.
	preempted bool
	gone      bool
}

func New(cfg Config) *Keeper {
	if cfg.Retain <= 0 {
		cfg.Retain = DefaultRetain
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	k := &Keeper{
		cfg:        cfg,
		unanswered: make(map[string]bool),
		out:        make(map[int64]*outgoing),
		ended:      make(chan struct{}),
	}
	k.cond = sync.NewCond(&k.mu)
	return k
}

// Ended is closed once the adapter exited and the attacher, if any, acked
// its exit or went away.
func (k *Keeper) Ended() <-chan struct{} { return k.ended }

// Stderr receives the adapter's stderr; the keeper keeps its tail.
func (k *Keeper) Stderr() io.Writer { return stderrWriter{k} }

type stderrWriter struct{ k *Keeper }

func (w stderrWriter) Write(p []byte) (int, error) {
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	w.k.stderr = append(w.k.stderr, p...)
	if over := len(w.k.stderr) - stderrTail; over > 0 {
		w.k.stderr = append(w.k.stderr[:0], w.k.stderr[over:]...)
	}
	return len(p), nil
}

// Pump relays the adapter's stdout until it ends.
func (k *Keeper) Pump(r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if errors.Is(err, errLineTooLong) {
			k.cfg.Logger.Warn("acpkeeper: dropped an agent message over the size limit", "limit", maxLine)
			continue
		}
		if len(line) > 0 {
			k.fromAgent(line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Exited records the adapter's exit and hands it to the attacher.
func (k *Keeper) Exited(code int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.exit = &Exit{Code: code, Stderr: string(k.stderr)}
	if k.att == nil || k.overflow {
		k.end()
		return
	}
	k.appendLocked(&retained{exit: k.exit})
}

func (k *Keeper) end() { k.endOnce.Do(func() { close(k.ended) }) }

func (k *Keeper) fromAgent(line []byte) {
	var h rpcHead
	if err := json.Unmarshal(line, &h); err != nil {
		k.cfg.Logger.Warn("acpkeeper: dropped agent output that is not JSON-RPC", "error", err)
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	// An attached server that falls behind slows the agent rather than
	// overflowing the keeper.
	for k.att != nil && !k.overflow && k.size > k.cfg.Retain/2 {
		k.cond.Wait()
	}
	switch {
	case h.request():
		id := string(h.ID)
		k.unanswered[id] = true
		k.appendLocked(&retained{line: line, request: id})
	case h.response():
		id, err := strconv.ParseInt(string(h.ID), 10, 64)
		e := k.out[id]
		if err != nil || e == nil {
			return
		}
		if !present(h.Error) {
			k.recordLocked(e, h.Result)
		}
		answer, err := withID(line, e.id)
		if err != nil {
			k.cfg.Logger.Warn("acpkeeper: dropped an agent answer", "error", err)
			delete(k.out, id)
			return
		}
		e.answer = answer
		switch {
		case k.att != nil && k.att.gen == e.gen:
			k.appendLocked(&retained{line: answer, owner: e.gen, out: id})
		case e.method != acp.AgentMethodSessionPrompt:
			delete(k.out, id)
		}
	default:
		k.appendLocked(&retained{line: line})
	}
}

// recordLocked keeps what a later attacher needs instead of initializing and
// opening the session again.
func (k *Keeper) recordLocked(e *outgoing, result json.RawMessage) {
	switch e.method {
	case acp.AgentMethodInitialize:
		k.init = result
	case acp.AgentMethodSessionNew:
		var r struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(result, &r) == nil && r.SessionID != "" {
			k.session, k.sessionID = result, r.SessionID
		}
	case acp.AgentMethodSessionLoad, acp.AgentMethodSessionResume:
		k.session, k.sessionID = result, e.sessionID
	}
}

func (k *Keeper) appendLocked(r *retained) {
	k.seq++
	r.seq = k.seq
	if k.overflow {
		return
	}
	k.frames = append(k.frames, r)
	k.size += len(r.line)
	if k.size > k.cfg.Retain {
		k.cfg.Logger.Warn("acpkeeper: unacked agent output exceeds the retention limit; the next attach restores the session instead", "limit", k.cfg.Retain)
		k.overflow, k.frames, k.size = true, nil, 0
		if k.att != nil {
			k.dropLocked(k.att)
		}
		if k.exit != nil {
			k.end()
		}
	}
	k.cond.Broadcast()
}

func (k *Keeper) ackLocked(seq uint64) {
	seq = min(seq, k.seq)
	if seq <= k.acked {
		return
	}
	k.acked = seq
	kept := k.frames[:0]
	for _, r := range k.frames {
		switch {
		case r.seq > seq || (r.request != "" && k.unanswered[r.request]):
			kept = append(kept, r)
			continue
		case r.out != 0:
			delete(k.out, r.out)
		case r.exit != nil:
			k.end()
		}
		k.size -= len(r.line)
	}
	clear(k.frames[len(kept):])
	k.frames = kept
	k.cond.Broadcast()
}

func (k *Keeper) dropLocked(a *attacher) {
	if a.gone {
		return
	}
	a.gone = true
	_ = a.conn.Close()
	if k.att == a {
		k.att = nil
		if k.exit != nil {
			k.end()
		}
	}
	k.cond.Broadcast()
}

// ServeConn serves one attacher until it detaches, disconnects or a newer
// one preempts it.
func (k *Keeper) ServeConn(conn io.ReadWriteCloser) {
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
	a, refusal := k.attach(conn, hello)
	if refusal != nil {
		_ = encodeFrame(conn, *refusal)
		_ = conn.Close()
		return
	}
	go k.send(a)
	for {
		line, err := readLine(br)
		if errors.Is(err, errLineTooLong) {
			k.cfg.Logger.Warn("acpkeeper: dropped a client message over the size limit", "limit", maxLine)
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
		case frameRPC:
			k.fromAttacher(a, f)
		case frameAck:
			k.mu.Lock()
			if k.att == a {
				k.ackLocked(f.Seq)
			}
			k.mu.Unlock()
		case frameDetach:
			k.mu.Lock()
			if k.att == a && !a.detaching {
				a.detaching = true
				a.ctrl = append(a.ctrl, frame{T: frameBye})
				k.cond.Broadcast()
			}
			k.mu.Unlock()
		case frameClose:
			k.mu.Lock()
			current := k.att == a && !a.detaching
			k.mu.Unlock()
			if current {
				k.agentMu.Lock()
				_ = k.cfg.AgentIn.Close()
				k.agentMu.Unlock()
			}
		}
	}
	k.mu.Lock()
	k.dropLocked(a)
	k.mu.Unlock()
}

func (k *Keeper) attach(conn io.ReadWriteCloser, h frame) (*attacher, *frame) {
	k.mu.Lock()
	defer k.mu.Unlock()
	refuse := func(code, message string) *frame {
		return &frame{T: frameRefused, V: Version, Code: code, Error: message, Exit: k.exit}
	}
	switch {
	case subtle.ConstantTimeCompare([]byte(h.Token), []byte(k.cfg.Token)) != 1 || k.cfg.Token == "":
		return nil, refuse(RefusedToken, "")
	case h.V != Version:
		return nil, refuse(RefusedVersion, "the keeper speaks protocol version "+strconv.Itoa(Version))
	case h.Gen <= k.gen:
		return nil, refuse(RefusedStale, "generation "+strconv.FormatUint(k.gen, 10)+" already attached")
	case k.overflow:
		return nil, refuse(RefusedOverflow, "")
	case k.exit != nil:
		return nil, refuse(RefusedExited, "the agent's ACP server exited with code "+strconv.Itoa(k.exit.Code))
	}
	if old := k.att; old != nil {
		// The sender tells it and then drops it.
		old.preempted = true
		old.ctrl = append(old.ctrl, frame{T: framePreempted, Gen: h.Gen})
		k.att = nil
	}
	k.gen = h.Gen
	k.ackLocked(h.Ack)
	state := State{Initialize: k.init, Session: k.session, SessionID: k.sessionID, Stderr: string(k.stderr)}
	// Answers belong to the attacher that asked; a prompt's answer stays
	// for the attacher that adopts the prompt.
	kept := k.frames[:0]
	for _, r := range k.frames {
		if r.owner != 0 {
			k.size -= len(r.line)
			continue
		}
		kept = append(kept, r)
	}
	clear(k.frames[len(kept):])
	k.frames = kept
	var prompt int64
	for _, id := range slices.Sorted(maps.Keys(k.out)) {
		e := k.out[id]
		if e.tag != "" {
			state.Delivered = append(state.Delivered, e.tag)
		}
		if e.method != acp.AgentMethodSessionPrompt {
			delete(k.out, id)
			continue
		}
		if prompt != 0 {
			delete(k.out, prompt)
		}
		prompt = id
	}
	if prompt != 0 {
		e := k.out[prompt]
		state.Prompt = &Prompt{Tag: e.tag, Answered: e.answer != nil}
	}
	a := &attacher{gen: h.Gen, conn: conn, cursor: k.acked}
	for _, r := range k.frames {
		if r.seq <= k.acked && r.request != "" && k.unanswered[r.request] {
			a.resend = append(a.resend, r)
		}
	}
	a.ctrl = []frame{{T: frameWelcome, V: Version, Gen: h.Gen, State: &state}}
	k.att = a
	k.cond.Broadcast()
	return a, nil
}

func (k *Keeper) fromAttacher(a *attacher, f frame) {
	var h rpcHead
	if json.Unmarshal(f.M, &h) != nil {
		return
	}
	k.agentMu.Lock()
	defer k.agentMu.Unlock()
	k.mu.Lock()
	if k.att != a || a.detaching {
		k.mu.Unlock()
		return
	}
	line := []byte(f.M)
	switch {
	case h.request() && h.Method == AdoptMethod:
		k.adoptLocked(a, h.ID)
		k.mu.Unlock()
		return
	case h.request():
		k.nextID++
		id := k.nextID
		k.out[id] = &outgoing{gen: a.gen, id: h.ID, method: h.Method, tag: f.Tag, sessionID: h.Params.SessionID}
		var err error
		if line, err = withID(line, json.RawMessage(strconv.FormatInt(id, 10))); err != nil {
			delete(k.out, id)
			k.mu.Unlock()
			return
		}
	case h.response():
		if !k.unanswered[string(h.ID)] {
			k.mu.Unlock()
			return
		}
		delete(k.unanswered, string(h.ID))
	}
	k.mu.Unlock()
	if _, err := k.cfg.AgentIn.Write(append(line, '\n')); err != nil {
		k.cfg.Logger.Warn("acpkeeper: write to the agent", "error", err)
	}
}

// adoptLocked hands the prompt an earlier attacher left in flight to a,
// which asked for it under id.
func (k *Keeper) adoptLocked(a *attacher, id json.RawMessage) {
	for key, e := range k.out {
		if e.method != acp.AgentMethodSessionPrompt || e.gen == a.gen {
			continue
		}
		e.gen, e.id = a.gen, id
		if e.answer != nil {
			answer, err := withID(e.answer, id)
			if err == nil {
				e.answer = answer
				k.appendLocked(&retained{line: answer, owner: a.gen, out: key})
				return
			}
		}
		return
	}
	answer, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": acp.RequestError{Code: -32002, Message: ErrNothingToAdopt.Error()},
	})
	k.appendLocked(&retained{line: answer, owner: a.gen})
}

// send writes the attacher's frames in order until it is gone.
func (k *Keeper) send(a *attacher) {
	w := bufio.NewWriterSize(a.conn, 64<<10)
	for {
		k.mu.Lock()
		var batch []frame
		for {
			if a.gone {
				k.mu.Unlock()
				return
			}
			if batch = k.pendingLocked(a); len(batch) > 0 {
				break
			}
			k.cond.Wait()
		}
		k.mu.Unlock()
		var err error
		for _, f := range batch {
			if err = encodeFrame(w, f); err != nil {
				break
			}
		}
		if err == nil {
			err = w.Flush()
		}
		k.mu.Lock()
		if err != nil || a.preempted {
			k.dropLocked(a)
			k.mu.Unlock()
			return
		}
		k.mu.Unlock()
	}
}

func (k *Keeper) pendingLocked(a *attacher) []frame {
	batch := a.ctrl
	a.ctrl = nil
	if a.detaching || a.preempted {
		return batch
	}
	for _, r := range a.resend {
		batch = append(batch, r.frame())
	}
	a.resend = nil
	for _, r := range k.frames {
		if r.seq <= a.cursor || (r.owner != 0 && r.owner != a.gen) {
			continue
		}
		batch = append(batch, r.frame())
		a.cursor = r.seq
	}
	return batch
}

func (r *retained) frame() frame {
	if r.exit != nil {
		return frame{T: frameExit, Seq: r.seq, Exit: r.exit}
	}
	return frame{T: frameRPC, Seq: r.seq, M: r.line}
}

var errLineTooLong = errors.New("acpkeeper: line exceeds the size limit")

// readLine reads one line without its newline. A line over maxLine is
// discarded with errLineTooLong.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxLine {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, errLineTooLong
		}
		line = append(line, chunk...)
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			if len(line) > 0 && errors.Is(err, io.EOF) {
				return line, nil
			}
			return nil, err
		}
		return line[:len(line)-1], nil
	}
}

func encodeFrame(w io.Writer, f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
