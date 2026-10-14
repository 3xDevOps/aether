package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeCaller struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeCaller) Call(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, *protocol.Error) {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	switch method {
	case protocol.MethodRunGet:
		return json.RawMessage(`{"run":{"id":"run-1","title":"Fix login","task":"t"}}`), nil
	case protocol.MethodRunList:
		return json.RawMessage(`{"runs":[]}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeCaller) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func start(t *testing.T, confirm Confirmer) (string, *fakeCaller) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCaller{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- (&Server{Caller: fc, Confirm: confirm}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return sock, fc
}

func rpcCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return coordtransport.ErrorCode(err)
}

func TestAllowlistAndMemberID(t *testing.T) {
	sock, fc := start(t, nil)
	err := coordtransport.Call(t.Context(), sock, "member.list", nil, nil)
	if rpcCode(t, err) != protocol.CodeMethodNotFound || !strings.Contains(err.Error(), "run.launch") {
		t.Fatalf("member.list: %v", err)
	}
	err = coordtransport.Call(t.Context(), sock, protocol.MethodRunList, map[string]any{"member_id": "m2"}, nil)
	if rpcCode(t, err) != protocol.CodeInvalidParams {
		t.Fatalf("member_id: %v", err)
	}
	if got := fc.called(); len(got) != 0 {
		t.Fatalf("caller reached: %v", got)
	}
}

func TestReadPassesThrough(t *testing.T) {
	sock, fc := start(t, nil)
	var out protocol.RunListResult
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodRunList, protocol.RunListParams{ActiveOnly: true}, &out); err != nil {
		t.Fatal(err)
	}
	if got := fc.called(); len(got) != 1 || got[0] != protocol.MethodRunList {
		t.Fatalf("calls: %v", got)
	}
}

func TestConfirmedApproveAndDecline(t *testing.T) {
	var got protocol.AssistantConfirmation
	var verdict error
	sock, fc := start(t, func(_ context.Context, c protocol.AssistantConfirmation) error {
		got = c
		return verdict
	})
	params := protocol.RunIDParams{RunID: "run-1"}

	verdict = errors.New("declined by the member")
	err := coordtransport.Call(t.Context(), sock, protocol.MethodRunKill, params, nil)
	if rpcCode(t, err) != protocol.CodeDenied || !strings.Contains(err.Error(), "declined by the member") {
		t.Fatalf("decline: %v", err)
	}
	if got.Summary != `Stop run run-1 ("Fix login")` || got.Method != protocol.MethodRunKill {
		t.Fatalf("confirmation: %+v", got)
	}
	for _, m := range fc.called() {
		if m == protocol.MethodRunKill {
			t.Fatal("run.kill ran after a decline")
		}
	}

	verdict = nil
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodRunKill, params, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if calls := fc.called(); calls[len(calls)-1] != protocol.MethodRunKill {
		t.Fatalf("calls: %v", calls)
	}

	if err := coordtransport.Call(t.Context(), sock, protocol.MethodEnvSave, nil, nil); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	if !strings.HasPrefix(got.Summary, "Save your environment") {
		t.Fatalf("env.save summary: %q", got.Summary)
	}
}

func TestDocs(t *testing.T) {
	sock, _ := start(t, nil)
	var list protocol.AssistantDocsResult
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodAssistantDocs, nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Topics) == 0 || strings.HasSuffix(list.Topics[0], ".md") {
		t.Fatalf("topics: %v", list.Topics)
	}
	var topic protocol.AssistantDocsResult
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodAssistantDocs, protocol.AssistantDocsParams{Topic: "quickstart"}, &topic); err != nil {
		t.Fatal(err)
	}
	if topic.Text == "" {
		t.Fatal("empty quickstart")
	}
	err := coordtransport.Call(t.Context(), sock, protocol.MethodAssistantDocs, protocol.AssistantDocsParams{Topic: "nope"}, nil)
	if rpcCode(t, err) != protocol.CodeNotFound || !strings.Contains(err.Error(), "quickstart") {
		t.Fatalf("unknown topic: %v", err)
	}
}
