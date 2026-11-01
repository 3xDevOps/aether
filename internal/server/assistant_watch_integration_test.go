//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// The assistant watches every run it launches: the watch is listed and
// stoppable like any other, and its notices reach the assistant
// conversation as well as the watch's own record.
func TestIntegrationAssistantWatchesRun(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	env := startAssistantServer(t, ctx, nil)
	defer func() { env.stop() }()
	store := env.srv.Store()

	ws := seedAssistantWorkspace(t, ctx, env, "follow")

	sub, err := env.srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	const task = "follow this run"
	release := make(chan struct{})
	env.rt.script(task, func(c *e2eContainer) {
		c.output("working\r\n")
		<-release
		c.exitNow(0)
	})

	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	frames := env.watch(ctx, 0)
	conn, err := net.Dial("unix", filepath.Join(scheduler.AssistantSocketDir(filepath.Join(env.data, "assistant"), env.member.ID), "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchHeadless)})
	req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: protocol.MethodRunLaunch, Params: params})
	if _, err = conn.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	pending := frames.until(t, "the launch confirmation", func(f protocol.AssistantFrame) bool {
		return f.Status != nil && len(f.Status.Confirmations) == 1
	})
	if perr := env.call(protocol.MethodAssistantConfirm, protocol.AssistantConfirmParams{ConfirmationID: pending.Status.Confirmations[0].ID, Approve: true}, nil); perr != nil {
		t.Fatalf("assistant.confirm: %v", perr)
	}
	line, err := protocol.ReadLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	var resp protocol.Response
	if err = json.Unmarshal(line, &resp); err != nil || resp.Error != nil {
		t.Fatalf("run.launch answered %s (%v)", line, err)
	}
	var launched protocol.RunResult
	if err = json.Unmarshal(resp.Result, &launched); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.RunID(launched.Run.ID), WorkspaceID: ws.ID}
	var watches protocol.WatchListResult
	if perr := env.call(protocol.MethodWatchList, protocol.WatchListParams{RunID: string(run.ID)}, &watches); perr != nil {
		t.Fatalf("watch.list: %v", perr)
	}
	if len(watches.Watches) != 1 || watches.Watches[0].Source != protocol.WatchSourceAssistant ||
		watches.Watches[0].State != protocol.WatchActive || strings.Join(watches.Watches[0].Conditions, ",") != "needs_you,finished" {
		t.Fatalf("watches after the launch = %+v, want one active assistant watch on needs_you and finished", watches.Watches)
	}
	watchID := watches.Watches[0].ID

	noticeOf := func(kind string) func(events.Event) bool {
		return func(e events.Event) bool {
			p, ok := e.Payload.(events.AssistantNoticePayload)
			return ok && e.RunID == run.ID && p.Kind == kind
		}
	}
	count := func(kind string) int {
		n := 0
		for _, e := range seen {
			if noticeOf(kind)(e) {
				n++
			}
		}
		return n
	}
	waitRunStatus(t, sub, &seen, string(run.ID), domain.RunRunning)

	// The watch outlives a restart of the server.
	_ = conn.Close()
	_ = sub.Close()
	env.stop()
	env = startAssistantServer(t, ctx, env)
	store = env.srv.Store()
	if sub, err = env.srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096}); err != nil {
		t.Fatal(err)
	}
	seen = nil

	if _, err = store.UpdateRunStatus(ctx, run.ID, domain.RunNeedsAttention, "needs input: pick a database", domain.ActivityNeedsYou, nil, nil); err != nil {
		t.Fatal(err)
	}
	publishStatus := func() {
		t.Helper()
		if _, err := env.srv.Bus().Publish(ctx, events.Event{WorkspaceID: ws.ID, RunID: run.ID, Payload: events.RunStatusPayload{To: domain.RunNeedsAttention, Reason: "needs input: pick a database"}}); err != nil {
			t.Fatal(err)
		}
	}
	publishStatus()
	attention := waitEvent(t, sub, &seen, "the needs-attention notice", noticeOf(events.AssistantNoticeNeedsAttention))
	if p := attention.Payload.(events.AssistantNoticePayload); attention.WorkspaceID != ws.ID || attention.ActorID != env.member.ID ||
		p.MemberID != env.member.ID || p.WatchID != watchID || !strings.Contains(p.Title, "needs your attention") || p.Body != "pick a database" {
		t.Fatalf("needs-attention notice = %+v on %+v", p, attention)
	}
	var got protocol.WatchGetResult
	if perr := env.call(protocol.MethodWatchGet, protocol.WatchIDParams{WatchID: watchID}, &got); perr != nil {
		t.Fatalf("watch.get: %v", perr)
	}
	if len(got.Notifications) != 1 || got.Notifications[0].Condition != protocol.WatchNeedsYou || got.Notifications[0].Body != "pick a database" {
		t.Fatalf("watch notifications = %+v, want the one need", got.Notifications)
	}
	statuses := map[string]string{}
	for _, d := range got.Notifications[0].Deliveries {
		statuses[d.Channel] = d.Status
	}
	// The owner has no push device, so nothing pushed the need; skipped is
	// only for a push the run's own notification already made.
	if statuses[protocol.WatchChannelInApp] != protocol.WatchStatusDelivered || statuses[protocol.WatchChannelAssistant] != protocol.WatchStatusDelivered ||
		statuses[protocol.WatchChannelPush] != protocol.WatchStatusNoDestination {
		t.Fatalf("deliveries = %+v, want in-app and assistant delivered, push no_destination", got.Notifications[0].Deliveries)
	}
	publishStatus()
	waitEvent(t, sub, &seen, "the second status event", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == run.ID && p.To == domain.RunNeedsAttention && e.Seq > attention.Seq
	})
	time.Sleep(500 * time.Millisecond)
	for len(sub.Events()) > 0 {
		seen = append(seen, <-sub.Events())
	}
	if n := count(events.AssistantNoticeNeedsAttention); n != 1 {
		t.Fatalf("%d needs-attention notices after the same state was published twice, want 1", n)
	}

	close(release)
	finished := waitEvent(t, sub, &seen, "the finished notice", noticeOf(events.AssistantNoticeFinished))
	if p := finished.Payload.(events.AssistantNoticePayload); !strings.Contains(p.Title, "finished") && !strings.Contains(p.Title, "failed") {
		t.Fatalf("finished notice = %+v", p)
	}
	if perr := env.call(protocol.MethodWatchGet, protocol.WatchIDParams{WatchID: watchID}, &got); perr != nil {
		t.Fatalf("watch.get: %v", perr)
	}
	if len(got.Notifications) != 2 || got.Notifications[0].Condition != protocol.WatchFinished {
		t.Fatalf("watch notifications after the run ended = %+v, want the finish on top of the need", got.Notifications)
	}

	var history protocol.RunACPHistoryResult
	if perr := env.call(protocol.MethodAssistantHistory, protocol.AssistantHistoryParams{}, &history); perr != nil {
		t.Fatal(perr)
	}
	attentionNotices, finishedNotices := 0, 0
	for _, f := range history.Frames {
		item := string(f.Item)
		if !strings.Contains(item, `"kind":"notice"`) {
			continue
		}
		if strings.Contains(item, "needs your attention") && strings.Contains(item, "pick a database") && strings.Contains(item, "watch="+watchID) {
			attentionNotices++
		}
		if strings.Contains(item, `Session \"`+task+`\"`) && (strings.Contains(item, "finished") || strings.Contains(item, "failed")) {
			finishedNotices++
		}
	}
	if attentionNotices != 1 || finishedNotices != 1 {
		t.Fatalf("transcript holds %d needs-attention and %d finished notices, want one each: %v", attentionNotices, finishedNotices, history.Frames)
	}
}

// seedAssistantWorkspace creates a workspace with one commit on main, pushed
// by a second admin.
func seedAssistantWorkspace(t *testing.T, ctx context.Context, env *assistantEnv, name string) *domain.Workspace {
	t.Helper()
	store := env.srv.Store()
	pusherPath, pusher := writeClientKey(t)
	if err := store.CreateMember(ctx, &domain.Member{DisplayName: "Pusher", PublicKey: string(ssh.MarshalAuthorizedKey(pusher.PublicKey())), Color: "#3cb44b", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	ws := &domain.Workspace{Name: name, BaseBranch: domain.DefaultBaseBranch}
	if err := store.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seed := t.TempDir()
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+pusherPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seed, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seed, gitEnv, "config", "user.name", "E2E")
	runGit(t, seed, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seed, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seed, "README.md"), "# "+name+"\n")
	runGit(t, seed, gitEnv, "add", "-A")
	runGit(t, seed, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seed, gitEnv, "push", "-q", fmt.Sprintf("ssh://aether@%s/%s.git", waitSSHAddr(t, env.srv), ws.ID), "main")
	return ws
}
