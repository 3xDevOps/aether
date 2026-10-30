package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestRunLaunchReplaysAnIdempotencyKey(t *testing.T) {
	e := newTestEnv(t, nil)
	e.runs.admitInto = e.store
	launch := func(task, key string) (protocol.RunLaunchResult, *protocol.Error) {
		t.Helper()
		raw, perr := callJSON(t, e, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude", IdempotencyKey: key})
		var result protocol.RunLaunchResult
		if perr == nil {
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result, perr
	}
	launches := func() int {
		n := 0
		for _, call := range e.runs.Calls() {
			if strings.HasPrefix(call, "launch:") {
				n++
			}
		}
		return n
	}

	first, perr := launch("build it", "chat-42")
	if perr != nil || first.Replayed {
		t.Fatalf("first launch = %+v, %v", first, perr)
	}
	again, perr := launch("build it", "chat-42")
	if perr != nil || !again.Replayed || again.Run.ID != first.Run.ID || launches() != 1 {
		t.Fatalf("retry = %+v, %v after %d launches; want session %s replayed", again, perr, launches(), first.Run.ID)
	}
	if _, perr := launch("build something else", "chat-42"); perr == nil || perr.Code != protocol.CodeConflict {
		t.Fatalf("same key, other params = %v, want CodeConflict", perr)
	}
	if err := e.store.DeleteRun(t.Context(), domain.RunID(first.Run.ID)); err != nil {
		t.Fatal(err)
	}
	if _, perr := launch("build it", "chat-42"); perr == nil || perr.Code != protocol.CodeNotFound || launches() != 1 {
		t.Fatalf("retry after the session was deleted = %v after %d launches, want CodeNotFound and no launch", perr, launches())
	}
	if _, perr := launch("build it", ""); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := launch("build it", ""); perr != nil || launches() != 3 {
		t.Fatalf("keyless launches = %d, %v; want each to launch", launches(), perr)
	}
}

// A retry with the key while the first launch is still provisioning
// returns the run without waiting for the provisioning.
func TestRunLaunchReplaysDuringProvisioning(t *testing.T) {
	e := newTestEnv(t, nil)
	started, release := make(chan struct{}, 1), make(chan struct{})
	e.runs.admitInto, e.runs.launchStarted, e.runs.launchRelease = e.store, started, release
	params := protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "slow image", Harness: "claude", IdempotencyKey: "k-slow"}
	first := make(chan *protocol.Error, 1)
	go func() {
		_, perr := callJSON(t, e, e.member.ID, protocol.MethodRunLaunch, params)
		first <- perr
	}()
	<-started
	replayed := make(chan protocol.RunLaunchResult, 1)
	go func() {
		var result protocol.RunLaunchResult
		raw, perr := callJSON(t, e, e.member.ID, protocol.MethodRunLaunch, params)
		if perr != nil || json.Unmarshal(raw, &result) != nil {
			t.Errorf("retry during provisioning = %s, %v", raw, perr)
		}
		replayed <- result
	}()
	select {
	case got := <-replayed:
		if bound, _, err := e.store.LaunchReceipt(t.Context(), e.member.ID, "k-slow"); err != nil || !got.Replayed || got.Run.ID != string(bound) {
			t.Errorf("retry during provisioning = %+v, want session %s replayed (%v)", got, bound, err)
		}
	case <-time.After(5 * time.Second):
		t.Error("a retry waited for the first launch's provisioning")
	}
	close(release)
	if perr := <-first; perr != nil {
		t.Fatal(perr)
	}
}

type failingReceipts struct {
	store.Store
	err error
}

func (f failingReceipts) CompleteLaunchReceipt(context.Context, domain.MemberID, string, domain.RunID) error {
	return f.err
}

func TestRunLaunchReceiptOutlivesAFailedLaunch(t *testing.T) {
	e := newTestEnv(t, nil)
	e.runs.admitInto = e.store
	launch := func(task, key string) (protocol.RunLaunchResult, *protocol.Error) {
		t.Helper()
		raw, perr := callJSON(t, e, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude", IdempotencyKey: key})
		var result protocol.RunLaunchResult
		if perr == nil {
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result, perr
	}
	launches := func() int {
		n := 0
		for _, call := range e.runs.Calls() {
			if strings.HasPrefix(call, "launch:") {
				n++
			}
		}
		return n
	}

	e.runs.failAdmitted = errors.New("provision: image pull failed")
	if _, perr := launch("provision fails", "k-failed"); perr == nil || !strings.Contains(perr.Message, "image pull failed") {
		t.Fatalf("failed launch = %v, want the launch error", perr)
	}
	e.runs.failAdmitted = nil
	left, _, err := e.store.LaunchReceipt(t.Context(), e.member.ID, "k-failed")
	if err != nil || left == "" {
		t.Fatalf("receipt after a failed launch = %q, %v; want the session it left", left, err)
	}
	replay, perr := launch("provision fails", "k-failed")
	if perr != nil || !replay.Replayed || replay.Run.ID != string(left) || launches() != 1 {
		t.Fatalf("retry after a failed launch = %+v, %v after %d launches; want session %s replayed", replay, perr, launches(), left)
	}

	fingerprint, err := launchFingerprint(protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "crashed", Harness: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.ReserveLaunchReceipt(t.Context(), e.member.ID, "k-pending", fingerprint); err != nil {
		t.Fatal(err)
	}
	relaunched, perr := launch("crashed", "k-pending")
	if perr != nil || relaunched.Replayed || launches() != 2 {
		t.Fatalf("retry of a reservation with no session = %+v, %v after %d launches; want one new launch", relaunched, perr, launches())
	}
	if bound, _, err := e.store.LaunchReceipt(t.Context(), e.member.ID, "k-pending"); err != nil || string(bound) != relaunched.Run.ID {
		t.Fatalf("reservation bound to %q, %v; want %s", bound, err, relaunched.Run.ID)
	}

	e.srv.cfg.Store = failingReceipts{Store: e.store, err: errors.New("disk I/O error")}
	if _, perr := launch("unrecorded", "k-unrecorded"); perr == nil || !strings.Contains(perr.Message, "disk I/O error") || !strings.Contains(perr.Message, "started") {
		t.Fatalf("launch whose receipt write failed = %v, want the write error naming the started session", perr)
	}
}

// A launch whose caller goes away keeps provisioning: cancelling it would
// fail the run it is creating.
func TestRunLaunchOutlivesItsCaller(t *testing.T) {
	e := newTestEnv(t, nil)
	started, release := e.runs.blockLaunch()
	raw, err := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: "slow image", Harness: "claude", IdempotencyKey: "k-gone"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.Local(e.member.ID).Call(ctx, protocol.MethodRunLaunch, raw)
		done <- perr
	}()
	<-started
	cancel()
	select {
	case perr := <-done:
		t.Fatalf("launch ended with %v when its caller went away, before provisioning finished", perr)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if perr := <-done; perr != nil {
		t.Fatalf("launch whose caller went away = %v, want the session", perr)
	}
}
