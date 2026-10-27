//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Enhanced agents report waiting --until from inside their turn. One waits
// on a run that then ends: the server writes the outcome to its inbox and
// starts exactly one more turn for it. The other waits on a run that
// already ended: its report returns the resolution and no turn follows.
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
	waitOn := func(target domain.RunID, key string) string {
		return acpmock.PromptRun + coordtransport.CLIPath + " report --outcome waiting --for agents --until run:" + string(target) +
			":finished --summary 'waiting for the build' --idempotency-key " + key
	}
	get := func(run domain.RunID) protocol.Run {
		t.Helper()
		var got protocol.RunResult
		if status := postJSON(t, env.web+"/api/v1/run.get", `{"run_id":"`+string(run)+`"}`, &got); status != http.StatusOK {
			t.Fatalf("run.get status %d", status)
		}
		return got.Run
	}
	inbox := func(run domain.RunID) []*store.ListedRunMessage {
		t.Helper()
		page, err := env.srv.Store().ListRunMessages(ctx, store.RunMessageFilter{WorkspaceID: env.ws.ID, RunID: run, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}

	ended := launch("say pong")
	waitTurns(ctx, t, env.srv, ended, 1)
	if err := env.srv.sched.Kill(ctx, ended, ada.ID); err != nil {
		t.Fatalf("kill %s: %v", ended, err)
	}
	target := launch("say pong")
	waitTurns(ctx, t, env.srv, target, 1)
	waiter, satisfied := launch(waitOn(target, "wait-1")), launch(waitOn(ended, "wait-2"))
	waitTurns(ctx, t, env.srv, waiter, 1)
	items := waitTurns(ctx, t, env.srv, satisfied, 1)
	if said := assistantText(items); !strings.Contains(said, `"resolved"`) || !strings.Contains(said, "is already resolved (abandoned)") {
		t.Fatalf("report output %q, want the resolution returned", said)
	}
	if w := get(waiter).WaitConditions; len(w) != 1 || w[0].State != protocol.WaitPending {
		t.Fatalf("waiter wait_conditions = %+v, want one pending", w)
	}

	if err := env.srv.sched.Kill(ctx, target, ada.ID); err != nil {
		t.Fatalf("kill %s: %v", target, err)
	}
	waitPrompted(ctx, t, env.srv, waiter, protocol.CoordInboxContext(1))
	for _, run := range []domain.RunID{waiter, satisfied} {
		quiesce(ctx, t, env.srv, run)
	}

	msgs := inbox(waiter)
	if len(msgs) != 1 || msgs[0].FromRun != waiter || msgs[0].ToRun != waiter ||
		!strings.Contains(msgs[0].Body, "run:"+string(target)+":finished resolved: abandoned") {
		for _, m := range msgs {
			t.Logf("message %s -> %s: %q", m.FromRun, m.ToRun, m.Body)
		}
		t.Fatalf("waiter inbox has %d messages, want one to itself saying the target was abandoned", len(msgs))
	}
	// Aether also asks each of the mock's turns for a report, as it does
	// after one that reported waiting without --until: a reminder turn
	// follows the task's turn and the wake's.
	if inboxes, reminders, turns := countPrompts(t, env.srv, waiter, protocol.CoordInboxContext(1)),
		countPrompts(t, env.srv, waiter, coordhooks.ReportReminderLead), turnsEnded(t, env.srv, waiter); inboxes != 1 || turns != 2+reminders {
		t.Fatalf("waiter: %d inbox prompts, %d reminders, %d turns; want one wake and no other turn", inboxes, reminders, turns)
	}
	if w := get(waiter).WaitConditions; len(w) != 1 || w[0].State != protocol.WaitResolved || w[0].DeliveredAt == "" {
		t.Fatalf("waiter wait_conditions = %+v, want resolved and delivered", w)
	}
	if reminders, turns := countPrompts(t, env.srv, satisfied, coordhooks.ReportReminderLead), turnsEnded(t, env.srv, satisfied); turns != 1+reminders ||
		len(inbox(satisfied)) != 0 || countPrompts(t, env.srv, satisfied, protocol.CoordInboxContext(1)) != 0 {
		t.Fatalf("satisfied run: %d turns, %d reminders, %d messages; want its one turn and no wake", turns, reminders, len(inbox(satisfied)))
	}
	if w := get(satisfied).WaitConditions; len(w) != 1 || w[0].State != protocol.WaitResolved || w[0].Outcome != "abandoned" || w[0].DeliveredAt == "" {
		t.Fatalf("satisfied wait_conditions = %+v, want resolved and returned", w)
	}
	for _, run := range []domain.RunID{waiter, satisfied} {
		if err := env.srv.sched.Kill(ctx, run, ada.ID); err != nil {
			t.Fatalf("kill %s: %v", run, err)
		}
	}
}

func assistantText(items []acphost.Item) string {
	var b strings.Builder
	for _, it := range items {
		if it.Kind == acphost.KindMessage && it.Message.Role == "assistant" {
			b.WriteString(it.Message.Text)
		}
	}
	return b.String()
}

func turnsEnded(t *testing.T, srv *Server, run domain.RunID) int {
	t.Helper()
	page, err := srv.sched.ACPHistory(run, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range page.Items {
		if it.Kind == acphost.KindTurnEnd {
			n++
		}
	}
	return n
}

func countPrompts(t *testing.T, srv *Server, run domain.RunID, instruction string) int {
	t.Helper()
	page, err := srv.sched.ACPHistory(run, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range page.Items {
		if it.Kind == acphost.KindMessage && it.Message.Role == "user" && strings.Contains(it.Message.Text, strings.TrimSpace(instruction)) {
			n++
		}
	}
	return n
}

// quiesce waits until run has ended every turn it started and started no
// other for five seconds.
func quiesce(ctx context.Context, t *testing.T, srv *Server, run domain.RunID) {
	t.Helper()
	last, since := -1, time.Now()
	for {
		page, err := srv.sched.ACPHistory(run, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		started, ended := 0, 0
		for _, it := range page.Items {
			switch it.Kind {
			case acphost.KindTurnStart:
				started++
			case acphost.KindTurnEnd:
				ended++
			}
		}
		if started != ended || started != last {
			last, since = started, time.Now()
		} else if time.Since(since) >= 5*time.Second {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s never went quiet: %d turns started, %d ended", run, started, ended)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
