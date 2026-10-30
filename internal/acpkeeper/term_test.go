package acpkeeper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// pty stands in for a PTY master: it records input and resizes.
type pty struct {
	mu    sync.Mutex
	in    bytes.Buffer
	sizes [][2]uint16
}

func (p *pty) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.in.Write(b)
}

func (p *pty) resize(cols, rows uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sizes = append(p.sizes, [2]uint16{cols, rows})
	return nil
}

func (p *pty) eventually(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		done := ok()
		p.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the terminal never saw it")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTerm(t *testing.T, retain int) (*Term, *pty) {
	t.Helper()
	p := &pty{}
	return NewTerm(TermConfig{Token: token, Input: p, Resize: p.resize, Cols: 80, Rows: 24, Retain: retain}), p
}

func attachTerm(t *testing.T, k *Term, gen, from uint64) (*TermClient, error) {
	t.Helper()
	c, s := net.Pipe()
	go k.ServeConn(s)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := AttachTerm(ctx, c, Hello{Gen: gen, Token: token}, from)
	if err == nil {
		t.Cleanup(func() { _ = client.Close() })
	}
	return client, err
}

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		b := make([]byte, n)
		k, _ := io.ReadFull(r, b)
		got <- string(b[:k])
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(timeout):
		t.Fatalf("timed out reading %d bytes", n)
	}
	return ""
}

func TestTermReattachReplaysFromOffset(t *testing.T) {
	t.Parallel()
	k, p := newTerm(t, 0)
	_, _ = k.Write([]byte("$ make\r\n"))
	first, err := attachTerm(t, k, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st := first.State(); st.Start != 0 || st.End != 8 || st.Cols != 80 || st.Rows != 24 {
		t.Fatalf("welcome state %+v", st)
	}
	_, _ = k.Write([]byte("building\r\n"))
	if got := readN(t, first.Stdout(), 18); got != "$ make\r\nbuilding\r\n" {
		t.Fatalf("first attacher read %q", got)
	}
	// The server goes away; the program keeps writing.
	_ = first.Close()
	_, _ = k.Write([]byte("done\r\n"))

	second, err := attachTerm(t, k, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	if st := second.State(); st.Start != 0 || st.End != 24 || second.From() != 8 {
		t.Fatalf("welcome state %+v from %d", st, second.From())
	}
	// Reads stop at the welcome's End, so a caller can tell replay from new
	// output.
	if got := readN(t, second.Stdout(), 16); got != "building\r\ndone\r\n" {
		t.Fatalf("replay %q", got)
	}
	if _, err := second.Stdin().Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if err := second.Resize(context.Background(), 120, 40); err != nil {
		t.Fatal(err)
	}
	p.eventually(t, func() bool { return p.in.String() == "ls\r" && len(p.sizes) == 1 && p.sizes[0] == [2]uint16{120, 40} })
	_, _ = k.Write([]byte("a.txt\r\n"))
	if got := readN(t, second.Stdout(), 7); got != "a.txt\r\n" {
		t.Fatalf("live output %q", got)
	}

	k.Exited(3)
	if _, err := second.Stdout().Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after exit: %v, want EOF", err)
	}
	if exit := second.Exit(); exit == nil || exit.Code != 3 {
		t.Fatalf("exit %+v, want code 3", exit)
	}
	select {
	case <-k.Ended():
	case <-time.After(timeout):
		t.Fatal("the keeper did not end after handing over the exit")
	}
}

func TestTermReplayStartsAtTheRetainedWindow(t *testing.T) {
	t.Parallel()
	k, _ := newTerm(t, 8)
	_, _ = k.Write([]byte("0123456789abcdef"))
	c, err := attachTerm(t, k, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if st.End != 16 || st.Start == 0 || st.Start > 8 || c.From() != st.Start {
		t.Fatalf("state %+v from %d; want the window to have dropped the oldest bytes", st, c.From())
	}
	want := "0123456789abcdef"[st.Start:]
	if got := readN(t, c.Stdout(), len(want)); got != want {
		t.Fatalf("replay %q, want %q", got, want)
	}
}

func TestTermNewerGenerationPreempts(t *testing.T) {
	t.Parallel()
	k, p := newTerm(t, 0)
	old, err := attachTerm(t, k, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachTerm(t, k, 1, 0); !isRefused(err, RefusedStale) {
		t.Fatalf("same generation: %v, want stale", err)
	}
	if _, err := attachTerm(t, k, 2, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.Done():
	case <-time.After(timeout):
		t.Fatal("the older attacher was not dropped")
	}
	if !errors.Is(old.Err(), ErrPreempted) {
		t.Fatalf("older attacher ended with %v, want ErrPreempted", old.Err())
	}
	_, _ = old.Stdin().Write([]byte("stale"))
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.in.Len() != 0 {
		t.Fatalf("a preempted attacher's input reached the terminal: %q", p.in.String())
	}
}

func TestTermRefusesAWrongTokenAndAnACPKeeper(t *testing.T) {
	t.Parallel()
	k, _ := newTerm(t, 0)
	c, s := net.Pipe()
	go k.ServeConn(s)
	if _, err := AttachTerm(context.Background(), c, Hello{Gen: 1, Token: "nope"}, 0); !isRefused(err, RefusedToken) {
		t.Fatalf("wrong token: %v", err)
	}
	a := newAgent(t, 0)
	if _, err := AttachTerm(context.Background(), a.conn(), Hello{Gen: 1, Token: token}, 0); err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("attach to an ACP keeper: %v", err)
	}
}

func isRefused(err error, code string) bool {
	var refused *RefusedError
	return errors.As(err, &refused) && refused.Code == code
}
