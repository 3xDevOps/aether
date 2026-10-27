//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// An Enhanced agent reports waiting until another run finishes; that run
// ends, and the server writes the outcome to the agent's inbox and starts
// its next turn.
func TestIntegrationRunWaitWakesTheAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env := startEnhancedServer(ctx, t)
	defer env.stop(t)
	ada, err := env.srv.Store().GetMemberByTailnetLogin(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	launch := func(task string) domain.RunID {
		t.Helper()
		var raw json.RawMessage
		params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(env.ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchACP)})
		if status := postJSON(t, env.web+"/api/v1/run.launch", string(params), &raw); status != http.StatusOK {
			t.Fatalf("run.launch status %d: %s", status, raw)
		}
		var launched protocol.RunResult
		if err := json.Unmarshal(raw, &launched); err != nil {
			t.Fatal(err)
		}
		return domain.RunID(launched.Run.ID)
	}
	owner, target := launch("integrate"), launch("build")
	waitTurns(ctx, t, env.srv, owner, 1)
	waitTurns(ctx, t, env.srv, target, 1)

	socket := waitMissionSocket(t, filepath.Join(env.data, "coord", string(owner)))
	until := "run:" + string(target) + ":finished"
	var refused error
	if refused = pacedCall(ctx, socket, protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, Summary: "waiting for the build", IdempotencyKey: "bad", Until: []string{"ci finishes"},
	}, nil); refused == nil || !strings.Contains(refused.Error(), protocol.WaitGrammar) {
		t.Fatalf("until \"ci finishes\": %v, want the grammar", refused)
	}
	var reported protocol.CoordReportResult
	if err := pacedCall(ctx, socket, protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, WaitingFor: protocol.CoordWaitingForAgents, Summary: "waiting for the build",
		IdempotencyKey: "wait", Until: []string{until},
	}, &reported); err != nil {
		t.Fatalf("coord.report waiting until: %v", err)
	}
	if !strings.Contains(reported.NextAction, until) {
		t.Fatalf("next action %q, want it to name %s", reported.NextAction, until)
	}

	var got protocol.RunResult
	if status := postJSON(t, env.web+"/api/v1/run.get", `{"run_id":"`+string(owner)+`"}`, &got); status != http.StatusOK {
		t.Fatalf("run.get status %d", status)
	}
	if len(got.Run.WaitConditions) != 1 || got.Run.WaitConditions[0].Spec != until || got.Run.WaitConditions[0].State != protocol.WaitPending {
		t.Fatalf("wait_conditions = %+v, want %s pending", got.Run.WaitConditions, until)
	}

	if err := env.srv.sched.Kill(ctx, target, ada.ID); err != nil {
		t.Fatalf("kill %s: %v", target, err)
	}
	waitTurns(ctx, t, env.srv, owner, 2)
	waitPrompted(ctx, t, env.srv, owner, protocol.CoordInboxContext(1))

	page, err := env.srv.Store().ListRunMessages(ctx, store.RunMessageFilter{WorkspaceID: env.ws.ID, RunID: owner, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if msgs := page.Items; len(msgs) != 1 || msgs[0].FromRun != owner || msgs[0].ToRun != owner ||
		!strings.Contains(msgs[0].Body, until+" resolved: abandoned") {
		for _, m := range msgs {
			t.Logf("message %s -> %s: %q", m.FromRun, m.ToRun, m.Body)
		}
		t.Fatalf("inbox has %d messages, want one to itself saying %s resolved abandoned", len(msgs), until)
	}
	if status := postJSON(t, env.web+"/api/v1/run.get", `{"run_id":"`+string(owner)+`"}`, &got); status != http.StatusOK {
		t.Fatalf("run.get status %d", status)
	}
	if w := got.Run.WaitConditions; len(w) != 1 || w[0].State != protocol.WaitResolved || w[0].Outcome != "abandoned" || w[0].DeliveredAt == "" {
		t.Fatalf("wait_conditions = %+v, want resolved abandoned and delivered", w)
	}
	if err := env.srv.sched.Kill(ctx, owner, ada.ID); err != nil {
		t.Fatalf("kill %s: %v", owner, err)
	}
}
