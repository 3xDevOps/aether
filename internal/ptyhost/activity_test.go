package ptyhost

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestClassifyTitleNativeAndNamedStatuses(t *testing.T) {
	cases := []struct {
		title string
		state ActivityState
		ok    bool
	}{
		{"OC | permission prompt keeps reappearing", ActivityIdle, true},
		{"⠋ OC | ask Claude", ActivityWorking, true},
		{"tmux | ⠋ OC | ask Claude", ActivityWorking, true},
		{"⠋ π - session - cwd", ActivityWorking, true},
		{"π - ⠋ project", ActivityWorking, true},
		{"OMP > ⠋ project", ActivityWorking, true},
		{"π > ⠋ project", ActivityIdle, true},
		{"copilot.exe - action required", ActivityBlocked, true},
		{"⠋ Codex ready", ActivityWorking, true},
		{"database ready", ActivityUnknown, false},
		{"~/codex/ready", ActivityUnknown, false},
		{"opencode-blinker", ActivityUnknown, false},
	}
	for _, tc := range cases {
		state, ok := classifyTitle(tc.title)
		if state != tc.state || ok != tc.ok {
			t.Errorf("classifyTitle(%q) = %v, %v; want %v, %v", tc.title, state, ok, tc.state, tc.ok)
		}
	}
}

func TestTitleScannerObservesDuplicateTitlesButDeduplicatesDisplay(t *testing.T) {
	var observed, displayed []string
	var scanner titleScanner
	scanner.scanWithObserver(
		[]byte("\x1b]0;Codex working\a\x1b]0;Codex working\a"),
		func(title string) { observed = append(observed, title) },
		func(title string) { displayed = append(displayed, title) },
	)
	if len(observed) != 2 || observed[0] != observed[1] {
		t.Fatalf("observed titles = %q, want two identical observations", observed)
	}
	if len(displayed) != 1 || displayed[0] != observed[0] {
		t.Fatalf("displayed titles = %q, want one deduplicated title", displayed)
	}
}

func TestAgentActivityStaleFallbackRequiresTitlelessOutput(t *testing.T) {
	base := time.Now()
	s := &session{activity: Activity{State: ActivityWorking, ObservedAt: base}}
	if got, _ := s.agentActivity(base.Add(staleWorkingTitleTimeout)); got.State != ActivityWorking {
		t.Fatalf("silent working activity = %#v, want working", got)
	}

	s.staleSince = base
	got, ok := s.agentActivity(base.Add(staleWorkingTitleTimeout))
	if !ok || got.State != ActivityIdle || !got.Stale || !got.ObservedAt.Equal(base) {
		t.Fatalf("stale activity = %#v, %v; want stale idle at titleless output", got, ok)
	}
}

func TestAgentActivityDuplicateTitleCancelsStaleFallback(t *testing.T) {
	base := time.Now()
	s := &session{activity: Activity{State: ActivityWorking, ObservedAt: base}, staleSince: base}
	s.observeTitle("Codex working", base.Add(time.Second), false)
	if got, _ := s.agentActivity(base.Add(staleWorkingTitleTimeout)); !got.ObservedAt.Equal(base) {
		t.Fatalf("working repaint advanced ObservedAt to %v, want %v", got.ObservedAt, base)
	}
	got, ok := s.agentActivity(base.Add(staleWorkingTitleTimeout + time.Second))
	if !ok || got.State != ActivityWorking || got.Stale {
		t.Fatalf("activity after duplicate title = %#v, %v; want fresh working", got, ok)
	}
}

func TestHostAgentActivityPreservesChunkedAndCoalescedTitleOrder(t *testing.T) {
	var titles = make(chan string, 2)
	h, _ := newTestHost(t, func(cfg *Config) {
		cfg.OnTitle = func(_ SessionKey, title string) {
			titles <- title
		}
	})
	att := newFakeAtt()
	run := domain.RunID("run-title-order")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "\x1b]0;Codex wor")
	att.writeOutput(t, "king\a\x1b]2;Codex ready\a")
	want := []string{"Codex working", "Codex ready"}
	for _, expected := range want {
		select {
		case got := <-titles:
			if got != expected {
				t.Fatalf("title callback = %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for title %q", expected)
		}
	}
	waitFor(t, "final native idle activity", func() bool {
		activity, ok := h.AgentActivity(RunSession(run))
		return ok && activity.State == ActivityIdle && !activity.Stale
	})
}

func TestSessionWorkingRepaintDuringQuietDoesNotAdvanceActivity(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-working-repaint")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	s := h.lookup(RunSession(run))
	s.mu.Lock()
	s.paintQuietUntil = time.Now().Add(time.Hour)
	s.mu.Unlock()

	frame := []byte("\x1b]0;Codex working\a")
	s.deliver(frame)
	first, ok := h.AgentActivity(RunSession(run))
	if !ok || first.State != ActivityWorking {
		t.Fatalf("first working title activity = %#v, %v", first, ok)
	}
	s.deliver(frame)
	second, ok := h.AgentActivity(RunSession(run))
	if !ok || !second.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("working repaint advanced ObservedAt from %v to %v", first.ObservedAt, second.ObservedAt)
	}
	if output, ok := h.LastOutput(RunSession(run)); !ok || !output.IsZero() {
		t.Fatalf("working repaint advanced LastOutput to %v, %v", output, ok)
	}
}

func TestSemanticTitlePreservesStatusBeyondDisplayLimit(t *testing.T) {
	for _, size := range []int{maxTitleRunes, 2 * maxSemanticTitleRunes} {
		label := "Codex " + strings.Repeat("x", size)
		var scanner titleScanner
		var states []ActivityState
		var displayed []string
		for _, suffix := range []string{" working", " ready"} {
			scanner.scanWithObserver([]byte("\x1b]0;"+label+suffix+"\a"),
				func(title string) {
					state, _ := classifyTitle(title)
					states = append(states, state)
				},
				func(title string) { displayed = append(displayed, title) })
		}
		if len(states) != 2 || states[0] != ActivityWorking || states[1] != ActivityIdle {
			t.Fatalf("status after %d label runes = %v, want working then idle", size, states)
		}
		if len(displayed) != 1 || displayed[0] != label[:maxTitleRunes] {
			t.Fatalf("display titles = %q, want one unchanged bounded label", displayed)
		}
	}
}
