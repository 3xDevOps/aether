package sshd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/serverupdate"
)

// stubUpdates stands in for *serverupdate.Service: the handler's job is
// the admin gate, the error mapping, and holding the restart back until
// the response is out, none of which needs a real binary swap.
type stubUpdates struct {
	status  protocol.ServerUpdateStatusResult
	result  protocol.ServerUpdateResult
	err     error
	restart func()
	calls   chan protocol.ServerUpdateParams
}

func (s *stubUpdates) Status(context.Context) (protocol.ServerUpdateStatusResult, error) {
	return s.status, nil
}

func (s *stubUpdates) Update(_ context.Context, _ domain.MemberID, p protocol.ServerUpdateParams) (protocol.ServerUpdateResult, func(), error) {
	select {
	case s.calls <- p:
	default:
	}
	if s.err != nil {
		return protocol.ServerUpdateResult{}, nil, s.err
	}
	return s.result, s.restart, nil
}

func updateEnv(t *testing.T, svc *stubUpdates) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) { c.Services.ServerUpdate = svc })
}

func TestServerUpdateRequiresAdmin(t *testing.T) {
	svc := &stubUpdates{calls: make(chan protocol.ServerUpdateParams, 1)}
	e := updateEnv(t, svc)
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)

	err := controlAs(t, e, bob).Call(protocol.MethodServerUpdate,
		protocol.ServerUpdateParams{When: protocol.ServerUpdateNow}, nil)
	wantDenied(t, err, "collaborator server.update")
	if !strings.Contains(err.Error(), protocol.MethodServerUpdate+" requires the admin role") {
		t.Fatalf("error = %v, want it to name the method and the role", err)
	}
	select {
	case p := <-svc.calls:
		t.Fatalf("a denied call still reached the service with %+v", p)
	default:
	}
}

// The status is readable by anyone: a collaborator looking at the "server
// is behind" banner should learn whether an admin can press the button or
// has to run the commands on the host.
func TestServerUpdateStatusIsReadableByAnyMember(t *testing.T) {
	svc := &stubUpdates{status: protocol.ServerUpdateStatusResult{
		ServerVersion:   "v0.1.0",
		Latest:          "v0.2.0",
		UpdateAvailable: true,
		Capable:         true,
	}}
	e := updateEnv(t, svc)
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)

	var got protocol.ServerUpdateStatusResult
	if err := controlAs(t, e, viewer).Call(protocol.MethodServerUpdateStatus, struct{}{}, &got); err != nil {
		t.Fatalf("server.update_status as a viewer: %v", err)
	}
	if got.Latest != "v0.2.0" || !got.UpdateAvailable || !got.Capable {
		t.Fatalf("status = %+v, want the stub's answer", got)
	}
}

// The restart replaces the process, so it must not run until the result
// line has been written: a client that never saw it could not tell a
// restart from a dropped connection. The stub blocks in the restart, so a
// Call that returns proves the response went out first.
func TestServerUpdateRestartsOnlyAfterTheResponse(t *testing.T) {
	release := make(chan struct{})
	restarted := make(chan struct{})
	svc := &stubUpdates{
		result: protocol.ServerUpdateResult{Status: protocol.ServerUpdateApplying, Version: "v0.2.0"},
		restart: func() {
			close(restarted)
			<-release
		},
	}
	e := updateEnv(t, svc)
	defer close(release)

	var got protocol.ServerUpdateResult
	done := make(chan error, 1)
	go func() {
		done <- controlClient(t, e).Call(protocol.MethodServerUpdate,
			protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow}, &got)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server.update: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server.update did not answer before the restart ran")
	}
	if got.Status != protocol.ServerUpdateApplying || got.Version != "v0.2.0" {
		t.Fatalf("result = %+v, want an applying v0.2.0", got)
	}
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the restart never ran after the response")
	}
}

// An unprivileged server refuses with CodeInvalidState and names the two
// commands to run on the host, the same pair server.update_status returns
// as manual_commands.
func TestServerUpdateIncapableNamesTheManualCommands(t *testing.T) {
	e := updateEnv(t, &stubUpdates{err: serverupdate.ErrIncapable})

	err := controlClient(t, e).Call(protocol.MethodServerUpdate,
		protocol.ServerUpdateParams{When: protocol.ServerUpdateNow}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("server.update on an incapable server = %v, want CodeInvalidState", err)
	}
	for _, cmd := range serverupdate.ManualCommands() {
		if !strings.Contains(pe.Message, cmd) {
			t.Fatalf("message %q does not name %q", pe.Message, cmd)
		}
	}
}

func TestServerUpdateBadTagIsInvalidParams(t *testing.T) {
	e := updateEnv(t, &stubUpdates{err: serverupdate.ErrBadTag})

	err := controlClient(t, e).Call(protocol.MethodServerUpdate,
		protocol.ServerUpdateParams{Version: "latest", When: protocol.ServerUpdateNow}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("server.update with a bad tag = %v, want CodeInvalidParams", err)
	}
}

// A server assembled without the service says so rather than pretending
// the method does not exist.
func TestServerUpdateUnavailableWithoutTheService(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	for _, method := range []string{protocol.MethodServerUpdate, protocol.MethodServerUpdateStatus} {
		err := c.Call(method, struct{}{}, nil)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
			t.Fatalf("%s without the service = %v, want CodeUnavailable", method, err)
		}
	}
}
