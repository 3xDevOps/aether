//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// history reads every item the run's log holds, oldest first.
func (e *soakEnv) history(runID string) ([]acphost.Item, error) {
	var all []acphost.Item
	before := int64(0)
	for {
		var page protocol.RunACPHistoryResult
		if err := e.call(protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{
			RunID: runID, BeforeSeq: before, Limit: protocol.ACPHistoryMaxLimit,
		}, &page); err != nil {
			return nil, err
		}
		var items []acphost.Item
		for _, f := range page.Frames {
			var it acphost.Item
			if json.Unmarshal(f.Item, &it) == nil {
				items = append(items, it)
			}
		}
		all = append(items, all...)
		if len(page.Frames) < protocol.ACPHistoryMaxLimit || page.OldestSeq <= 1 {
			return all, nil
		}
		before = page.OldestSeq
	}
}

func dockerOut(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type soakContainer struct {
	id, started string
}

func containerIdentity(t *testing.T, runID string) soakContainer {
	t.Helper()
	out, err := dockerOut("inspect", "--format", "{{.Id}} {{.State.StartedAt}}", containerName(runID))
	if err != nil {
		t.Fatalf("inspect %s: %v (%s)", containerName(runID), err, out)
	}
	id, started, _ := strings.Cut(out, " ")
	return soakContainer{id: id, started: started}
}

func containerFile(runID, path string) (string, error) {
	return dockerOut("exec", containerName(runID), "cat", path)
}

// soakEvent mirrors the soakagent's record.
type soakEvent struct {
	T    int64  `json:"t"`
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
	Dur  int64  `json:"dur_ms"`
	ID   string `json:"id"`
	Err  string `json:"err"`
}

func (e soakEvent) done() time.Time { return time.UnixMilli(e.T + e.Dur) }

func readEvents(runID, name string) []soakEvent {
	raw, err := containerFile(runID, "/workspace/soak/"+name+".jsonl")
	if err != nil {
		return nil
	}
	var out []soakEvent
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var ev soakEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

// gapAround is the time between the last success that completed before the
// outage began and the first that completed after it began.
func gapAround(events []soakEvent, kind string, down time.Time) (time.Duration, bool) {
	var before, after time.Time
	for _, ev := range events {
		if ev.Kind != kind || !ev.OK {
			continue
		}
		d := ev.done()
		if d.Before(down) {
			if d.After(before) {
				before = d
			}
		} else if after.IsZero() || d.Before(after) {
			after = d
		}
	}
	if before.IsZero() || after.IsZero() {
		return 0, false
	}
	return after.Sub(before), true
}

// watcher samples a run's status and streamed ticks through the restarts.
type soakWatcher struct {
	mu sync.Mutex
	// arrivals is when the server's stream first showed each numbered line.
	arrivals map[int]time.Time
	lastTick int
	items    []string
	statuses []string
	ticks    []tickObs
}

type tickObs struct {
	at time.Time
	n  int
}

func (w *soakWatcher) run(ctx context.Context, e *soakEnv, runID string) {
	for ctx.Err() == nil {
		var got protocol.RunResult
		if err := e.call(protocol.MethodRunGet, protocol.RunIDParams{RunID: runID}, &got); err == nil {
			w.mu.Lock()
			w.statuses = append(w.statuses, got.Run.Status+"/"+got.Run.Reason)
			w.mu.Unlock()
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// acpStream is an open aether-acp stream: the ack, then one ACPFrame a line.
type acpStream struct {
	ack    protocol.ACPStreamResponse
	lines  *bufio.Reader
	closer func()
}

func (s *acpStream) close() { s.closer() }

func (e *soakEnv) openACP(req protocol.ACPStreamRequest) (*acpStream, error) {
	client, err := e.dial()
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	closer := func() { _ = sess.Close(); _ = client.Close() }
	stdin, err := sess.StdinPipe()
	if err != nil {
		closer()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		closer()
		return nil, err
	}
	if err = sess.RequestSubsystem(protocol.SubsystemACP); err != nil {
		closer()
		return nil, err
	}
	header, _ := json.Marshal(req)
	if _, err = stdin.Write(append(header, '\n')); err != nil {
		closer()
		return nil, err
	}
	s := &acpStream{lines: bufio.NewReaderSize(stdout, 1<<20), closer: closer}
	line, err := s.lines.ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &s.ack)
	}
	if err == nil && !s.ack.OK {
		err = fmt.Errorf("aether-acp refused: %d %s", s.ack.Code, s.ack.Error)
	}
	if err != nil {
		closer()
		return nil, err
	}
	return s, nil
}

// ticks follows the run's stream through every restart, reconnecting from the
// last item it saw, and records when each streamed line first arrived.
func (w *soakWatcher) follow(ctx context.Context, e *soakEnv, runID string) {
	var after int64
	for ctx.Err() == nil {
		s, err := e.openACP(protocol.ACPStreamRequest{RunID: runID, AfterSeq: after})
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go func() { <-ctx.Done(); s.close() }()
		for {
			line, err := s.lines.ReadBytes('\n')
			if err != nil {
				break
			}
			var f protocol.ACPFrame
			var it acphost.Item
			if json.Unmarshal(line, &f) != nil || json.Unmarshal(f.Item, &it) != nil {
				continue
			}
			after = max(after, it.Seq)
			if it.Message == nil || it.Message.Role != "assistant" {
				continue
			}
			n := 0
			for _, l := range strings.Split(it.Message.Text, "\n") {
				if v, ok := strings.CutPrefix(l, "tick "); ok {
					if c, convErr := strconv.Atoi(v); convErr == nil && c > n {
						n = c
					}
				}
			}
			w.mu.Lock()
			if w.arrivals == nil {
				w.arrivals = map[int]time.Time{}
			}
			for k := w.lastTick + 1; k <= n; k++ {
				w.arrivals[k] = time.Now()
			}
			w.lastTick = max(w.lastTick, n)
			if n > 0 && (len(w.ticks) == 0 || w.ticks[len(w.ticks)-1].n < n) {
				w.ticks = append(w.ticks, tickObs{time.Now(), n})
			}
			w.mu.Unlock()
		}
		s.close()
	}
}

func countKind(events []soakEvent, kind string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func soakTurnEnded(e *soakEnv, runID string) bool {
	items, _ := e.history(runID)
	for _, it := range items {
		if it.Kind == acphost.KindTurnEnd {
			return true
		}
	}
	return false
}

func waitUntil(d time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool, dump ...func() string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, f := range dump {
		t.Log(f())
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func pendingRequest(e *soakEnv, runID string) string {
	items, err := e.history(runID)
	if err != nil {
		return ""
	}
	for _, it := range items {
		if it.Kind == acphost.KindRequest && it.Request != nil && it.Request.Status == acphost.RequestPending {
			return it.Request.ID
		}
	}
	return ""
}
