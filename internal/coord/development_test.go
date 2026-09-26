package coord

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type developmentAuthorityStub struct {
	calls atomic.Int32
}

func (d *developmentAuthorityStub) Capabilities(context.Context, domain.RunID) ([]string, error) {
	return []string{protocol.MethodDevTerminalList, protocol.MethodDevTerminalList, "dev.terminal.unknown", protocol.MethodTaskAccept}, nil
}

func (d *developmentAuthorityStub) HandleAgent(context.Context, domain.RunID, string, json.RawMessage) (any, error) {
	d.calls.Add(1)
	return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "human holds control"}
}

type developmentMissionStub struct{ missionTransportStub }

func (developmentMissionStub) Assignment(context.Context, domain.RunID) (protocol.CoordMissionAssignment, error) {
	return protocol.CoordMissionAssignment{MissionID: "mission", Capabilities: []string{protocol.MethodCoordStatus, protocol.MethodTaskShow, protocol.MethodDevBrowserOpen, "run.git.push"}}, nil
}

func TestStatusCombinesIndependentDevelopmentAndMissionAuthority(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			dev := &developmentAuthorityStub{}
			h := newHarness(t, 1, func(c *Config) {
				c.Disabled, c.Development, c.Mission = disabled, dev, developmentMissionStub{}
			})
			status, err := h.svc.Status(t.Context(), h.run(0))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{protocol.MethodCoordStatus, protocol.MethodDevTerminalList}
			if !disabled {
				want = append(want, protocol.MethodTaskShow)
			}
			if !slices.Equal(status.Capabilities, want) {
				t.Fatalf("capabilities = %v, want %v", status.Capabilities, want)
			}
			if status.Assignment != nil && !slices.Equal(status.Assignment.Capabilities, []string{protocol.MethodCoordStatus, protocol.MethodTaskShow}) {
				t.Fatalf("mission advertised authority outside its allowlist: %v", status.Assignment.Capabilities)
			}
			if disabled && (status.Assignment != nil || len(status.Peers) != 0 || status.Unread != 0) {
				t.Fatalf("disabled status leaked mission/mailbox authority: %+v", status)
			}
		})
	}
}

func TestDevelopmentSocketRejectsIdentityAndLifetimeEscapes(t *testing.T) {
	dev := &developmentAuthorityStub{}
	h := newHarness(t, 2, func(c *Config) { c.Disabled, c.Development = true, dev })
	h.start()
	for _, run := range []domain.RunID{h.run(0), h.run(1)} {
		if _, err := h.svc.Provision(t.Context(), run, nil); err != nil {
			t.Fatal(err)
		}
	}
	client := h.dial(t, h.run(0))
	for _, params := range []any{
		map[string]string{"run_id": string(h.run(1))},
		map[string]string{"run_id": ""},
		map[string]string{"RUN_ID": string(h.run(1))},
		[]string{"not an object"},
		json.RawMessage(`null`),
	} {
		err := client.Call(protocol.MethodDevTerminalList, params, nil)
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
			t.Fatalf("identity override %v = %v, want invalid params", params, err)
		}
	}
	for _, method := range []string{"dev.terminal.unknown", "run.git.push", "workspace.import"} {
		err := client.Call(method, nil, nil)
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeMethodNotFound {
			t.Fatalf("unallowlisted method %s = %v", method, err)
		}
	}
	if dev.calls.Load() != 0 {
		t.Fatal("rejected request reached development authority")
	}
	// Disabled peer policy must not hide the development service's own control denial.
	err := client.Call(protocol.MethodDevTerminalList, nil, nil)
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeDenied || dev.calls.Load() != 1 {
		t.Fatalf("development control refusal = %v, calls=%d", err, dev.calls.Load())
	}
	if updateErr := h.db.UpdateRunStatus(t.Context(), h.run(0), domain.RunCompleted, "", nil, nil); updateErr != nil {
		t.Fatal(updateErr)
	}
	err = client.Call(protocol.MethodDevTerminalList, nil, nil)
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeUnavailable || dev.calls.Load() != 1 {
		t.Fatalf("finished run dispatch = %v, calls=%d", err, dev.calls.Load())
	}
	status, statusErr := h.svc.Status(t.Context(), h.run(0))
	if statusErr != nil || slices.Contains(status.Capabilities, protocol.MethodDevTerminalList) {
		t.Fatalf("finished run capabilities = %+v, %v", status, statusErr)
	}
	if _, err := h.svc.Status(t.Context(), "unknown-run"); err == nil || err.Code != protocol.CodeNotFound {
		t.Fatalf("unknown identity = %v", err)
	}
}

func TestUnconfiguredDevelopmentIsNotAdvertisedOrDispatched(t *testing.T) {
	h := newHarness(t, 1, func(c *Config) { c.Disabled = true })
	h.start()
	if _, err := h.svc.Provision(t.Context(), h.run(0), nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, h.run(0))
	var status protocol.CoordStatusResult
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(status.Capabilities, []string{protocol.MethodCoordStatus}) {
		t.Fatalf("unconfigured capabilities = %v", status.Capabilities)
	}
	err := client.Call(protocol.MethodDevTerminalList, nil, nil)
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeMethodNotFound {
		t.Fatalf("unconfigured development = %v", err)
	}
}

type developmentResultStub struct {
	developmentAuthorityStub
	result atomic.Value
}

func (d *developmentResultStub) HandleAgent(context.Context, domain.RunID, string, json.RawMessage) (any, error) {
	return d.result.Load(), nil
}

func TestDevelopmentSocketBoundsSerializedResults(t *testing.T) {
	dev := &developmentResultStub{}
	h := newHarness(t, 1, func(c *Config) { c.Disabled, c.Development = true, dev })
	h.start()
	if _, err := h.svc.Provision(t.Context(), h.run(0), nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, h.run(0))
	// Quotes count toward the serialized result budget.
	dev.result.Store(strings.Repeat("x", protocol.MaxDevResultBytes-2))
	var result string
	if err := client.Call(protocol.MethodDevTerminalList, nil, &result); err != nil {
		t.Fatalf("result at limit: %v", err)
	}
	if len(result) != protocol.MaxDevResultBytes-2 {
		t.Fatal("result at limit was silently truncated")
	}
	dev.result.Store(strings.Repeat("x", protocol.MaxDevResultBytes-1))
	err := client.Call(protocol.MethodDevTerminalList, nil, nil)
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInternal {
		t.Fatalf("oversized result = %v, want explicit failure", err)
	}
	var status protocol.CoordStatusResult
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil || status.RunID != string(h.run(0)) {
		t.Fatalf("socket unusable after oversized result: %+v, %v", status, err)
	}
}
