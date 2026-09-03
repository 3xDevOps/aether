package sshd

import (
	"bytes"
	"context"
	"encoding/json"
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

// failingWriter is a channel that dies mid-call, the way a client that
// vanished between the binary swap and the response looks from here.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// The deferred restart runs even when the response could not be written.
// By then apply has already replaced both binaries and recorded the
// update as applied; dropping the restart would leave the server on the
// old image with its one update slot held for the rest of the process's
// life, and no way for anyone to clear it.
func TestRespondRestartsEvenWhenTheWriteFails(t *testing.T) {
	ran := false
	slot := &afterResponse{fn: func() { ran = true }}
	want := errors.New("channel closed")

	err := respond(failingWriter{err: want}, protocol.Response{JSONRPC: "2.0"}, slot)
	if !errors.Is(err, want) {
		t.Fatalf("respond error = %v, want the write error", err)
	}
	if !ran {
		t.Fatal("a failed response write dropped the deferred restart")
	}
}

// The ordinary path still writes first and defers second.
func TestRespondWritesBeforeTheDeferredWork(t *testing.T) {
	var out bytes.Buffer
	var order []string
	slot := &afterResponse{fn: func() { order = append(order, "deferred") }}

	if err := respond(&out, protocol.Response{JSONRPC: "2.0", ID: json.RawMessage(`1`)}, slot); err != nil {
		t.Fatalf("respond: %v", err)
	}
	order = append(order, "checked")
	if out.Len() == 0 {
		t.Fatal("respond wrote nothing")
	}
	if len(order) != 2 || order[0] != "deferred" {
		t.Fatalf("order = %v, want the deferred work to have already run", order)
	}
}

// A slot no handler filled is simply not run.
func TestRespondWithNothingDeferred(t *testing.T) {
	var out bytes.Buffer
	if err := respond(&out, protocol.Response{JSONRPC: "2.0"}, &afterResponse{}); err != nil {
		t.Fatalf("respond: %v", err)
	}
}

// Every role below admin is refused, not just the collaborator default.
func TestServerUpdateDeniedForEveryNonAdminRole(t *testing.T) {
	svc := &stubUpdates{calls: make(chan protocol.ServerUpdateParams, 1)}
	e := updateEnv(t, svc)
	for name, role := range map[string]domain.Role{
		"Vera":  domain.RoleViewer,
		"Colin": domain.RoleCollaborator,
	} {
		signer, _ := addMember(t, e, name, role, false)
		wantDenied(t, controlAs(t, e, signer).Call(protocol.MethodServerUpdate,
			protocol.ServerUpdateParams{When: protocol.ServerUpdateNow}, nil), string(role)+" server.update")
	}
	select {
	case p := <-svc.calls:
		t.Fatalf("a denied call still reached the service with %+v", p)
	default:
	}
}

// A member awaiting approval reaches no method but server.info, the status
// method included.
func TestServerUpdatePendingMemberIsDeniedBothMethods(t *testing.T) {
	e := updateEnv(t, &stubUpdates{})
	signer, _ := addMember(t, e, "Pat", domain.RoleAdmin, true)
	c := controlAs(t, e, signer)

	for _, method := range []string{protocol.MethodServerUpdate, protocol.MethodServerUpdateStatus} {
		wantDenied(t, c.Call(method, struct{}{}, nil), "pending member "+method)
	}
}

// Removing a member revokes the connection they already hold.
func TestServerUpdateRemovedMemberIsDenied(t *testing.T) {
	e := updateEnv(t, &stubUpdates{})
	signer, member := addMember(t, e, "Gone", domain.RoleAdmin, false)
	c := controlAs(t, e, signer)
	if err := e.store.DeleteMember(context.Background(), member.ID); err != nil {
		t.Fatalf("delete member: %v", err)
	}
	wantDenied(t, c.Call(protocol.MethodServerUpdate,
		protocol.ServerUpdateParams{When: protocol.ServerUpdateNow}, nil), "removed member server.update")
}
