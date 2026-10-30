package ops

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var testWatch = protocol.Watch{ID: "w-1", Target: protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: "run-1"}, TargetTitle: injection,
	Conditions: []string{protocol.WatchNeedsYou, protocol.WatchFinished}, State: protocol.WatchActive, Source: protocol.WatchSourceMCP,
	ExpiresAt: "2026-11-09T10:00:00Z", Observed: protocol.WatchObserved{Summary: "Working"}, Link: "/?page=settings&id=watches&watch=w-1"}

const testReceipt = "Watching run Fix login for: needs you, finished. Push: 2 devices. Expires Nov 9."

// A create names its target from the one id it is given, says it came from
// the connector, and returns the watch's state and the server's receipt;
// a retry sends the same keyless request, which the server answers with the
// live watch it already has.
func TestWatchCreate(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodWatchCreate] = returns(protocol.WatchResult{Watch: testWatch, Receipt: testReceipt})
	cs := connectorSession(t, f, ScopeWrite)
	args := map[string]any{"run_id": "run:run-1", "conditions": []string{"needs_you", "finished"}}
	text, out := run(t, cs, "aether_watch", args)
	if text != "Watch w-1 is active." || out["watch_id"] != "w-1" || out["state"] != "active" || out["write"] != writeDone ||
		out["url"] != "https://aether.example/?page=settings&id=watches&watch=w-1" {
		t.Fatalf("create = %q %v", text, out)
	}
	if u := out["untrusted"].(map[string]any); u["receipt"] != testReceipt || u["target_title"] != injection || strings.Contains(trusted(t, out), injection) {
		t.Fatalf("untrusted = %v, want the receipt and title there only", u)
	}
	acted := f.acted()
	if want := `{"target":{"kind":"run","run_id":"run-1"},"conditions":["needs_you","finished"],"source":"mcp"}`; acted.method != protocol.MethodWatchCreate ||
		acted.scope != ScopeWrite || acted.params != want {
		t.Fatalf("acted = %+v, want %s", acted, want)
	}
	f.calls = nil
	run(t, cs, "aether_watch", args)
	if again := f.acted(); again.params != acted.params {
		t.Fatalf("retry sent %s, want %s", again.params, acted.params)
	}

	for in, want := range map[string]string{
		"acme/shop#7":                                          `{"kind":"pr","repo":"acme/shop","number":7}`,
		"https://github.com/acme/shop/pull/7":                  `{"kind":"pr","repo":"acme/shop","number":7}`,
		"https://github.com/acme/shop/pull/7#issuecomment-123": `{"kind":"pr","repo":"acme/shop","number":7}`,
		"https://github.com/acme/shop/pull/7/files?w=1":        `{"kind":"pr","repo":"acme/shop","number":7}`,
		"github.com/acme/shop/pull/7?foo":                      `{"kind":"pr","repo":"acme/shop","number":7}`,
	} {
		f.calls = nil
		run(t, cs, "aether_watch", map[string]any{"pr": in})
		if got := f.acted().params; !strings.Contains(got, want) {
			t.Errorf("pr %q sent %s, want target %s", in, got, want)
		}
	}
	f.calls = nil
	run(t, cs, "aether_watch", map[string]any{"swarm_id": "swarm:mis-1"})
	if got := f.acted().params; !strings.Contains(got, `{"kind":"mission","mission_id":"mis-1"}`) {
		t.Errorf("swarm watch sent %s", got)
	}

	for _, args := range []map[string]any{{}, {"pr": "acme#7"}, {"pr": "https://github.com/acme/shop/issues/7"}, {"run_id": "run-1", "paused": true},
		{"watch_id": "w-1", "run_id": "run-2"}} {
		if text, _, failed := try(t, cs, "aether_watch", args); !failed {
			t.Errorf("%v = %q, want refused", args, text)
		}
	}

	// Two targets are refused by name rather than one silently winning;
	// run_id with pr names the run that opened it.
	f.calls = nil
	for refusal, args := range map[string]map[string]any{
		"pass one target, not run_id and swarm_id":        {"run_id": "run-1", "swarm_id": "mis-1"},
		"pass one target, not pr and swarm_id":            {"pr": "acme/shop#7", "swarm_id": "mis-1"},
		"pass one target, not pr and run_id and swarm_id": {"pr": "acme/shop#7", "run_id": "run-1", "swarm_id": "mis-1"},
	} {
		if text, _, failed := try(t, cs, "aether_watch", args); !failed || !strings.HasPrefix(text, refusal) {
			t.Errorf("%v = %q, want %q", args, text, refusal)
		}
	}
	if len(called(f, protocol.MethodWatchCreate)) != 0 {
		t.Fatalf("an ambiguous target reached the server: %+v", f.calls)
	}
	run(t, cs, "aether_watch", map[string]any{"pr": "acme/shop#7", "run_id": "run-1"})
	if got := f.acted().params; !strings.Contains(got, `{"kind":"pr","run_id":"run-1","repo":"acme/shop","number":7}`) {
		t.Errorf("pr with its run sent %s", got)
	}

	g := newFake(t)
	g.answers[protocol.MethodWatchCreate] = returns(protocol.WatchResult{Watch: testWatch, Receipt: testReceipt})
	run(t, assistantSession(t, g.call), "aether_watch", map[string]any{"run_id": "run-1"})
	if got := g.acted().params; !strings.Contains(got, `"source":"assistant"`) {
		t.Fatalf("assistant create sent %s, want source assistant", got)
	}

	// Without a dashboard URL the assistant still gets the watch's link,
	// relative to the dashboard it runs in; a connector gets none.
	g.answers[protocol.MethodServerInfo] = returns(protocol.ServerInfoResult{})
	if _, out := run(t, assistantSession(t, g.call), "aether_watch", map[string]any{"run_id": "run-1"}); out["url"] != testWatch.Link {
		t.Fatalf("assistant url without a dashboard URL = %v, want %s", out["url"], testWatch.Link)
	}
	if _, out := run(t, connectorSession(t, g, ScopeWrite), "aether_watch", map[string]any{"run_id": "run-1"}); out["url"] != "" {
		t.Fatalf("connector url without a dashboard URL = %v, want none", out["url"])
	}
}

// The server's refusal of a condition it does not support reaches the
// model word for word, with the conditions to use instead.
func TestWatchUnsupportedCondition(t *testing.T) {
	f := newFake(t)
	refusal := `Aether has no deploy signal, so it cannot watch for "deployed"; watch finished or pr_merged instead`
	f.errs[protocol.MethodWatchCreate] = &protocol.Error{Code: protocol.CodeInvalidParams, Message: refusal}
	text, _, failed := try(t, connectorSession(t, f, ScopeWrite), "aether_watch", map[string]any{"run_id": "run-1", "conditions": []string{"deployed"}})
	if !failed || !strings.HasPrefix(text, refusal) {
		t.Fatalf("deployed = %q, want the server's refusal", text)
	}
}

func TestWatchUpdate(t *testing.T) {
	f := newFake(t)
	paused := testWatch
	paused.State = protocol.WatchPaused
	f.answers[protocol.MethodWatchUpdate] = returns(protocol.WatchResult{Watch: paused, Receipt: "Paused: run Fix login."})
	text, out := run(t, connectorSession(t, f, ScopeWrite), "aether_watch", map[string]any{"watch_id": "w-1", "paused": false})
	if text != "Watch w-1 is paused." || out["state"] != protocol.WatchPaused {
		t.Fatalf("update = %q %v", text, out)
	}
	if got := f.acted(); got.method != protocol.MethodWatchUpdate || got.params != `{"watch_id":"w-1","paused":false}` {
		t.Fatalf("acted = %+v, want paused false sent", got)
	}
}

// The list and one watch's notices are bounded, and what agents and GitHub
// wrote stays under untrusted.
func TestWatchesAreBounded(t *testing.T) {
	f := newFake(t)
	var watches []protocol.Watch
	for i := range maxWatches + 10 {
		w := testWatch
		w.ID = fmt.Sprintf("w-%d", i)
		watches = append(watches, w)
	}
	var notices []protocol.WatchNotification
	for i := range maxNotices + 5 {
		notices = append(notices, protocol.WatchNotification{ID: fmt.Sprintf("n-%d", i), WatchID: "w-1", Condition: protocol.WatchNeedsYou,
			Title: injection, Body: injection, Link: "/?run=run-1", CreatedAt: "2026-10-27T10:00:00Z",
			Deliveries: []protocol.WatchDelivery{{Channel: protocol.WatchChannelPush, Status: protocol.WatchStatusNoDestination, At: "2026-10-27T10:00:00Z"}}})
	}
	f.answers[protocol.MethodWatchList] = returns(protocol.WatchListResult{Watches: watches})
	f.answers[protocol.MethodWatchGet] = func(raw json.RawMessage) (any, error) {
		var p protocol.WatchIDParams
		if err := json.Unmarshal(raw, &p); err != nil || p.WatchID != "w-1" {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "watch not found"}
		}
		return protocol.WatchGetResult{Watch: testWatch, Notifications: notices}, nil
	}
	cs := connectorSession(t, f)

	text, out := run(t, cs, "aether_watches", nil)
	if rows := out["watches"].([]any); text != "The newest 50 of 60 watches; the rest are listed in the dashboard at https://aether.example/?page=settings&id=watches." || len(rows) != maxWatches || out["more"] != float64(10) {
		t.Fatalf("list = %q, %d rows, more %v", text, len(rows), out["more"])
	}
	if strings.Contains(trusted(t, out), injection) {
		t.Fatalf("list carries agent text outside untrusted: %s", trusted(t, out))
	}

	text, out = run(t, cs, "aether_watches", map[string]any{"watch_id": "w-1"})
	got := out["notifications"].([]any)
	if text != "Watch w-1 is active; 15 notices, newest first." || len(got) != maxNotices || out["more_notifications"] != float64(5) {
		t.Fatalf("get = %q, %d notices, more %v", text, len(got), out["more_notifications"])
	}
	first := got[0].(map[string]any)
	if first["url"] != "https://aether.example/?run=run-1" || first["deliveries"].([]any)[0].(map[string]any)["status"] != protocol.WatchStatusNoDestination ||
		strings.Contains(trusted(t, out), injection) {
		t.Fatalf("notice = %v", first)
	}
	if text, _, failed := try(t, cs, "aether_watches", map[string]any{"watch_id": "w-9"}); !failed || !strings.HasSuffix(text, nextWatches) {
		t.Fatalf("unknown watch = %q, want not found with the way to ids", text)
	}
}

func TestUnwatch(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodWatchDelete] = returns(struct{}{})
	text, out := run(t, connectorSession(t, f, ScopeWrite), "aether_unwatch", map[string]any{"watch_id": "w-1"})
	if text != "Deleted watch w-1." || out["state"] != "deleted" || out["write"] != writeDone {
		t.Fatalf("unwatch = %q %v", text, out)
	}
	if got := f.acted(); got.method != protocol.MethodWatchDelete || got.scope != ScopeWrite || got.params != `{"watch_id":"w-1"}` {
		t.Fatalf("acted = %+v", got)
	}

	// A retry after the delete went through succeeds, saying so.
	g := newFake(t)
	g.errs[protocol.MethodWatchDelete] = &protocol.Error{Code: protocol.CodeNotFound, Message: "store: watch w-1: not found"}
	text, out = run(t, connectorSession(t, g, ScopeWrite), "aether_unwatch", map[string]any{"watch_id": "w-1"})
	if text != "Watch w-1 is already gone; nothing was deleted." || out["state"] != "not_found" {
		t.Fatalf("repeated unwatch = %q %v", text, out)
	}
	h := newFake(t)
	h.errs[protocol.MethodWatchDelete] = &protocol.Error{Code: protocol.CodeUnavailable, Message: "watch service not configured"}
	if text, _, failed := try(t, connectorSession(t, h, ScopeWrite), "aether_unwatch", map[string]any{"watch_id": "w-1"}); !failed {
		t.Fatalf("unwatch on a failing server = %q, want the failure", text)
	}
}

// watch.get returns at most WatchNotificationsMax notices, so a full page
// is a lower bound, not a total.
func TestWatchNoticesAtTheCap(t *testing.T) {
	f := newFake(t)
	notices := make([]protocol.WatchNotification, protocol.WatchNotificationsMax)
	for i := range notices {
		notices[i] = protocol.WatchNotification{ID: fmt.Sprintf("n-%d", i), Deliveries: []protocol.WatchDelivery{}}
	}
	f.answers[protocol.MethodWatchGet] = returns(protocol.WatchGetResult{Watch: testWatch, Notifications: notices})
	text, out := run(t, connectorSession(t, f), "aether_watches", map[string]any{"watch_id": "w-1"})
	if text != "Watch w-1 is active; at least 50 notices, newest first." || out["more_notifications_capped"] != true {
		t.Fatalf("get at the cap = %q %v", text, out)
	}
}
