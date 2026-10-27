package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "wait",
		short:   "wait until a run finishes, needs you or ends its turn (exit 0 finished, 1 failed, 3 needs you, 4 parked until a message, 5 its wait broke, 124 timed out)",
		usage:   "aether wait <run-id> [--timeout <duration>] [--json]",
		example: "aether wait run-d6ay38tj8j --timeout 30m --json",
		flags:   true,
		run:     runWait,
	})
}

const (
	waitFinished = 0
	waitFailed   = 1
	waitNeedsYou = 3
	waitWaiting  = 4
	waitBroken   = 5
	waitTimedOut = 124
)

// waitPoll is how often aether wait reads the run when it has no event
// stream; with one, a status event wakes it and waitRecheck catches what
// arrives without one, such as a teammate's question.
const (
	waitPoll    = 5 * time.Second
	waitRecheck = 30 * time.Second
)

// waitOutputGrace is how long past --timeout the last calls, which build
// the output, may take before the connection is cut.
const waitOutputGrace = 15 * time.Second

func runWait(args []string) error {
	fs := newFlagSet("wait")
	timeout := fs.Duration("timeout", 0, "give up after this long and exit 124, e.g. 30m (default: no limit)")
	asJSON := fs.Bool("json", false, "print the run as aether runs <run-id> --json does")
	runID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: aether wait <run-id> [--timeout <duration>] [--json]", err)
	}
	if runID == "" {
		return fmt.Errorf("usage: aether wait <run-id> [--timeout <duration>] [--json]")
	}
	var deadline <-chan time.Time
	end := time.Now().Add(*timeout)
	if *timeout > 0 {
		timer := time.NewTimer(*timeout)
		defer timer.Stop()
		deadline = timer.C
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
	var cutOff atomic.Bool
	if *timeout > 0 {
		// A call has no deadline of its own: a server that stops answering
		// is cut off by closing the connection.
		cut := time.AfterFunc(time.Until(end)+waitOutputGrace, func() {
			cutOff.Store(true)
			_ = conn.Close()
		})
		defer cut.Stop()
	}
	c, err := conn.Control()
	if err != nil {
		return timedOutAfterCut(err, cutOff.Load(), *timeout, runID)
	}
	defer func() { _ = c.Close() }()
	changed, every := make(chan struct{}, 1), waitPoll
	stream, err := conn.EventsStream(protocol.SubscribeRequest{RunID: runID, Types: []string{string(events.TypeRunStatus)}})
	if err == nil {
		defer func() { _ = stream.Close() }()
		go signalLines(stream, changed)
		every = waitRecheck
	}
	err = waitRun(c, os.Stdout, os.Stderr, runID, *asJSON, changed, every, deadline, *timeout)
	return timedOutAfterCut(err, cutOff.Load(), *timeout, runID)
}

func timedOutAfterCut(err error, cut bool, timeout time.Duration, runID string) error {
	var status *exitStatusError
	if err == nil || !cut || errors.As(err, &status) {
		return err
	}
	return &exitStatusError{code: waitTimedOut, err: fmt.Errorf("timed out after %s waiting for run %s: the server stopped answering: %w", timeout, runID, err)}
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

func waitRun(c *protocol.Client, w, stderr io.Writer, runID string, asJSON bool, changed <-chan struct{}, every time.Duration, deadline <-chan time.Time, timeout time.Duration) error {
	tick := time.NewTicker(every)
	defer tick.Stop()
	var doubt string
	for {
		r, err := getRun(c, runID)
		if err != nil {
			return err
		}
		// A wait Aether cannot vouch for keeps waiting, said once on stderr.
		// The source's error text can change on every read, so it does not
		// make the doubt new.
		if r.Wait != nil && (r.Wait.Kind == domain.WaitSourceUnknown || r.Wait.Kind == domain.WaitFault) {
			if key := string(r.Wait.Kind) + "|" + r.Wait.Detail; key != doubt {
				doubt = key
				_, _ = fmt.Fprintf(stderr, "run %s still waits, but %s\n", r.ID, printable(r.Wait.Text(time.Now())))
			}
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
		case code == waitWaiting:
			return &exitStatusError{code: code, err: fmt.Errorf("run %s is parked and waits for a message", r.ID)}
		case code == waitBroken:
			return &exitStatusError{code: code, err: fmt.Errorf("run %s will not wake on its own: %s", r.ID, printable(r.Reason))}
		}
		return nil
	}
}

// A paused run waits for its resume. A run needs you when the Needs you list
// holds one of its requests for you, or by the rule aether runs --needs-you
// shares; one parked with nothing pending waits for a message, which is not a
// request for a person. Waiting on agents, CI or a process, or a swarm
// worker's request its integrator answers, keeps the wait going. A wait
// whose condition resolved but whose wake never reached the agent, or whose
// source went unread for an hour, is broken.
func waitOutcome(r protocol.Run, items []protocol.AttentionItem) (int, bool) {
	status := domain.RunStatus(r.Status)
	switch {
	case status == domain.RunFailed || status == domain.RunInterrupted:
		return waitFailed, true
	case status.Terminal():
		return waitFinished, true
	case !status.Live() || r.Paused:
		return 0, false
	case r.Activity == string(domain.ActivityNeedsYou) && r.Cause == string(events.CauseWaitFault):
		return waitBroken, true
	}
	for _, it := range items {
		if it.RunID == r.ID && it.Kind != protocol.AttentionKindReady && it.Kind != protocol.AttentionKindFailed {
			return waitNeedsYou, true
		}
	}
	switch activity := domain.RunActivity(r.Activity); {
	case activity == domain.ActivityFinished:
		return waitFinished, true
	case activity == domain.ActivityFailed:
		return waitFailed, true
	case needsYou(r):
		return waitNeedsYou, true
	case activity == domain.ActivityWaiting, activity == domain.ActivityIdle, activity == domain.ActivityAnswered:
		return waitWaiting, true
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
	switch code {
	case waitWaiting, waitBroken:
		_, err := fmt.Fprintf(w, "  aether message %s \"<reply>\"\n", r.ID)
		return err
	case waitNeedsYou:
		for _, it := range items {
			if it.RunID == r.ID {
				return printNeeds(w, r.ID, items)
			}
		}
		_, err := fmt.Fprintf(w, "  it waits on another member; aether runs %s shows its state\n", r.ID)
		return err
	}
	return nil
}
