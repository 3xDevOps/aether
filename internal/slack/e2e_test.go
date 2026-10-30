package slack

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

type e2eRuns struct {
	sshd.RunController
	mu       sync.Mutex
	pending  map[domain.RunID][]acphost.Request
	answered []string
}

func (r *e2eRuns) set(run domain.RunID, requests ...acphost.Request) {
	r.mu.Lock()
	r.pending[run] = requests
	r.mu.Unlock()
}

func (r *e2eRuns) ACPPending(run domain.RunID) []acphost.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.pending[run])
}

func (r *e2eRuns) PendingInputs(run domain.RunID) []domain.RunInputRequest {
	var out []domain.RunInputRequest
	for _, p := range r.ACPPending(run) {
		out = append(out, domain.RunInputRequest{ID: p.ID, SessionID: "s1", Kind: p.Kind})
	}
	return out
}

func (r *e2eRuns) ACPAnswer(run domain.RunID, request, option string, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answered = append(r.answered, request+" "+option)
	r.pending[run] = nil
	return nil
}

func (r *e2eRuns) answers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.answered)
}

func (*e2eRuns) Paused(domain.RunID) bool                 { return false }
func (*e2eRuns) Switching(domain.RunID) domain.LaunchMode { return "" }
func (*e2eRuns) Retention(*domain.Run) (scheduler.RetentionInfo, error) {
	return scheduler.RetentionInfo{}, nil
}

type e2eRooms struct {
	sshd.RoomService
	mu    sync.Mutex
	posts []string
}

func (r *e2eRooms) Post(ctx context.Context, in collab.MessageInput) (collab.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, fmt.Sprintf("%s via %s: %s", in.ActorID, domain.ViaFrom(ctx), in.Body))
	return collab.Result{Message: &store.RoomMessage{ID: "m1", State: store.RoomMessageSent}}, nil
}

func (r *e2eRooms) posted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.posts)
}

type (
	noGit struct{ sshd.GitTransport }
	noPTY struct{ sshd.PTYAttacher }
)

// The bot over the real control server and push service: a request
// reaches its owner's DM with its once options as buttons, a press
// answers the agent as the member, the message clears, and a reply in
// the thread reaches the run as the member via Slack.
func TestARunNeedIsAnsweredFromSlack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	runs, rooms := &e2eRuns{pending: map[domain.RunID][]acphost.Request{}}, &e2eRooms{}
	srv, err := sshd.New(sshd.Config{Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(t.TempDir(), "host_ed25519_key"),
		Store: h.db, Bus: bus, Git: noGit{}, PTY: noPTY{}, Runs: runs, Name: "build-box", Services: sshd.Services{Rooms: rooms}})
	if err != nil {
		t.Fatalf("sshd.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	h.svc, err = New(Config{Store: h.db, AppToken: "xapp-test", BotToken: "xoxb-test", Name: "build-box",
		API: h.slack.srv.URL + "/api/", HTTP: h.slack.srv.Client(),
		Local: func(ctx context.Context, grant sshd.ConnectorGrant, id string) (Caller, error) {
			if checkErr := srv.CheckConnectorGrant(ctx, grant); checkErr != nil {
				return nil, checkErr
			}
			return srv.ConnectorLocal(grant, id, Client, Scopes), nil
		}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.svc.Close() })
	costs, err := cost.New(cost.Config{Store: h.db, Bus: bus})
	if err != nil {
		t.Fatalf("cost.New: %v", err)
	}
	notices, err := push.New(push.Config{Store: h.db, Bus: bus, Runs: runs, Budgets: costs,
		KeyPath: filepath.Join(t.TempDir(), "vapid_key.pem"), Quiet: time.Millisecond})
	if err != nil {
		t.Fatalf("push.New: %v", err)
	}
	notices.UseSink(h.svc)
	if err = notices.Start(ctx); err != nil {
		t.Fatalf("push Start: %v", err)
	}
	t.Cleanup(func() { _ = notices.Close() })
	h.start()

	code, err := h.svc.Code(sshd.ConnectorGrant{Member: h.ada.ID})
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	h.slack.send(eventEnvelope("env-link", "Ev1", dm("U1", code.Code)))
	if got := h.slack.waitPosts(1)[0].Text; !strings.Contains(got, "Linked to Ada on build-box") {
		t.Fatalf("link reply = %q", got)
	}

	run := &domain.Run{Title: "Clean the build", WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Harness: "claude",
		Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err = h.db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	runs.set(run.ID, acphost.Request{ID: "req1", Kind: "permission", Title: "Run rm -rf build?", Options: []acphost.Option{
		{ID: "opt-allow", Name: "Allow", Kind: "allow_once"},
		{ID: "opt-always", Name: "Always allow", Kind: "allow_always"},
		{ID: "opt-reject", Name: "Reject", Kind: "reject_once"},
	}})
	publish := func() {
		t.Helper()
		if _, err := bus.Publish(ctx, events.Event{WorkspaceID: run.WorkspaceID, RunID: run.ID,
			Payload: events.RunInputPayload{PendingInputs: runs.PendingInputs(run.ID)}}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	publish()
	need := h.slack.waitPosts(2)[1]
	l := layoutOf(t, need)
	if need.Channel != "D1" || len(l.buttons) != 2 || l.buttons[0].label != "Allow" || l.buttons[1].label != "Reject" ||
		!strings.Contains(l.sections[0], "Permission requested") || l.sections[1] != "Run rm -rf build?" {
		t.Fatalf("need message = %+v in %s, want Allow and Reject on the request", l, need.Channel)
	}
	var ts string
	eventually(t, "the message to be kept", func() bool {
		m, getErr := h.db.GetSlackMessage(ctx, run.ID)
		if getErr == nil {
			ts = m.TS
		}
		return getErr == nil
	})

	h.slack.send(pressEnvelope(ts, l.buttons[1].value))
	eventually(t, "the agent to get the answer", func() bool { return len(runs.answers()) == 1 })
	if got := runs.answers(); got[0] != "req1 opt-reject" {
		t.Fatalf("agent answered %q, want req1 opt-reject", got)
	}
	reply := h.slack.waitPosts(3)[2]
	if reply.ThreadTS != ts || reply.Text != "Answered: Reject." {
		t.Fatalf("press reply = %+v, want Answered: Reject. in the run's thread", reply)
	}
	publish()
	eventually(t, "the message to clear", func() bool {
		updates := h.slack.calls("chat.update")
		return len(updates) > 0 && strings.Contains(layoutOf(t, updates[len(updates)-1]).sections[0], "Nothing needs you now.")
	})

	h.slack.send(eventEnvelope("env-reply", "Ev2", threadReply("U1", "use eu-west", ts)))
	eventually(t, "the reply to reach the run", func() bool { return len(rooms.posted()) == 1 })
	if got, want := rooms.posted()[0], fmt.Sprintf("%s via Slack: use eu-west", h.ada.ID); got != want {
		t.Fatalf("run got %q, want %q", got, want)
	}
	if got := h.slack.waitPosts(4)[3]; got.ThreadTS != ts || got.Text != "Sent to the run." {
		t.Fatalf("reply answer = %+v", got)
	}
	if !h.slack.acked("env-link") || !h.slack.acked("env-press") || !h.slack.acked("env-reply") {
		t.Fatal("an envelope went unacknowledged")
	}
}
