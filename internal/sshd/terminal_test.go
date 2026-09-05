package sshd

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func TestTerminalControlStatusAndStopAreMemberScoped(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var status protocol.TerminalStatusResult
	if err := c.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
		t.Fatalf("terminal.status: %v", err)
	}
	if status.Running || status.Image != "" || len(status.Tabs) != 0 {
		t.Fatalf("status = %+v, want empty stopped status", status)
	}
	if err := c.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop: %v", err)
	}
	calls := e.runs.Calls()
	if len(calls) < 2 || calls[len(calls)-2] != "terminal-status:"+string(e.member.ID) || calls[len(calls)-1] != "terminal-stop:"+string(e.member.ID) {
		t.Fatalf("RunController calls = %v", calls)
	}
}

func TestTerminalEnvironmentSaveAndResetAreMemberScoped(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	var saved protocol.EnvSaveResult
	if err := c.Call(protocol.MethodEnvSave, struct{}{}, &saved); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	wantImage := "aether/member-" + string(e.member.ID) + ":1"
	if saved.Image != wantImage {
		t.Fatalf("saved image = %q, want %q", saved.Image, wantImage)
	}
	if err := c.Call(protocol.MethodEnvReset, struct{}{}, nil); err != nil {
		t.Fatalf("env.reset: %v", err)
	}
	calls := e.runs.Calls()
	if len(calls) < 2 || calls[len(calls)-2] != "env-save:"+string(e.member.ID) || calls[len(calls)-1] != "env-reset:"+string(e.member.ID) {
		t.Fatalf("RunController calls = %v", calls)
	}
}

func TestEnvironmentTerminalNotRunningMapsToInvalidState(t *testing.T) {
	if e := rpcError(scheduler.ErrTerminalNotRunning); e.Code != protocol.CodeInvalidState {
		t.Fatalf("terminal not running code = %d, want %d", e.Code, protocol.CodeInvalidState)
	}
}

func TestTerminalSentinelsMapToWireCodes(t *testing.T) {
	if e := rpcError(scheduler.ErrTerminalTabLimit); e.Code != protocol.CodeInvalidState {
		t.Fatalf("tab limit code = %d, want %d", e.Code, protocol.CodeInvalidState)
	}
	if e := rpcError(scheduler.ErrInvalidTerminalTab); e.Code != protocol.CodeInvalidParams {
		t.Fatalf("invalid tab code = %d, want %d", e.Code, protocol.CodeInvalidParams)
	}
}
