package adapter

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// pipeAtt is a minimal pipe-based runtime.Attachment for driving a real
// ptyhost session from a test.
type pipeAtt struct {
	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter
}

func newPipeAtt() *pipeAtt {
	a := &pipeAtt{}
	a.inR, a.inW = io.Pipe()
	a.outR, a.outW = io.Pipe()
	return a
}

func (a *pipeAtt) Stdin() io.WriteCloser                    { return a.inW }
func (a *pipeAtt) Stdout() io.Reader                        { return a.outR }
func (a *pipeAtt) Stderr() io.Reader                        { return strings.NewReader("") }
func (a *pipeAtt) Resize(context.Context, uint, uint) error { return nil }
func (a *pipeAtt) Close() error                             { _ = a.outR.Close(); return nil }

// TestManagerWithRealPTYTap runs the full path with the real ptyhost tap:
// fixture bytes written as agent PTY output flow through
// ptyhost.Host.TapOutput -> Manager -> normalizer -> claude adapter -> bus.
func TestManagerWithRealPTYTap(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_tty.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	const (
		workspace = domain.WorkspaceID("ws_1")
		run       = domain.RunID("run_1")
	)
	host, err := ptyhost.New(ptyhost.Config{TranscriptDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ptyhost.New: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	att := newPipeAtt()
	if startErr := host.StartSession(t.Context(), run, att); startErr != nil {
		t.Fatalf("StartSession: %v", startErr)
	}

	bus := newTestBus(t)
	runs := &fakeRuns{runs: map[domain.RunID]*domain.Run{
		run: {ID: run, WorkspaceID: workspace, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunRunning},
	}}
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeAgentEvent, events.TypeRunCost}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	startManager(t, bus, runs, host)
	publishRunning(t, bus, run)

	// Emit the fixture as agent output in small writes. Even if the
	// manager taps late, the scrollback replay covers what it missed;
	// stdout stays open until the events from terminated lines arrived,
	// so the session cannot end before the tap attaches.
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for len(data) > 0 {
			n := 512
			if n > len(data) {
				n = len(data)
			}
			if _, err := att.outW.Write(data[:n]); err != nil {
				return
			}
			data = data[n:]
		}
	}()

	want := fixturePayloads()
	got := collect(t, sub, len(want)-1, 10*time.Second)

	// All bytes are delivered (io.Pipe writes are synchronous), so ending
	// the agent now (stdout EOF) loses nothing: the fixture's
	// unterminated final result line must still arrive via the
	// normalizer flush.
	<-wrote
	_ = att.outW.Close()
	got = append(got, collect(t, sub, 1, 10*time.Second)...)

	var payloads []events.Payload
	for _, e := range got {
		payloads = append(payloads, e.Payload)
		if e.WorkspaceID != workspace || e.RunID != run {
			t.Errorf("event scoped to %s/%s, want %s/%s", e.WorkspaceID, e.RunID, workspace, run)
		}
	}
	assertPayloads(t, payloads, want)
}
