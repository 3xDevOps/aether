package sshd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
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

func TestTerminalAdmissionSerializesMemberRemoval(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, target := addMember(t, e, "Terminal user", domain.RoleCollaborator, false)
	started, release := e.runs.blockEnsureTerminal()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	type terminalResult struct {
		term *LocalTerminal
		ack  protocol.TerminalResponse
		err  error
	}
	terminalDone := make(chan terminalResult, 1)
	go func() {
		term, ack, err := e.srv.Local(target.ID).Terminal(ctx, protocol.TerminalRequest{
			Tab: "main", Cols: 80, Rows: 24,
		})
		terminalDone <- terminalResult{term: term, ack: ack, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("terminal admission did not reach EnsureTerminal")
	}

	params, err := json.Marshal(protocol.MemberRemoveParams{MemberID: string(target.ID)})
	if err != nil {
		t.Fatal(err)
	}
	removed := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.memberRemove(ctx, e.member.ID, params)
		removed <- perr
	}()
	select {
	case perr := <-removed:
		t.Fatalf("member.remove returned before terminal admission completed: %+v", perr)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	var result terminalResult
	select {
	case result = <-terminalDone:
	case <-time.After(time.Second):
		t.Fatal("terminal admission did not complete after release")
	}
	if result.err != nil || !result.ack.OK {
		t.Fatalf("terminal admission = ack %+v err %v, want success", result.ack, result.err)
	}
	if perr := <-removed; perr != nil {
		t.Fatalf("member.remove after terminal admission: %+v", perr)
	}
	if result.term != nil {
		_ = result.term.Close()
	}
	if _, err := e.store.GetMember(ctx, target.ID); err == nil {
		t.Fatal("member remained after successful removal")
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
