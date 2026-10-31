package acpkeeper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	token   = "tok"
	timeout = 5 * time.Second
)

func note(n int) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"n":%d}}`, n)
}

func req(id, method string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q,"params":{"sessionId":"s1"}}`, id, method)
}

func resp(id, result string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result)
}

func head(t *testing.T, line string) rpcHead {
	t.Helper()
	var h rpcHead
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return h
}

func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("stream ended, wanted a line")
		}
		return l
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a line")
	}
	return ""
}

func isClosed(t *testing.T, lines <-chan string) bool {
	t.Helper()
	select {
	case _, ok := <-lines:
		if ok {
			t.Fatal("got a line, wanted the stream to end")
		}
		return true
	case <-time.After(timeout):
		return false
	}
}

// agent is a scripted adapter wired to a keeper.
type agent struct {
	t           *testing.T
	k           *Keeper
	out         *io.PipeWriter
	in          chan string
	stdinClosed chan struct{}
	pumped      chan struct{}
}

func newAgent(t *testing.T, retain int) *agent {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	a := &agent{
		t: t, out: outW, in: make(chan string, 64),
		stdinClosed: make(chan struct{}), pumped: make(chan struct{}),
		k: New(Config{Token: token, AgentIn: inW, Retain: retain, Logger: slog.New(slog.DiscardHandler)}),
	}
	go func() {
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			a.in <- sc.Text()
		}
		close(a.stdinClosed)
	}()
	go func() {
		_ = a.k.Pump(outR)
		close(a.pumped)
	}()
	t.Cleanup(func() { _ = outW.Close(); _ = inW.Close() })
	return a
}

// send emits one line and returns once the keeper processed it: the blank
// line that follows is only read after the keeper handled the line before.
func (a *agent) send(line string) {
	a.t.Helper()
	for _, part := range []string{line + "\n", "\n"} {
		if _, err := io.WriteString(a.out, part); err != nil {
			a.t.Fatal(err)
		}
	}
}

func (a *agent) recv() string { a.t.Helper(); return next(a.t, a.in) }

// recvReq reads the next line the adapter received and returns its keeper id.
func (a *agent) recvReq(method string) string {
	a.t.Helper()
	line := a.recv()
	h := head(a.t, line)
	if h.Method != method {
		a.t.Fatalf("adapter received %q, want method %q", line, method)
	}
	return string(h.ID)
}

func (a *agent) conn() net.Conn {
	c, s := net.Pipe()
	go a.k.ServeConn(s)
	a.t.Cleanup(func() { _ = c.Close() })
	return c
}

func (a *agent) attachWith(h Hello) (*peer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := Attach(ctx, a.conn(), h)
	if err != nil {
		return nil, err
	}
	p := &peer{Client: c, t: a.t, lines: make(chan string, 64)}
	go func() {
		sc := bufio.NewScanner(c.Reader())
		for sc.Scan() {
			// Handled at once, as acphost handles it after what precedes it.
			var m struct {
				Method string `json:"method"`
				Params struct {
					Seq uint64 `json:"seq"`
				} `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &m) == nil && m.Method == HandledMethod {
				c.Writer().(interface{ Handled(uint64) }).Handled(m.Params.Seq)
				continue
			}
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	return p, nil
}

func (a *agent) attach(gen uint64) *peer {
	a.t.Helper()
	p, err := a.attachWith(Hello{Gen: gen, Token: token})
	if err != nil {
		a.t.Fatalf("attach gen %d: %v", gen, err)
	}
	return p
}

func (a *agent) refusal(h Hello) *RefusedError {
	a.t.Helper()
	_, err := a.attachWith(h)
	var re *RefusedError
	if !errors.As(err, &re) {
		a.t.Fatalf("attach %+v: err = %v, want *RefusedError", h, err)
	}
	return re
}

// barrier proves everything an attacher wrote before it reached the adapter,
// and that nothing else did.
func (a *agent) barrier(p *peer) {
	a.t.Helper()
	p.write(req("999", "barrier"))
	a.recvReq("barrier")
}

func waitAcked(t *testing.T, k *Keeper, seq uint64) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		k.mu.Lock()
		acked := k.acked
		k.mu.Unlock()
		if acked >= seq {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("keeper acked %d, want %d", acked, seq)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type peer struct {
	*Client
	t     *testing.T
	lines chan string
}

func (p *peer) write(line string) {
	p.t.Helper()
	if _, err := p.Writer().Write([]byte(line + "\n")); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) writeTagged(line, tag string, turn int64) {
	p.t.Helper()
	w := p.Writer().(interface {
		WriteTagged([]byte, string, int64) (int, error)
	})
	if _, err := w.WriteTagged([]byte(line+"\n"), tag, turn); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) next() string { p.t.Helper(); return next(p.t, p.lines) }

func (p *peer) wantNext(want string) {
	p.t.Helper()
	if got := p.next(); got != want {
		p.t.Fatalf("got %s, want %s", got, want)
	}
}

// raw speaks the frame protocol directly.
type raw struct {
	t      *testing.T
	conn   net.Conn
	frames chan frame
}

func (a *agent) rawHello(h frame) (*raw, frame) {
	a.t.Helper()
	r := &raw{t: a.t, conn: a.conn(), frames: make(chan frame, 64)}
	go func() {
		dec := json.NewDecoder(r.conn)
		for {
			var f frame
			if dec.Decode(&f) != nil {
				close(r.frames)
				return
			}
			r.frames <- f
		}
	}()
	h.T = frameHello
	r.send(h)
	return r, r.next()
}

func (a *agent) rawAttach() *raw {
	a.t.Helper()
	r, f := a.rawHello(frame{V: Version, Gen: 1, Token: token})
	if f.T != frameWelcome {
		a.t.Fatalf("got %+v, want welcome", f)
	}
	return r
}

func (r *raw) send(f frame) {
	r.t.Helper()
	if err := encodeFrame(r.conn, f); err != nil {
		r.t.Fatal(err)
	}
}

func (r *raw) next() frame {
	r.t.Helper()
	select {
	case f, ok := <-r.frames:
		if !ok {
			r.t.Fatal("connection closed, wanted a frame")
		}
		return f
	case <-time.After(timeout):
		r.t.Fatal("timed out waiting for a frame")
	}
	return frame{}
}

func TestReplaysOnlyUnackedOutput(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	a.send(note(1))
	a.send(note(2))
	r := a.rawAttach()
	for seq := uint64(1); seq <= 2; seq++ {
		if f := r.next(); f.T != frameRPC || f.Seq != seq {
			t.Fatalf("got %+v, want rpc seq %d", f, seq)
		}
	}
	_ = r.conn.Close()
	a.send(note(3))

	b := a.attach(2)
	for n := 1; n <= 3; n++ {
		b.wantNext(note(n))
	}
	waitAcked(t, a.k, 3)
	_ = b.Close()
	a.send(note(4))

	c := a.attach(3)
	c.wantNext(note(4))
}

func TestNewerGenerationPreempts(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	old := a.attach(1)
	cur := a.attach(2)
	select {
	case <-old.Done():
	case <-time.After(timeout):
		t.Fatal("preempted attacher did not end")
	}
	if !isClosed(t, old.lines) {
		t.Fatal("preempted reader did not end")
	}
	if !errors.Is(old.Err(), ErrPreempted) {
		t.Fatalf("preempted attacher ended with %v, want ErrPreempted", old.Err())
	}
	_, _ = old.Writer().Write([]byte(req("1", "stale") + "\n"))
	cur.write(req("1", "current"))
	a.recvReq("current")
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	a.attach(2)
	for _, gen := range []uint64{2, 1} {
		if re := a.refusal(Hello{Gen: gen, Token: token}); re.Code != RefusedStale {
			t.Fatalf("gen %d: code %q, want %q", gen, re.Code, RefusedStale)
		}
	}
	if re := a.refusal(Hello{Gen: 3, Token: "wrong"}); re.Code != RefusedToken {
		t.Fatalf("code %q, want %q", re.Code, RefusedToken)
	}
	_, f := a.rawHello(frame{V: 2, Gen: 3, Token: token})
	if f.T != frameRefused || f.Code != RefusedVersion {
		t.Fatalf("got %+v, want refused %q", f, RefusedVersion)
	}
	a.attach(3)
}

func TestOverflowRefusesAttach(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 1024)
	pad := strings.Repeat("x", 600)
	for range 3 {
		a.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"pad":%q}}`, pad))
	}
	if re := a.refusal(Hello{Gen: 1, Token: token}); re.Code != RefusedOverflow {
		t.Fatalf("code %q, want %q", re.Code, RefusedOverflow)
	}
}

const promptDone = `{"stopReason":"end_turn"}`

func TestAdoptPromptAnsweredWhileDetached(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.attach(1)
	first.writeTagged(req("1", "session/prompt"), "m1", 3)
	kid := a.recvReq("session/prompt")
	_ = first.Close()
	a.send(resp(kid, promptDone))

	second := a.attach(2)
	st := second.State()
	if want := (&Prompt{Turn: 3}); st.Prompt == nil || *st.Prompt != *want {
		t.Fatalf("State.Prompt = %+v, want %+v", st.Prompt, want)
	}
	second.write(req("7", AdoptMethod))
	got := second.next()
	if h := head(t, got); string(h.ID) != "7" || string(h.Result) != promptDone {
		t.Fatalf("adopted answer %s, want id 7 with %s", got, promptDone)
	}
	a.barrier(second)
}

func TestAdoptPromptAnsweredAfterAdoption(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.attach(1)
	first.write(req("1", "session/prompt"))
	kid := a.recvReq("session/prompt")
	_ = first.Close()

	second := a.attach(2)
	if p := second.State().Prompt; p == nil {
		t.Fatal("State.Prompt = nil, want the prompt in flight")
	}
	second.write(req("7", AdoptMethod))
	a.send(resp(kid, promptDone))
	got := second.next()
	if h := head(t, got); string(h.ID) != "7" || string(h.Result) != promptDone {
		t.Fatalf("adopted answer %s, want id 7 with %s", got, promptDone)
	}
	a.barrier(second)
}

func TestAdoptWithNothingInFlight(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	p := a.attach(1)
	p.write(req("7", AdoptMethod))
	got := p.next()
	if h := head(t, got); string(h.ID) != "7" || len(h.Error) == 0 {
		t.Fatalf("got %s, want an error response to id 7", got)
	}
	a.barrier(p)
}

func TestDroppedPromptReachesAgentOnce(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.attach(1)
	first.write(req("1", "session/prompt"))
	_ = first.Close()
	a.recvReq("session/prompt")

	second := a.attach(2)
	if p := second.State().Prompt; p == nil {
		t.Fatal("State.Prompt = nil, want the prompt in flight")
	}
	a.barrier(second)
}

func TestPendingAgentRequestIsResentOnce(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	ask := req("5", "session/request_permission")
	answer := resp("5", `{"outcome":"allow"}`)
	a.send(ask)

	first := a.rawAttach()
	f := first.next()
	if f.T != frameRPC || string(f.M) != ask {
		t.Fatalf("got %+v, want the request", f)
	}
	first.send(frame{T: frameAck, Seq: f.Seq})

	second := a.attach(2)
	second.wantNext(ask)
	_ = encodeFrame(first.conn, frame{T: frameRPC, M: json.RawMessage(answer)})
	second.write(answer)
	if got := a.recv(); got != answer {
		t.Fatalf("adapter received %s, want %s", got, answer)
	}
	second.write(answer)
	a.barrier(second)
}

func TestSameIDAcrossGenerations(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.attach(1)
	first.write(req("1", "first"))
	kidA := a.recvReq("first")

	second := a.attach(2)
	second.write(req("1", "second"))
	kidB := a.recvReq("second")
	if kidA == kidB {
		t.Fatalf("both requests reached the adapter as id %s", kidA)
	}
	a.send(resp(kidA, `"for-first"`))
	a.send(resp(kidB, `"for-second"`))
	got := second.next()
	if h := head(t, got); string(h.ID) != "1" || string(h.Result) != `"for-second"` {
		t.Fatalf("got %s, want id 1 with for-second", got)
	}
}

func TestDeliveredTagsAreUnackedRequests(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.rawAttach()

	first.send(frame{T: frameRPC, M: json.RawMessage(req("1", "room/send")), Tag: "t1"})
	kid := a.recvReq("room/send")
	a.send(resp(kid, `{}`))
	answered := first.next()
	first.send(frame{T: frameAck, Seq: answered.Seq})

	first.send(frame{T: frameRPC, M: json.RawMessage(req("2", "room/send")), Tag: "t2"})
	kid = a.recvReq("room/send")
	a.send(resp(kid, `{}`))
	first.next()
	_ = first.conn.Close()

	second := a.attach(2)
	if got := second.State().Delivered; len(got) != 1 || got[0] != "t2" {
		t.Fatalf("State.Delivered = %v, want [t2]", got)
	}
}

func TestAdapterExit(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	p := a.attach(1)
	_, _ = io.WriteString(a.k.Stderr(), "boom")
	_ = a.out.Close()
	<-a.pumped
	a.k.Exited(3)

	select {
	case <-p.Done():
	case <-time.After(timeout):
		t.Fatal("attachment did not end")
	}
	if e := p.Exit(); e == nil || e.Code != 3 || e.Stderr != "boom" {
		t.Fatalf("Exit = %+v, want code 3 with stderr boom", e)
	}
	if err := p.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
	if !isClosed(t, p.lines) {
		t.Fatal("reader did not end")
	}
	select {
	case <-a.k.Ended():
	case <-time.After(timeout):
		t.Fatal("keeper did not end after the ack")
	}
	re := a.refusal(Hello{Gen: 2, Token: token})
	if re.Code != RefusedExited || re.Exit == nil || re.Exit.Code != 3 {
		t.Fatalf("refusal %+v, want exited with code 3", re)
	}
}

func TestDetachKeepsAdapter(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.attach(1)
	a.send(note(1))
	first.wantNext(note(1))
	waitAcked(t, a.k, 1)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := first.Detach(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Writer().Write([]byte(req("1", "dropped") + "\n")); !errors.Is(err, ErrDetached) {
		t.Fatalf("write after detach: %v, want ErrDetached", err)
	}
	_ = first.Writer().Close()
	_ = first.Close()
	a.send(note(2))
	select {
	case <-a.stdinClosed:
		t.Fatal("detach closed the adapter's stdin")
	default:
	}

	second := a.attach(2)
	second.wantNext(note(2))
	a.barrier(second)
}

func TestBridge(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go a.k.ServeConn(c)
		}
	}()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Bridge(sock, inR, outW) }()
	if err := encodeFrame(inW, frame{T: frameHello, V: Version, Gen: 1, Token: token}); err != nil {
		t.Fatal(err)
	}
	var f frame
	if err := json.NewDecoder(outR).Decode(&f); err != nil || f.T != frameWelcome {
		t.Fatalf("got %+v, %v, want welcome", f, err)
	}
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(timeout):
		t.Fatal("bridge did not return after its input ended")
	}
}

func TestOversizedClientMessageKeepsTheAttachment(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	r := a.rawAttach()
	if _, err := r.conn.Write(append(make([]byte, maxLine+1), '\n')); err != nil {
		t.Fatal(err)
	}
	r.send(frame{T: frameRPC, M: []byte(req("1", "after"))})
	a.recvReq("after")
}

func TestAttachedServerThatFallsBehindSlowsTheAgent(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 4<<10)
	r := a.rawAttach()
	big := fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"pad":%q}}`, strings.Repeat("x", 1<<10))
	wrote := make(chan struct{})
	go func() {
		for range 8 {
			a.send(big)
		}
		close(wrote)
	}()
	var seq uint64
	for range 2 {
		seq = r.next().Seq
	}
	select {
	case <-wrote:
		t.Fatal("the agent wrote past the retention limit an attached server had not acked")
	case <-time.After(200 * time.Millisecond):
	}
	for {
		r.send(frame{T: frameAck, Seq: seq})
		select {
		case <-wrote:
			if _, welcome := a.rawHello(frame{V: Version, Gen: 2, Token: token}); welcome.T != frameWelcome {
				t.Fatalf("attach after the burst = %+v, want welcome", welcome)
			}
			return
		case f := <-r.frames:
			seq = f.Seq
		case <-time.After(timeout):
			t.Fatal("acks did not let the agent go on")
		}
	}
}

func TestRefusesAGenerationFarAhead(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	if re := a.refusal(Hello{Gen: math.MaxUint64, Token: token}); re.Code != RefusedStale {
		t.Fatalf("code %q, want %q", re.Code, RefusedStale)
	}
	a.attach(maxGenStep)
	if re := a.refusal(Hello{Gen: 2*maxGenStep + 1, Token: token}); re.Code != RefusedStale {
		t.Fatalf("code %q, want %q", re.Code, RefusedStale)
	}
	a.attach(2 * maxGenStep)
}

func TestAnsweredRequestIsNotSentAgain(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	first := a.rawAttach()
	a.send(req("5", "session/request_permission"))
	asked := first.next()
	first.send(frame{T: frameRPC, M: json.RawMessage(resp("5", `{"outcome":"allow"}`))})
	if h := head(t, a.recv()); string(h.ID) != "5" {
		t.Fatalf("the agent got %+v, want the answer to 5", h)
	}
	// Killed before it acked the request it answered.
	_ = first.conn.Close()
	if asked.Seq == 0 {
		t.Fatal("the request had no seq")
	}

	a.send(note(1))
	second := a.attach(2)
	second.wantNext(note(1))
}

func TestStalledAgentStillTakesAcks(t *testing.T) {
	t.Parallel()
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinR.Close() })
	outR, outW := io.Pipe()
	t.Cleanup(func() { _ = outW.Close() })
	k := New(Config{Token: token, AgentIn: stdinW, Retain: 4 << 10, Logger: slog.New(slog.DiscardHandler)})
	go func() { _ = k.Pump(outR) }()
	a := &agent{t: t, k: k}
	p := a.attach(1)
	// The agent never reads this, as an adapter blocked on its own output.
	p.write(req("1", "session/prompt"))
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for n := range 200 {
			if _, err := io.WriteString(outW, note(n)+"\n"); err != nil {
				return
			}
		}
	}()
	for n := range 200 {
		p.wantNext(note(n))
	}
	select {
	case <-wrote:
	case <-time.After(timeout):
		t.Fatal("the agent's output stalled")
	}
}

func TestAcksOnlyWhatWasHandled(t *testing.T) {
	t.Parallel()
	a := newAgent(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := Attach(ctx, a.conn(), Hello{Gen: 1, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	a.send(note(1))
	sc := bufio.NewScanner(c.Reader())
	for _, want := range []string{note(1), fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"seq":1}}`, HandledMethod)} {
		if !sc.Scan() || sc.Text() != want {
			t.Fatalf("read %q, want %q", sc.Text(), want)
		}
	}
	time.Sleep(300 * time.Millisecond)
	a.k.mu.Lock()
	acked := a.k.acked
	a.k.mu.Unlock()
	if acked != 0 {
		t.Fatalf("acked %d before acphost handled it", acked)
	}
	c.Writer().(interface{ Handled(uint64) }).Handled(1)
	waitAcked(t, a.k, 1)
}
