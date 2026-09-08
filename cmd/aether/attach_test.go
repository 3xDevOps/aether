package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// term.GetSize on Windows is GetConsoleScreenBufferInfo, which only accepts a
// console output handle. Querying stdin there always fails, so the probe order
// has to be stdout first with stdin as the redirected-stdout fallback.
func TestTermSizePrefersStdout(t *testing.T) {
	stdout, stdin := int(os.Stdout.Fd()), int(os.Stdin.Fd())
	restore := termSizeOf
	t.Cleanup(func() { termSizeOf = restore })

	probed := []int{}
	termSizeOf = func(fd int) (int, int, error) {
		probed = append(probed, fd)
		switch fd {
		case stdout:
			return 120, 40, nil
		case stdin:
			return 100, 30, nil
		}
		return 0, 0, errors.New("unknown fd")
	}

	cols, rows := termSize()
	if cols != 120 || rows != 40 {
		t.Fatalf("termSize() = %dx%d, want 120x40", cols, rows)
	}
	if len(probed) != 1 || probed[0] != stdout {
		t.Fatalf("probed fds = %v, want stdout (%d) only", probed, stdout)
	}
}

func TestTermSizeFallsBackToStdin(t *testing.T) {
	stdin := int(os.Stdin.Fd())
	restore := termSizeOf
	t.Cleanup(func() { termSizeOf = restore })

	termSizeOf = func(fd int) (int, int, error) {
		if fd == stdin {
			return 100, 30, nil
		}
		return 0, 0, errors.New("not a console")
	}

	if cols, rows := termSize(); cols != 100 || rows != 30 {
		t.Fatalf("termSize() = %dx%d, want 100x30", cols, rows)
	}
}

// Under `go test` neither standard handle is a console, so the real call must
// still yield the 80x24 default instead of a zero size or a panic.
func TestTermSizeDefaultsWhenNoConsole(t *testing.T) {
	if cols, rows := termSize(); cols != 80 || rows != 24 {
		t.Fatalf("termSize() = %dx%d, want 80x24", cols, rows)
	}
}
func TestAttachShellFlagUsage(t *testing.T) {
	err := runAttach([]string{"--shell"})
	if err == nil || err.Error() != "usage: aether attach [--read-only] [--shell <tab>] <run>" {
		t.Fatalf("runAttach missing shell value error = %v", err)
	}
}

func TestEnableVirtualTerminalOnNonConsole(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	for name, f := range map[string]*os.File{
		"pipe": w,
		"file": mustTempFile(t),
	} {
		t.Run(name, func(t *testing.T) {
			restore := enableVirtualTerminal(f)
			if restore == nil {
				t.Fatal("enableVirtualTerminal returned a nil restore func")
			}
			restore()
		})
	}
}

func mustTempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "console")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// A terminal answers the queries in the scrollback it has just been handed
// - device attributes, colour reads, XTVERSION, the kitty probe - and those
// answers travel to the server on the same channel as typing, where they
// are recorded as steering the run. They are dropped while the replay is
// still going out; what the person types once it has landed is not.
func TestReplayGateDropsWhatArrivesBeforeTheReplayLands(t *testing.T) {
	const replay = "\x1b[c\x1b]11;?\x07"
	const answers = "\x1b[?1;2c\x1b]11;rgb:0/0/0\x07"
	gate := &replayGate{remaining: len(replay)}
	writer := &countingWriter{w: io.Discard, gate: gate}

	// The terminal answers first, while the replay is still going out, and
	// the replay lands before the person types. Driving both from the one
	// reader orders them against the gate instead of racing it.
	step := 0
	source := readerFunc(func(p []byte) (int, error) {
		step++
		switch step {
		case 1:
			return copy(p, answers), nil
		case 2:
			if _, err := writer.Write([]byte(replay)); err != nil {
				return 0, err
			}
			return copy(p, "ls\r"), nil
		}
		return 0, io.EOF
	})

	forwarded, err := io.ReadAll(&gatedReader{r: source, gate: gate})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(forwarded) != "ls\r" {
		t.Errorf("forwarded %q, want only what was typed after the replay", forwarded)
	}
	if !gate.open() {
		t.Error("gate still closed after the whole replay was written")
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// The gate opens on the byte count the server announced, not on a
// separator: live output arriving in the same write as the replay's tail
// must not hold it shut.
func TestReplayGateOpensOnceTheCountIsMet(t *testing.T) {
	gate := &replayGate{remaining: 8}
	if gate.open() {
		t.Fatal("gate open before anything was written")
	}
	gate.wrote(4)
	if gate.open() {
		t.Fatal("gate open half way through the replay")
	}
	gate.wrote(12)
	if !gate.open() {
		t.Fatal("gate closed after the replay and live output were written")
	}
}

// syncBuffer collects what the input copy forwards. That copy outlives
// copyRawStreams, which returns as soon as the output side is done, so the
// buffer has to be safe to read while it is still being written.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// With no replay to mute, every byte goes straight through.
func TestCopyRawStreamsWithoutReplayForwardsEverything(t *testing.T) {
	sent := &syncBuffer{}
	stream := &rawTestStream{Reader: strings.NewReader("hi\n"), Writer: sent}
	var output bytes.Buffer
	if err := copyRawStreams(stream, strings.NewReader("\x1b[?1;2c"), &output, 0); err != nil {
		t.Fatalf("copyRawStreams: %v", err)
	}
	// The input copy outlives the call, so wait for what it forwards.
	waitFor(t, func() bool { return sent.String() == "\x1b[?1;2c" })
	if got := sent.String(); got != "\x1b[?1;2c" {
		t.Errorf("stream received %q, want the bytes forwarded unchanged", got)
	}
	if got := output.String(); got != "hi\n" {
		t.Errorf("output = %q, want the stream's output", got)
	}
}

// waitFor polls cond until it holds or the test's patience runs out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the input copy to forward")
		}
		time.Sleep(time.Millisecond)
	}
}

// The gate is wired into copyRawStreams by the replay count openAttach
// reads off the server's ack. Turning either off - the count or the
// install - puts an attach back to forwarding what the terminal answers
// the replay with, so the wiring is driven here rather than the pieces
// under it.
func TestCopyRawStreamsMutesTheAnnouncedReplay(t *testing.T) {
	const replay = "\x1b[c\x1b]11;?\x07"
	const answers = "\x1b[?1;2c\x1b]11;rgb:0/0/0\x07"

	// Each side takes its turn from the other: the terminal answers before
	// the replay is written out, and the person types after it. Nothing
	// here races the gate.
	answered := make(chan struct{})
	replayed := make(chan struct{})
	typed := make(chan struct{})

	reads := 0
	stream := &rawTestStream{
		Reader: readerFunc(func(p []byte) (int, error) {
			reads++
			if reads == 1 {
				<-answered
				return copy(p, replay), nil
			}
			<-typed
			return 0, io.EOF
		}),
		Writer: &syncBuffer{},
	}
	writes := 0
	input := readerFunc(func(p []byte) (int, error) {
		writes++
		switch writes {
		case 1:
			defer close(answered)
			return copy(p, answers), nil
		case 2:
			<-replayed
			return copy(p, "ls\r"), nil
		}
		close(typed)
		return 0, io.EOF
	})

	var output syncBuffer
	watcher := &gateWatcher{w: &output, at: len(replay), done: replayed, once: &sync.Once{}}
	err := copyRawStreams(stream, input, watcher, len(replay))
	if err != nil {
		t.Fatalf("copyRawStreams: %v", err)
	}
	sent := stream.Writer.(*syncBuffer)
	waitFor(t, func() bool { return sent.String() == "ls\r" })
	if got := sent.String(); got != "ls\r" {
		t.Errorf("stream received %q, want only what was typed after the replay", got)
	}
	if got := output.String(); got != replay {
		t.Errorf("output = %q, want the replay", got)
	}
}

// gateWatcher closes done once at bytes have been written, which is where
// the gate opens.
type gateWatcher struct {
	w    io.Writer
	at   int
	done chan struct{}

	written int
	once    *sync.Once
}

func (g *gateWatcher) Write(p []byte) (int, error) {
	n, err := g.w.Write(p)
	g.written += n
	if g.written >= g.at {
		g.once.Do(func() { close(g.done) })
	}
	return n, err
}
