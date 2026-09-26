package coord

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordcli"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func runContextHook(t *testing.T, socket string) (int, error) {
	t.Helper()
	return coordcli.Run(context.Background(), []string{"hook", "generic", "context"}, coordcli.Config{
		Socket: socket,
		In:     strings.NewReader(`{}`),
		Out:    io.Discard,
		ErrOut: io.Discard,
	})
}

func requireTransportConflict(t *testing.T, err error) {
	t.Helper()
	if coordtransport.ErrorCode(err) == protocol.CodeConflict {
		return
	}
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeConflict {
		t.Fatalf("error = %v, want transport CodeConflict", err)
	}
}

// Exercise the production CLI hook, not just the new wire method. The fixed
// service clock prevents real elapsed time from replenishing either budget.
func TestNativeHookBurstPreservesExplicitCoordination(t *testing.T) {
	h := newHarness(t, 2, func(cfg *Config) {
		cfg.Evidence = &coordReportEvidenceCapture{id: "after-hooks-evidence"}
	})
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	dir, provisionErr := h.svc.Provision(ctx, a, nil)
	if provisionErr != nil {
		t.Fatal(provisionErr)
	}
	pending, rpcErr := h.svc.Send(ctx, b, sendParams(a, "please coordinate"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	socket := filepath.Join(dir, coordtransport.SocketName)
	for i := range requestBurst {
		if code, err := runContextHook(t, socket); err != nil || code != coordcli.ExitOK {
			t.Fatalf("hook %d: exit %d, error %v", i, code, err)
		}
	}
	for range 3 {
		code, err := runContextHook(t, socket)
		if code != coordcli.ExitFailure {
			t.Fatalf("hook after burst: exit %d, want failure", code)
		}
		requireTransportConflict(t, err)
	}

	client := h.dial(t, a)
	var status protocol.CoordStatusResult
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil {
		t.Fatalf("explicit status after hook burst: %v", err)
	}
	if status.RunID != string(a) || status.Unread != 1 {
		t.Fatalf("status after hooks = %+v, want authenticated run and unread message", status)
	}
	var inbox protocol.CoordInboxResult
	if err := client.Call(protocol.MethodCoordInbox, nil, &inbox); err != nil {
		t.Fatalf("explicit inbox after hook burst: %v", err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != pending.MessageID || inbox.AckToken == "" {
		t.Fatalf("hooks must not acknowledge pending mail: %+v", inbox)
	}
	var sent protocol.CoordSendResult
	if err := client.Call(protocol.MethodCoordSend, sendParams(b, "coordinated reply"), &sent); err != nil {
		t.Fatalf("explicit send after hook burst: %v", err)
	}
	peerInbox, rpcErr := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if rpcErr != nil || len(peerInbox.Messages) != 1 || peerInbox.Messages[0].ID != sent.MessageID || peerInbox.Messages[0].Body != "coordinated reply" {
		t.Fatalf("sent message = %+v, error %v", peerInbox, rpcErr)
	}
	var report protocol.CoordReportResult
	if err := client.Call(protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "coordination complete", IdempotencyKey: "after-hooks",
	}, &report); err != nil {
		t.Fatalf("explicit report after hook burst: %v", err)
	}
	if report.Outcome != protocol.CoordOutcomeSuccess || report.Summary != "coordination complete" {
		t.Fatalf("report = %+v", report)
	}

	h.advance(requestRefill)
	if code, err := runContextHook(t, socket); err != nil || code != coordcli.ExitOK {
		t.Fatalf("hook after refill: exit %d, error %v", code, err)
	}
	_, err := runContextHook(t, socket)
	requireTransportConflict(t, err)
}

func TestOrdinaryBudgetExhaustionPreservesHooksNotMutations(t *testing.T) {
	h := newHarness(t, 2)
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	dir, provisionErr := h.svc.Provision(ctx, a, nil)
	if provisionErr != nil {
		t.Fatal(provisionErr)
	}
	client := h.dial(t, a)
	for i := range requestBurst {
		if err := client.Call(protocol.MethodCoordStatus, nil, nil); err != nil {
			t.Fatalf("ordinary status %d: %v", i, err)
		}
	}
	requireTransportConflict(t, client.Call(protocol.MethodCoordStatus, nil, nil))
	if code, err := runContextHook(t, filepath.Join(dir, coordtransport.SocketName)); err != nil || code != coordcli.ExitOK {
		t.Fatalf("hook with exhausted ordinary budget: exit %d, error %v", code, err)
	}
	// A hook request may carry mutation-shaped params, but can only read status.
	var status protocol.CoordStatusResult
	if err := client.Call(protocol.MethodCoordHookStatus, sendParams(b, "not a send"), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID != string(a) {
		t.Fatalf("hook status identity = %q, want %q", status.RunID, a)
	}
	requireTransportConflict(t, client.Call(protocol.MethodCoordSend, sendParams(b, "blocked send"), nil))
	if unread, err := h.db.CountUnackedRunMessages(ctx, b); err != nil || unread != 0 {
		t.Fatalf("mutation escaped ordinary budget: unread %d, error %v", unread, err)
	}
	h.advance(requestRefill)
	if err := client.Call(protocol.MethodCoordSend, sendParams(b, "allowed after refill"), nil); err != nil {
		t.Fatalf("send after ordinary refill: %v", err)
	}
	requireTransportConflict(t, client.Call(protocol.MethodCoordStatus, nil, nil))
}

func TestMalformedAndUnknownTrafficUsesOrdinaryBudget(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		code int
	}{
		"malformed":        {`{"jsonrpc":"2.0","id":1,"method":"coord.hook.status",`, protocol.CodeParse},
		"invalid-envelope": {`{"jsonrpc":"1.0","id":1,"method":"coord.hook.status"}`, protocol.CodeInvalidRequest},
		"unknown":          {`{"jsonrpc":"2.0","id":1,"method":"coord.hook.send"}`, protocol.CodeMethodNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 2)
			h.start()
			for _, run := range []domain.RunID{h.run(0), h.run(1)} {
				if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
					t.Fatal(err)
				}
			}
			conn, dialErr := net.Dial("unix", filepath.Join(h.dir, "coord", string(h.run(0)), coordtransport.SocketName))
			if dialErr != nil {
				t.Fatal(dialErr)
			}
			defer conn.Close() //nolint:errcheck // test cleanup
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			for i := range requestBurst {
				if _, err := fmt.Fprintln(conn, tc.line); err != nil {
					t.Fatal(err)
				}
				line, readErr := protocol.ReadLine(reader)
				if readErr != nil {
					t.Fatal(readErr)
				}
				var resp protocol.Response
				if err := json.Unmarshal(line, &resp); err != nil {
					t.Fatal(err)
				}
				if resp.Error == nil || resp.Error.Code != tc.code {
					t.Fatalf("request %d: error %+v, want code %d", i, resp.Error, tc.code)
				}
			}
			client := h.dial(t, h.run(0))
			requireTransportConflict(t, client.Call(protocol.MethodCoordStatus, nil, nil))
			if err := client.Call(protocol.MethodCoordHookStatus, nil, nil); err != nil {
				t.Fatalf("invalid traffic consumed hook budget: %v", err)
			}
			other := h.dial(t, h.run(1))
			if err := other.Call(protocol.MethodCoordStatus, nil, nil); err != nil {
				t.Fatalf("invalid traffic consumed another run's budget: %v", err)
			}
			if _, err := fmt.Fprintln(conn, `{"jsonrpc":"1.0","id":2,"method":"coord.hook.status"}`); err != nil {
				t.Fatal(err)
			}
			line, readErr := protocol.ReadLine(reader)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var limited protocol.Response
			if err := json.Unmarshal(line, &limited); err != nil {
				t.Fatal(err)
			}
			if limited.Error == nil || limited.Error.Code != protocol.CodeConflict || string(limited.ID) != "2" {
				t.Fatalf("invalid envelope beyond burst = %+v, want correlated rate limit", limited)
			}
			requireTransportConflict(t, client.Call("coord.hook.send", nil, nil))
			// An unidentifiable malformed request beyond the budget closes the
			// connection instead of providing an unbounded parse-error stream.
			if _, err := fmt.Fprintln(conn, "{"); err != nil {
				t.Fatal(err)
			}
			if _, err := protocol.ReadLine(reader); !errors.Is(err, io.EOF) {
				t.Fatalf("malformed request past burst: %v, want EOF", err)
			}
		})
	}
}

func TestHookStatusBudgetIsPerRunAndReleased(t *testing.T) {
	h := newHarness(t, 2)
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	for _, run := range []domain.RunID{a, b} {
		if _, err := h.svc.Provision(ctx, run, nil); err != nil {
			t.Fatal(err)
		}
	}
	client := h.dial(t, a)
	for i := range requestBurst {
		if err := client.Call(protocol.MethodCoordHookStatus, nil, nil); err != nil {
			t.Fatalf("hook %d: %v", i, err)
		}
	}
	requireTransportConflict(t, client.Call(protocol.MethodCoordHookStatus, nil, nil))
	if err := h.dial(t, b).Call(protocol.MethodCoordHookStatus, nil, nil); err != nil {
		t.Fatalf("another run's hook budget: %v", err)
	}
	if err := h.svc.Release(a); err != nil {
		t.Fatal(err)
	}
	h.svc.mu.Lock()
	_, retained := h.svc.hookBuckets[a]
	h.svc.mu.Unlock()
	if retained {
		t.Fatal("released run retained its hook bucket")
	}
	if _, err := h.svc.Provision(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.dial(t, a).Call(protocol.MethodCoordHookStatus, nil, nil); err != nil {
		t.Fatalf("reprovisioned run retained exhausted hook budget: %v", err)
	}
}

func TestHookStatusPreservesReadOnlyStatusAuthorization(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	h.svc.cfg.Mission = missionTransportStub{mission: []domain.RunID{a, b}}
	request := func(run domain.RunID, method string) protocol.Response {
		return h.svc.handle(ctx, run, []byte(`{"jsonrpc":"2.0","id":1,"method":"`+method+`"}`))
	}
	if _, err := h.svc.Send(ctx, b, sendParams(a, "pending mission message")); err != nil {
		t.Fatal(err)
	}
	hook := request(a, protocol.MethodCoordHookStatus)
	if hook.Error != nil {
		t.Fatal(hook.Error)
	}
	var status protocol.CoordStatusResult
	if err := json.Unmarshal(hook.Result, &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID != string(a) || status.Assignment == nil || status.Assignment.MissionID != "mission-1" ||
		len(status.Peers) != 1 || status.Peers[0].RunID != string(b) || status.Unread != 1 {
		t.Fatalf("hook lost authenticated mission context: %+v", status)
	}
	for _, tc := range []struct {
		name string
		run  domain.RunID
		code int
		set  func()
	}{
		{"unknown-run", "missing", protocol.CodeNotFound, func() {}},
		{"closing-run", a, protocol.CodeUnavailable, func() { h.svc.closeRun(a) }},
		{"disabled", b, protocol.CodeUnavailable, func() { h.svc.cfg.Disabled = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set()
			ordinary := request(tc.run, protocol.MethodCoordStatus)
			hook := request(tc.run, protocol.MethodCoordHookStatus)
			if hook.Error == nil || hook.Error.Code != tc.code || ordinary.Error == nil || ordinary.Error.Code != tc.code {
				t.Fatalf("authorization differs: ordinary %+v, hook %+v, want code %d", ordinary.Error, hook.Error, tc.code)
			}
		})
	}
}
