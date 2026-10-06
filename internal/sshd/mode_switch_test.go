package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func (f *fakeRuns) SwitchMode(_ context.Context, run domain.RunID, actor domain.MemberID, mode domain.LaunchMode, admit func(begin func() error) error) error {
	if err := f.record(fmt.Sprintf("mode.switch:%s:%s:%s", run, actor, mode)); err != nil {
		return err
	}
	return admit(func() error { return f.record(fmt.Sprintf("mode.begin:%s:%s", run, mode)) })
}

func (f *fakeRuns) Switching(domain.RunID) domain.LaunchMode { return "" }

func (f *fakeRuns) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(slices.DeleteFunc(slices.Clone(f.calls), func(c string) bool { return c != call }))
}

func TestRunModeSwitchNeedsSteerAndAHeldLease(t *testing.T) {
	e, run := newACPEnv(t)
	switchTo := func(member domain.MemberID, mode string, lease protocol.ACPLease) (json.RawMessage, *protocol.Error) {
		return callJSON(t, e, member, protocol.MethodRunModeSwitch, protocol.RunModeSwitchParams{RunID: string(run.ID), Mode: mode, ACPLease: lease})
	}
	begun := fmt.Sprintf("mode.begin:%s:tui", run.ID)
	if _, perr := switchTo(e.member.ID, "headless", protocol.ACPLease{}); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("switch to background: %v", perr)
	}
	_, viewer := addMember(t, e, "Vic", domain.RoleViewer, false)
	if _, perr := switchTo(viewer.ID, "tui", protocol.ACPLease{}); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("switch by a viewer: %v", perr)
	}
	raw, perr := switchTo(e.member.ID, "tui", protocol.ACPLease{})
	if perr != nil {
		t.Fatalf("switch while nobody holds control: %v", perr)
	}
	var res protocol.RunResult
	if err := json.Unmarshal(raw, &res); err != nil || res.Run.ID != string(run.ID) {
		t.Fatalf("result %s (%v)", raw, err)
	}
	if got := e.runs.count(begun); got != 1 {
		t.Fatalf("begun %d times, want 1", got)
	}

	writer, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, perr := switchTo(e.member.ID, "tui", protocol.ACPLease{}); perr == nil || perr.Code != protocol.CodeConflict ||
		!strings.Contains(perr.Message, string(e.member.ID)+" holds the run's control") {
		t.Fatalf("switch without the held lease: %v", perr)
	}
	if _, perr := switchTo(e.member.ID, "tui", protocol.ACPLease{ControlSessionID: "tab-2", ControlGeneration: ack.ControlGeneration}); perr == nil || perr.Code != protocol.CodeConflict {
		t.Fatalf("switch from another session: %v", perr)
	}
	if _, perr := switchTo(e.member.ID, "tui", protocol.ACPLease{ControlSessionID: "tab-1", ControlGeneration: ack.ControlGeneration}); perr != nil {
		t.Fatalf("switch by the lease holder: %v", perr)
	}
	if got := e.runs.count(begun); got != 2 {
		t.Fatalf("begun %d times, want 2", got)
	}
}

func TestRunModeSwitchRefusalsAreTyped(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("wrapped: %w", scheduler.ErrNotSwitchable): protocol.ErrorReasonNotSwitchable,
		scheduler.ErrSessionNotReported:                       protocol.ErrorReasonSessionNotReported,
		scheduler.ErrAdapterNotInstalled:                      protocol.ErrorReasonAdapterNotInstalled,
	} {
		perr := modeSwitchError(err)
		var data struct {
			Reason string `json:"reason"`
		}
		if perr == nil || perr.Code != protocol.CodeInvalidState || json.Unmarshal(perr.Data, &data) != nil || data.Reason != want {
			t.Fatalf("%v: %+v, want data.reason %s", err, perr, want)
		}
		if perr.Message != err.Error() {
			t.Fatalf("message %q, want the real error %q", perr.Message, err.Error())
		}
	}
	if perr := modeSwitchError(fmt.Errorf("%w: paused", scheduler.ErrInvalidTransition)); perr.Code != protocol.CodeInvalidState {
		t.Fatalf("invalid state: %+v", perr)
	}
}
