package sshd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
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
		t.Fatalf("retry = %+v, %v after %d launches; want run %s replayed", again, perr, launches(), first.Run.ID)
	}
	if _, perr := launch("build something else", "chat-42"); perr == nil || perr.Code != protocol.CodeConflict {
		t.Fatalf("same key, other params = %v, want CodeConflict", perr)
	}
	if err := e.store.DeleteRun(t.Context(), domain.RunID(first.Run.ID)); err != nil {
		t.Fatal(err)
	}
	if _, perr := launch("build it", "chat-42"); perr == nil || perr.Code != protocol.CodeNotFound || launches() != 1 {
		t.Fatalf("retry after the run was deleted = %v after %d launches, want CodeNotFound and no launch", perr, launches())
	}
	if _, perr := launch("build it", ""); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := launch("build it", ""); perr != nil || launches() != 3 {
		t.Fatalf("keyless launches = %d, %v; want each to launch", launches(), perr)
	}
}
