package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestWaitExitsWithTheRunsOutcome(t *testing.T) {
	timedOut := make(chan time.Time)
	close(timedOut)
	for _, tc := range []struct {
		name     string
		run      protocol.Run
		items    []protocol.AttentionItem
		deadline <-chan time.Time
		code     int
		lines    []string
	}{
		{name: "completed", run: protocol.Run{Status: "completed"}, code: waitFinished, lines: []string{"run r1 completed\n"}},
		{name: "reported success", run: protocol.Run{Status: "needs-attention", Activity: "finished", Review: "ready"},
			items: []protocol.AttentionItem{{Kind: protocol.AttentionKindReady, RunID: "r1"}}, code: waitFinished},
		{name: "failed", run: protocol.Run{Status: "failed", Reason: "agent exited 1: boom"}, code: waitFailed,
			lines: []string{"run r1 failed: agent exited 1: boom\n"}},
		{name: "permission request", run: protocol.Run{Status: "needs-attention", Activity: "needs-you"},
			items: []protocol.AttentionItem{permissionItem()}, code: waitNeedsYou,
			lines: []string{"r1 permission req-1: Run rm -rf build\n", "  aether answer r1 req-1 allow-1    # Allow once\n"}},
		{name: "turn ended without a report", run: protocol.Run{Status: "needs-attention", Activity: "waiting", Reason: "turn ended without a report"},
			code: waitNeedsYou, lines: []string{"run r1 needs-attention: turn ended without a report\n", "  aether message r1 \"<reply>\"\n"}},
		{name: "paused", run: protocol.Run{Status: "needs-attention", Activity: "needs-you", Paused: true}, deadline: timedOut, code: waitTimedOut},
		{name: "working", run: protocol.Run{Status: "running", Activity: "working"}, deadline: timedOut, code: waitTimedOut,
			lines: []string{"run r1 running\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.run.ID, tc.run.WorkspaceID = "r1", "ws1"
			c := fakeControlMethods(t, map[string]*fakeReply{
				protocol.MethodRunGet:            {result: protocol.RunResult{Run: tc.run}},
				protocol.MethodAttentionList:     {result: protocol.AttentionListResult{Items: tc.items}},
				protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
				protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
			})
			var out bytes.Buffer
			err := waitRun(c, &out, "r1", false, make(chan struct{}), time.Hour, tc.deadline, time.Minute)
			code := 0
			var status *exitStatusError
			if errors.As(err, &status) {
				code = status.code
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("wait exit = %d (%v), want %d; output %q", code, err, tc.code, out.String())
			}
			for _, line := range tc.lines {
				if !strings.Contains(out.String(), line) {
					t.Errorf("wait printed %q, want the line %q", out.String(), line)
				}
			}
			if strings.Contains(out.String(), "always-1") {
				t.Errorf("wait offers an always option: %q", out.String())
			}
		})
	}
}

// A status event wakes the wait instead of its recheck interval.
func TestWaitWakesOnAStatusEvent(t *testing.T) {
	run := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "running", Activity: "working"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:            run,
		protocol.MethodAttentionList:     {result: protocol.AttentionListResult{}},
		protocol.MethodWorkspaceList:     {result: protocol.WorkspaceListResult{}},
		protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{}},
	})
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- waitRun(c, &bytes.Buffer{}, "r1", false, changed, time.Hour, nil, 0) }()
	for run.params() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	run.answer(protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "completed"}})
	changed <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait after the run completed = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a status event did not wake the wait")
	}
}

// Once --timeout has cut the connection, the error a stalled call returns
// becomes a timeout; an outcome wait already reached keeps its own code.
func TestWaitCutConnectionTimesOut(t *testing.T) {
	stalled := errors.New("EOF")
	var status *exitStatusError
	if err := timedOutAfterCut(stalled, true, time.Minute, "r1"); !errors.As(err, &status) || status.code != waitTimedOut {
		t.Fatalf("cut connection = %v, want exit %d", err, waitTimedOut)
	}
	if err := timedOutAfterCut(stalled, false, time.Minute, "r1"); err != stalled {
		t.Fatalf("uncut connection = %v, want the call's error", err)
	}
	needsYou := &exitStatusError{code: waitNeedsYou, err: errors.New("run r1 needs you")}
	if err := timedOutAfterCut(needsYou, true, time.Minute, "r1"); err != needsYou {
		t.Fatalf("outcome after cut = %v, want it unchanged", err)
	}
}
