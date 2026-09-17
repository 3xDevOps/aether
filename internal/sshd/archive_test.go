package sshd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// createRunWithStatus creates a run owned by e.member directly in status,
// bypassing the scheduler so archive tests can exercise every status
// without a real container.
func createRunWithStatus(t *testing.T, e *testEnv, status domain.RunStatus) *domain.Run {
	t.Helper()
	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "archive me",
		Harness: "claude", Mode: domain.LaunchTUI, Status: status,
		Branch: "aether/run-archive-me",
	}
	if err := e.store.CreateRun(context.Background(), r); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return r
}

// run.archive maps the scheduler's invalid-transition refusal to
// CodeInvalidState, preserving the real status in the message. The
// refusal itself is the scheduler's; here only the RPC mapping is
// exercised.
func TestRunArchiveRefusedOnNonFinalStatus(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	admin := controlClient(t, e)
	r := createRunWithStatus(t, e, domain.RunRunning)

	e.runs.setErr(fmt.Errorf("%w: run is %s; only merged, abandoned, failed or interrupted runs can be archived",
		errInvalidTransition, domain.RunRunning))
	err := admin.Call(protocol.MethodRunArchive,
		protocol.RunArchiveParams{RunID: string(r.ID), Archived: true}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("archive %s run: err = %v, want CodeInvalidState", domain.RunRunning, err)
	}
	if !strings.Contains(pe.Message, string(domain.RunRunning)) {
		t.Fatalf("archive error = %q, want it to name the real status", pe.Message)
	}
}

// A protected run rejects a non-owner collaborator's run.archive, the
// same rule run.kill and run.delete already enforce; the owner still
// archives it.
func TestRunArchiveProtectedRunDeniedForCollaborator(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	r := createRunWithStatus(t, e, domain.RunAbandoned)
	if err := e.store.SetRunProtected(ctx, r.ID, true); err != nil {
		t.Fatalf("SetRunProtected: %v", err)
	}

	cc := controlAs(t, e, collab)
	wantDenied(t, cc.Call(protocol.MethodRunArchive,
		protocol.RunArchiveParams{RunID: string(r.ID), Archived: true}, nil),
		"collaborator archive of protected run")

	admin := controlClient(t, e)
	var res protocol.RunResult
	if err := admin.Call(protocol.MethodRunArchive,
		protocol.RunArchiveParams{RunID: string(r.ID), Archived: true}, &res); err != nil {
		t.Fatalf("owner archive of protected run: %v", err)
	}
	if res.Run.ArchivedAt == nil {
		t.Fatal("archived run missing archived_at on the wire")
	}
}

// The run.archive RPC's RunResult carries archived_at and deletes_at
// while archived, and both are absent after a restore.
func TestRunArchiveWireShape(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	admin := controlClient(t, e)
	r := createRunWithStatus(t, e, domain.RunMerged)

	var archived protocol.RunResult
	if err := admin.Call(protocol.MethodRunArchive,
		protocol.RunArchiveParams{RunID: string(r.ID), Archived: true}, &archived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Run.ArchivedAt == nil || archived.Run.DeletesAt == nil {
		t.Fatalf("archived run = %+v, want archived_at and deletes_at set", archived.Run)
	}

	var restored protocol.RunResult
	if err := admin.Call(protocol.MethodRunArchive,
		protocol.RunArchiveParams{RunID: string(r.ID), Archived: false}, &restored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Run.ArchivedAt != nil || restored.Run.DeletesAt != nil {
		t.Fatalf("restored run wire fields = %+v, want both nil", restored.Run)
	}
}
