package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

func TestRebootRecoveryPreservesNativeAttention(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  ptyhost.ActivityState
		stale  bool
		reason string
	}{
		{name: "idle", state: ptyhost.ActivityIdle, reason: nativeIdleReason},
		{name: "blocked", state: ptyhost.ActivityBlocked, reason: nativeBlockedReason},
		{name: "stale", state: ptyhost.ActivityIdle, stale: true, reason: nativeStaleReason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			sub := e.subscribe(t)
			run, c := e.launchFake(t, "preserve native attention")

			e.pty.setActivity(run.ID, tc.state, tc.stale)
			e.sched.checkNativeActivity(t.Context())
			ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
			if got := ev.Payload.(events.RunStatusPayload).Reason; got != tc.reason {
				t.Fatalf("attention reason = %q, want %q", got, tc.reason)
			}

			if err := e.sched.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			pty2 := newFakePTY()
			s2 := e.newScheduler(t, e.rt, pty2)
			startScheduler(t, s2)
			waitFor(t, "recovered pty session", func() bool {
				return pty2.session(run.ID) != nil
			})

			c.output("fresh output\r\n")
			waitFor(t, "fresh output after recovery", func() bool {
				sess := pty2.session(run.ID)
				return sess != nil && sess.output() != ""
			})
			e.git.touch(run.ID)
			s2.checkStalls(t.Context())
			recovered, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatalf("GetRun after recovery: %v", err)
			}
			if recovered.Status != domain.RunNeedsAttention || recovered.Reason != tc.reason {
				t.Fatalf("recovered state = %s (%q), want needs-attention (%q)",
					recovered.Status, recovered.Reason, tc.reason)
			}

			pty2.setActivity(run.ID, ptyhost.ActivityWorking, false)
			s2.checkNativeActivity(t.Context())
			resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
			if got := resumed.Payload.(events.RunStatusPayload).Reason; got != "activity resumed" {
				t.Fatalf("resume reason = %q, want activity resumed", got)
			}
		})
	}
}

func TestRebootRecoveryKeepsGenericStallRecoverable(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Nanosecond
		cfg.PollInterval = time.Hour
	})
	sub := e.subscribe(t)
	run, c := e.launchFake(t, "recover generic stall")

	e.sched.checkStalls(t.Context())
	stalled := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if reason := stalled.Payload.(events.RunStatusPayload).Reason; !strings.HasPrefix(reason, "stalled: ") {
		t.Fatalf("stall reason = %q", reason)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	s2.cfg.StallThreshold = time.Hour
	startScheduler(t, s2)
	waitFor(t, "recovered pty session", func() bool {
		return pty2.session(run.ID) != nil
	})
	c.output("fresh output\r\n")
	waitFor(t, "fresh output after recovery", func() bool {
		sess := pty2.session(run.ID)
		return sess != nil && sess.output() != ""
	})
	e.git.touch(run.ID)
	s2.checkStalls(t.Context())
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if reason := resumed.Payload.(events.RunStatusPayload).Reason; reason != "activity resumed" {
		t.Fatalf("resume reason = %q, want activity resumed", reason)
	}
}
