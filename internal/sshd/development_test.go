package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// The transport suite supplies only process I/O; authorization and surface
// generations remain the real server/control implementation under test.
type developmentTestService struct{ env *testEnv }

func installDevelopmentTestService(e *testEnv) {
	if e.srv.cfg.Control == nil {
		e.srv.cfg.Control = control.New(control.Config{})
	}
	e.srv.cfg.Services.Development = &developmentTestService{env: e}
}

func (d *developmentTestService) Call(ctx context.Context, run domain.Run, principal control.Principal, method string, raw json.RawMessage, authorize func(context.Context) error) (any, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	if principal.Kind != control.PrincipalMember || principal.RunID != "" {
		panic("human transport spoofed principal")
	}
	if method != protocol.MethodDevTerminalList {
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unused test operation"}
	}
	return protocol.DevTerminalListResult{Terminals: []protocol.DevTerminal{
		{TerminalID: "shell", Incarnation: "shell-incarnation", Cols: 80, Rows: 24, Process: protocol.DevProcessState{State: "running"}},
		{TerminalID: "shared", Incarnation: "shared-incarnation", Cols: 80, Rows: 24, Process: protocol.DevProcessState{State: "running"}},
	}}, nil
}
func (d *developmentTestService) AttachTerminal(ctx context.Context, run domain.Run, principal control.Principal, target protocol.DevTerminalTarget, fence protocol.DevControlFence, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint, authorize func(context.Context) error) error {
	if err := authorize(ctx); err != nil {
		return err
	}
	conn.(ptyhost.TerminalResponderWriter).SetTerminalResponder(true)
	if !client.ReadOnly {
		client.InputAdmission = func(accept func() error) error {
			return d.env.srv.cfg.Control.AdmitSurface(string(run.ID), control.Surface{Kind: control.SurfaceTerminal, ID: target.TerminalID, Incarnation: target.Incarnation}, principal, fence.ControlSessionID, fence.ControlGeneration, func() error {
				if err := authorize(ctx); err != nil {
					return err
				}
				return accept()
			})
		}
	}
	return d.env.pty.Attach(ctx, ptyhost.RunShellSession(run.ID, target.TerminalID), client, conn, resize)
}
func (d *developmentTestService) BrowserFrames(ctx context.Context, run domain.Run, principal control.Principal, target protocol.DevBrowserPageTarget, authorize func(context.Context) error) (<-chan protocol.DevBrowserFrame, func(), error) {
	if err := authorize(ctx); err != nil {
		return nil, nil, err
	}
	return make(chan protocol.DevBrowserFrame), func() {}, nil
}
func (d *developmentTestService) OpenArtifact(ctx context.Context, run domain.Run, principal control.Principal, id string, authorize func(context.Context) error) (protocol.DevArtifact, io.ReadCloser, error) {
	if err := authorize(ctx); err != nil {
		return protocol.DevArtifact{}, nil, err
	}
	return protocol.DevArtifact{ID: id, RunID: string(run.ID), ContentType: "image/png", Bytes: 3}, io.NopCloser(strings.NewReader("png")), nil
}

func TestDevelopmentReadsRequireSteerAndAccountUse(t *testing.T) {
	e := newTestEnv(t, nil)
	installDevelopmentTestService(e)
	_, other := addMember(t, e, "observer", domain.RoleCollaborator, false)
	local := e.srv.Local(other.ID)
	raw, _ := json.Marshal(protocol.DevTerminalListParams{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}})
	if _, err := local.Call(t.Context(), protocol.MethodDevTerminalList, raw); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("read without account-use = %v", err)
	}
	if err := e.store.ShareAccount(t.Context(), e.member.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Call(t.Context(), protocol.MethodDevTerminalList, raw); err != nil {
		t.Fatal(err)
	}
	e.run.Protected = true
	if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Call(t.Context(), protocol.MethodDevTerminalList, raw); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("protected read = %v", err)
	}
}

func TestDevelopmentHumanCannotSupplyPrincipal(t *testing.T) {
	e := newTestEnv(t, nil)
	installDevelopmentTestService(e)
	raw := json.RawMessage(`{"run_id":"` + string(e.run.ID) + `","principal":{"kind":"run_agent"}}`)
	if _, err := e.srv.Local(e.member.ID).Call(t.Context(), protocol.MethodDevTerminalList, raw); err == nil || err.Code != protocol.CodeInvalidParams {
		t.Fatalf("spoofed principal = %v", err)
	}
}

func TestDevelopmentShellIncarnationAndSurfaceGeneration(t *testing.T) {
	e := controlAttachEnv(t)
	installDevelopmentTestService(e)
	first, ack := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", ControlSessionID: "first"}, true)
	if !ack.OK || !ack.HasControl || !ack.ServerOwnedResponder || ack.Incarnation != "shell-incarnation" {
		t.Fatalf("shell ack = %+v", ack)
	}
	defer func() { _ = first.ch.Close() }()
	_, wrong := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", Incarnation: "retired", ControlSessionID: "replacement"}, true)
	if wrong.OK || wrong.Code != protocol.CodeConflict {
		t.Fatalf("retired incarnation = %+v", wrong)
	}
	second, next := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", Incarnation: ack.Incarnation, ControlSessionID: "second", Takeover: true, ControlGeneration: ack.ControlGeneration}, true)
	if !next.OK || next.ControlGeneration <= ack.ControlGeneration {
		t.Fatalf("takeover = %+v", next)
	}
	defer func() { _ = second.ch.Close() }()
	first.expectExit(t, protocol.AttachExitControlRevoked)
	_, stale := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", Incarnation: ack.Incarnation, ControlSessionID: "first", ReleaseControl: true, ControlGeneration: ack.ControlGeneration}, true)
	if stale.OK || stale.Code != protocol.CodeConflict {
		t.Fatalf("stale release = %+v", stale)
	}
}

func TestDevelopmentBrowserIdleStreamClosesOnAccountRevocation(t *testing.T) {
	e := newTestEnv(t, nil)
	installDevelopmentTestService(e)
	_, other := addMember(t, e, "browser observer", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(t.Context(), e.member.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := e.srv.Local(other.ID).BrowserFrames(ctx, protocol.DevBrowserStreamRequest{DevBrowserPageTarget: protocol.DevBrowserPageTarget{DevBrowserTarget: protocol.DevBrowserTarget{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, SessionID: "browser"}, PageID: "page", PageRevision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if revokeErr := e.store.RevokeAccountShare(t.Context(), e.member.ID, other.ID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	_, err = stream.Read(make([]byte, 1))
	var exit *protocol.RemoteExitError
	if !errors.As(err, &exit) || exit.Status != protocol.AttachExitSteerRevoked {
		t.Fatalf("idle account revocation = %v", err)
	}
}
