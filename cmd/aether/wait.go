package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "wait",
		short: "wait until a run finishes or needs you (exit 0 finished, 1 failed, 3 needs you, 124 timed out)",
		run:   runWait,
	})
}

const (
	waitFinished = 0
	waitFailed   = 1
	waitNeedsYou = 3
	waitTimedOut = 124
)

// waitPoll is how often aether wait reads the run when it has no event
// stream; with one, a status event wakes it and waitRecheck catches what
// arrives without one, such as a teammate's question.
const (
	waitPoll    = 5 * time.Second
	waitRecheck = 30 * time.Second
)

func runWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	timeout := fs.Duration("timeout", 0, "give up after this long and exit 124, e.g. 30m (default: no limit)")
	asJSON := fs.Bool("json", false, "print the run as aether runs <run-id> --json does")
	runID, err := parseLeadingArg(fs, args)
	if err != nil || runID == "" {
		return fmt.Errorf("usage: aether wait <run-id> [--timeout <duration>] [--json]")
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
	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	changed, every := make(chan struct{}, 1), waitPoll
	stream, err := conn.EventsStream(protocol.SubscribeRequest{RunID: runID, Types: []string{string(events.TypeRunStatus)}})
	if err == nil {
		defer func() { _ = stream.Close() }()
		go signalLines(stream, changed)
		every = waitRecheck
	}
	var deadline <-chan time.Time
	if *timeout > 0 {
		timer := time.NewTimer(*timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	return waitRun(c, os.Stdout, runID, *asJSON, changed, every, deadline, *timeout)
}

func signalLines(stream io.Reader, changed chan<- struct{}) {
	defer close(changed)
	r := bufio.NewReaderSize(stream, 64<<10)
	for {
		if _, err := protocol.ReadLine(r); err != nil {
			return
		}
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}

func waitRun(c *protocol.Client, w io.Writer, runID string, asJSON bool, changed <-chan struct{}, every time.Duration, deadline <-chan time.Time, timeout time.Duration) error {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		r, err := getRun(c, runID)
		if err != nil {
			return err
		}
		items, err := attentionItems(c, r.WorkspaceID)
		if err != nil {
			return err
		}
		code, done := waitOutcome(r, items)
		timedOut := false
		if !done {
			select {
			case _, open := <-changed:
				if !open {
					changed = nil
					tick.Reset(waitPoll)
				}
				continue
			case <-tick.C:
				continue
			case <-deadline:
				code, timedOut = waitTimedOut, true
			}
		}
		v, err := runViewOf(c, r, items)
		if err != nil {
			return err
		}
		if err := printWaited(w, v, r, items, code, asJSON); err != nil {
			return err
		}
		switch {
		case timedOut:
			return &exitStatusError{code: code, err: fmt.Errorf("timed out after %s waiting for run %s", timeout, r.ID)}
		case code == waitFailed:
			return &exitStatusError{code: code, err: fmt.Errorf("run %s failed", r.ID)}
		case code == waitNeedsYou:
			return &exitStatusError{code: code, err: fmt.Errorf("run %s needs you", r.ID)}
		}
		return nil
	}
}

// A paused run waits for its resume. A live run whose turn ended with no
// report waits for a message, so it needs you too.
func waitOutcome(r protocol.Run, items []protocol.AttentionItem) (int, bool) {
	status := domain.RunStatus(r.Status)
	switch {
	case status == domain.RunFailed || status == domain.RunInterrupted:
		return waitFailed, true
	case status.Terminal():
		return waitFinished, true
	case !status.Live() || r.Paused:
		return 0, false
	}
	for _, it := range items {
		if it.RunID == r.ID && it.Kind != protocol.AttentionKindReady && it.Kind != protocol.AttentionKindFailed {
			return waitNeedsYou, true
		}
	}
	switch domain.RunActivity(r.Activity) {
	case domain.ActivityFinished:
		return waitFinished, true
	case domain.ActivityFailed:
		return waitFailed, true
	case domain.ActivityNeedsYou, domain.ActivityDisconnected, domain.ActivityWaiting, domain.ActivityIdle:
		return waitNeedsYou, true
	}
	return 0, false
}

func printWaited(w io.Writer, v runView, r protocol.Run, items []protocol.AttentionItem, code int, asJSON bool) error {
	if asJSON {
		return writeJSON(w, v)
	}
	if _, err := fmt.Fprintln(w, statusLine(v)); err != nil {
		return err
	}
	if code != waitNeedsYou {
		return nil
	}
	for _, it := range items {
		if it.RunID == r.ID {
			return printNeeds(w, r.ID, items)
		}
	}
	next := fmt.Sprintf("  it waits on another member; aether runs %s shows its state", r.ID)
	if a := domain.RunActivity(r.Activity); a == domain.ActivityWaiting || a == domain.ActivityIdle {
		next = fmt.Sprintf("  aether message %s \"<reply>\"", r.ID)
	}
	_, err := fmt.Fprintln(w, next)
	return err
}
