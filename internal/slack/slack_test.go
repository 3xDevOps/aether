package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

var bg = context.Background()

func TestLinkOverSocketMode(t *testing.T) {
	h := newHarness(t)
	h.start()

	res, err := h.svc.Code(sshd.ConnectorGrant{Member: h.ada.ID})
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	typed := strings.ToLower(strings.ReplaceAll(res.Code, "-", ""))
	h.slack.send(eventEnvelope("env-link", "Ev1", dm("U1", typed)))

	posts := h.slack.waitPosts(1)
	if posts[0].Channel != "D1" || !strings.Contains(posts[0].Text, "Linked to Ada on build-box") {
		t.Fatalf("link reply = %+v, want a post to D1 containing %q", posts[0], "Linked to Ada on build-box")
	}
	eventually(t, "the envelope ack", func() bool { return h.slack.acked("env-link") })
	link, err := h.db.GetSlackLink(bg, h.ada.ID)
	if err != nil || link.UserID != "U1" || link.Channel != "D1" || link.TeamID != "T1" {
		t.Fatalf("GetSlackLink = %+v, %v; want U1 in D1 of T1", link, err)
	}
	st, err := h.svc.Status(bg, h.ada.ID)
	if err != nil || st.Link == nil || st.Link.UserID != "U1" || !st.Connected || st.Bot != "aether" || st.Team != "Acme" {
		t.Fatalf("Status = %+v, %v; want connected bot aether in Acme, linked to U1", st, err)
	}

	h.slack.send(eventEnvelope("env-reuse", "Ev2", dm("U1", typed)))
	posts = h.slack.waitPosts(2)
	if !strings.Contains(posts[1].Text, "That code is wrong, expired or already used") {
		t.Fatalf("reused code reply = %q", posts[1].Text)
	}
}

func TestDirectMessagesFromStrangers(t *testing.T) {
	h := newHarness(t)
	for _, ev := range []map[string]any{
		{"type": "message", "channel_type": "im", "channel": "D1", "user": "U2", "bot_id": "B1", "text": "hi", "ts": "1.0"},
		{"type": "message", "subtype": "message_changed", "channel_type": "im", "channel": "D1", "user": "U2", "text": "hi", "ts": "1.0"},
	} {
		h.svc.handle(bg, eventEnvelope("e", "Ev", ev))
	}
	if posts := h.slack.posts(); len(posts) != 0 {
		t.Fatalf("bot and subtype messages were answered: %+v", posts)
	}

	h.svc.handle(bg, eventEnvelope("e", "Ev3", dm("U2", "hello")))
	posts := h.slack.posts()
	if len(posts) != 1 || !strings.Contains(posts[0].Text, "aether slack link") {
		t.Fatalf("stranger's DM reply = %+v, want the link instructions", posts)
	}
}

func TestReconnectsAfterDisconnect(t *testing.T) {
	h := newHarness(t)
	h.start()

	h.slack.send(map[string]string{"type": "disconnect", "reason": "refresh_requested"})
	eventually(t, "a second connection", func() bool {
		opens, sockets := h.slack.connections()
		return opens == 2 && sockets == 2
	})
	h.connected()

	h.slack.send(eventEnvelope("env-after", "Ev1", dm("U2", "hello")))
	eventually(t, "the envelope ack on the new connection", func() bool { return h.slack.acked("env-after") })
}

func TestNeedShowsButtonsThenEditsAndClears(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.local.setItems(permission(run, "Run rm -rf build?"))

	h.need(run, push.Need{Member: run.MemberID, Key: "k1", Body: "Permission requested", Answer: "req1"})
	h.need(run, push.Need{Member: run.MemberID, Key: "k1", Body: "Permission requested", Answer: "req1"})
	posts := h.slack.posts()
	if len(posts) != 1 || len(h.slack.calls("chat.update")) != 0 {
		t.Fatalf("the same need twice made %d posts and %d updates, want 1 and 0", len(posts), len(h.slack.calls("chat.update")))
	}
	l := layoutOf(t, posts[0])
	if posts[0].Channel != "D1" || len(l.sections) != 2 || !strings.Contains(l.sections[0], "Fix auth") || l.sections[1] != "Run rm -rf build?" {
		t.Fatalf("message = %+v %+v, want the title and the request", posts[0], l)
	}
	if want := fmt.Sprintf("<https://box.tail.ts.net/?run=%s|Open in Aether>", url.QueryEscape(string(run.ID))); !strings.Contains(l.context, want) {
		t.Fatalf("context = %q, want it to contain %q", l.context, want)
	}
	if len(l.buttons) != 2 || l.buttons[0].label != "Allow" || l.buttons[1].label != "Reject" {
		t.Fatalf("buttons = %+v, want Allow and Reject and never Always allow", l.buttons)
	}
	for i, option := range []string{"opt-allow", "opt-reject"} {
		var a answer
		if err := json.Unmarshal([]byte(l.buttons[i].value), &a); err != nil || a != (answer{string(run.ID), "req1", option}) {
			t.Fatalf("button %d value = %q (%v), want run, req1, %s", i, l.buttons[i].value, err, option)
		}
	}
	first := h.message(run)
	if first.NeedKey != "k1" || first.Channel != "D1" {
		t.Fatalf("stored message = %+v, want need k1 in D1", first)
	}

	h.need(run, push.Need{Member: run.MemberID, Key: "k2", Body: "Still waiting", Answer: "req1"})
	updates, posts := h.slack.calls("chat.update"), h.slack.posts()
	if len(updates) != 1 || updates[0].TS != first.TS || len(posts) != 2 || posts[1].ThreadTS != first.TS || !strings.Contains(posts[1].Text, "Still waiting") {
		t.Fatalf("a changed need made updates %+v and posts %+v, want an edit of %s and a thread reply", updates, posts, first.TS)
	}
	if got := h.message(run).NeedKey; got != "k2" {
		t.Fatalf("NeedKey = %q, want k2", got)
	}

	h.clear(run)
	h.clear(run)
	updates = h.slack.calls("chat.update")
	if len(updates) != 2 {
		t.Fatalf("clearing twice made %d updates in all, want 2", len(updates))
	}
	cleared := layoutOf(t, updates[1])
	if updates[1].TS != first.TS || !strings.Contains(cleared.sections[0], "Nothing needs you now.") || cleared.actions != 0 {
		t.Fatalf("cleared message = %+v %+v, want no need and no buttons", updates[1], cleared)
	}
	if got := h.message(run).NeedKey; got != "" {
		t.Fatalf("NeedKey after Clear = %q, want empty", got)
	}

	h.slack.refuseNext("chat.update", "message_not_found")
	h.need(run, push.Need{Member: run.MemberID, Key: "k3", Body: "Back again", Answer: "req1"})
	posts = h.slack.posts()
	if len(posts) != 3 || len(layoutOf(t, posts[2]).buttons) != 2 {
		t.Fatalf("after message_not_found posts = %+v, want a new message with buttons", posts)
	}
	if m := h.message(run); m.NeedKey != "k3" || m.TS == first.TS {
		t.Fatalf("stored message = %+v, want need k3 at a new ts", m)
	}
}

func TestNeedWithoutButtons(t *testing.T) {
	h := newHarness(t)
	h.link()
	long, form, escaped := h.newRun("Long"), h.newRun("Form"), h.newRun("Escaped")
	// JSON spells each < in six bytes, so this value outgrows Slack's limit.
	wide := permission(escaped, "Run it?")
	wide.Request.Options[0].ID = strings.Repeat("<", 400)
	h.local.setItems(
		permission(long, strings.Repeat("x", 700)),
		protocol.AttentionItem{Kind: protocol.AttentionKindQuestion, RunID: string(form.ID), Request: &protocol.AttentionRequest{
			ID: "q1", Kind: protocol.AttentionKindQuestion, Title: "Name?", Schema: json.RawMessage(`{"type":"object"}`),
			Options: []protocol.AttentionOption{{ID: "accept", Label: "Accept"}}}},
		wide,
	)

	h.need(long, push.Need{Member: long.MemberID, Key: "k", Body: "Permission requested", Answer: "req1"})
	h.need(form, push.Need{Member: form.MemberID, Key: "k", Body: "Question", Answer: "q1"})
	h.need(escaped, push.Need{Member: escaped.MemberID, Key: "k", Body: "Permission requested", Answer: "req1"})
	posts := h.slack.waitPosts(3)
	if l := layoutOf(t, posts[2]); l.actions != 0 {
		t.Fatalf("a request whose button value outgrows Slack's limit = %+v, want no buttons", l)
	}
	l := layoutOf(t, posts[0])
	if l.actions != 0 || !strings.Contains(l.context, "longer than Slack shows") {
		t.Fatalf("long request = %+v, want no buttons and a note that it is longer than Slack shows", l)
	}
	if got := len([]rune(l.sections[1])); got > maxRequest+1 {
		t.Fatalf("shown request has %d characters, want at most %d", got, maxRequest+1)
	}
	l = layoutOf(t, posts[1])
	if l.actions != 0 || !strings.Contains(l.context, "Answer it in the Aether dashboard") {
		t.Fatalf("question with a schema = %+v, want no buttons and a pointer to the dashboard", l)
	}
}

func TestASwarmNeedReachesItsHumanWithTheSwarmLink(t *testing.T) {
	h := newHarness(t)
	h.link()
	bob := &domain.Member{DisplayName: "Bob", TailnetLogin: "bob@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := h.db.CreateMember(bg, bob); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	run := &domain.Run{Title: "Ship the checkout", WorkspaceID: h.ws.ID, MemberID: bob.ID, Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(bg, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	h.need(run, push.Need{Member: h.ada.ID, Mission: "mis-1", Key: "k", Body: "The integrator asks: Which branch?"})
	posts := h.slack.posts()
	if len(posts) != 1 || posts[0].Channel != "D1" {
		t.Fatalf("posts = %+v, want one in Ada's D1", posts)
	}
	if l := layoutOf(t, posts[0]); !strings.Contains(l.context, "<https://box.tail.ts.net/?page=missions&id=mis-1|Open in Aether>") {
		t.Fatalf("context = %q, want the swarm's link", l.context)
	}
}

func TestAgentTextIsEscaped(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("<!channel> & <https://evil|x>")

	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "ping <!here> & more"})
	posts := h.slack.posts()
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	section := layoutOf(t, posts[0]).sections[0]
	for _, got := range []string{section, posts[0].Text} {
		if !strings.Contains(got, "&lt;!channel&gt; &amp; &lt;https://evil|x&gt;") || !strings.Contains(got, "&lt;!here&gt; &amp; more") ||
			strings.Contains(got, "<!") || strings.Contains(got, "<https") {
			t.Fatalf("%q does not escape the agent's text", got)
		}
	}
}

func TestThreadReplyReachesRun(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	ts := h.message(run).TS

	h.svc.handle(bg, eventEnvelope("e1", "Ev9", threadReply("U1", "a &lt;b&gt; &amp; c", ts)))
	injects := h.local.calls(protocol.MethodRunInject)
	var p protocol.RunInjectParams
	if len(injects) != 1 || json.Unmarshal(injects[0], &p) != nil || p.RunID != string(run.ID) || p.Message != "a <b> & c" || p.IdempotencyKey != "slack-Ev9" {
		t.Fatalf("run.inject calls = %s, want one for %s with message %q and key slack-Ev9", injects, run.ID, "a <b> & c")
	}
	posts := h.slack.posts()
	if last := posts[len(posts)-1]; last.ThreadTS != ts || last.Text != "Sent to the run." {
		t.Fatalf("reply = %+v, want %q in the run's thread", last, "Sent to the run.")
	}

	h.svc.handle(bg, eventEnvelope("e2", "Ev10", threadReply("U2", "hello", ts)))
	posts = h.slack.posts()
	if last := posts[len(posts)-1]; !strings.Contains(last.Text, "not linked") || len(h.local.calls(protocol.MethodRunInject)) != 1 {
		t.Fatalf("unlinked user: reply %q, %d injects; want the not-linked message and no new inject", last.Text, len(h.local.calls(protocol.MethodRunInject)))
	}
}

func TestRevokedGrantStopsActions(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	ts := h.message(run).TS
	h.local.setErr(errors.New("device revoked"))
	other := h.newRun("Other work")
	if err := h.svc.Need(bg, other, push.Need{Member: other.MemberID, Key: "k", Body: "Needs you"}); err != nil {
		t.Fatalf("Need with a revoked link = %v, want nil: trying again cannot deliver it", err)
	}
	if _, err := h.db.GetSlackMessage(bg, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("message for a revoked link: %v, want none", err)
	}

	h.svc.handle(bg, eventEnvelope("e1", "Ev1", threadReply("U1", "go on", ts)))
	posts := h.slack.posts()
	if last := posts[len(posts)-1]; !strings.Contains(last.Text, "your Slack link no longer works") || !strings.Contains(last.Text, "device revoked") {
		t.Fatalf("reply = %q, want the revoked-link message with its cause", last.Text)
	}
	if n := len(h.local.calls(protocol.MethodRunInject)); n != 0 {
		t.Fatalf("%d run.inject calls after the grant was revoked, want 0", n)
	}
}

func TestButtonPress(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.local.setItems(permission(run, "Run rm -rf build?"))
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Permission requested", Answer: "req1"})
	ts, buttons := h.message(run).TS, layoutOf(t, h.slack.posts()[0]).buttons
	reply := func() slackCall {
		t.Helper()
		posts := h.slack.posts()
		return posts[len(posts)-1]
	}

	h.svc.handle(bg, pressEnvelope(ts, buttons[1].value))
	var respond protocol.RunInputRespondParams
	calls := h.local.calls(protocol.MethodRunInputRespond)
	if len(calls) != 1 || json.Unmarshal(calls[0], &respond) != nil || respond.OptionID != "opt-reject" || respond.RequestID != "req1" || respond.RunID != string(run.ID) {
		t.Fatalf("run.input.respond calls = %s, want opt-reject for req1", calls)
	}
	if got := reply(); got.ThreadTS != ts || got.Text != "Answered: Reject." {
		t.Fatalf("reply = %+v, want %q in the message's thread", got, "Answered: Reject.")
	}

	forged, _ := json.Marshal(answer{string(run.ID), "req1", "opt-always"})
	h.svc.handle(bg, pressEnvelope(ts, string(forged)))
	if n := len(h.local.calls(protocol.MethodRunInputRespond)); n != 1 {
		t.Fatalf("a forged always option made %d run.input.respond calls in all, want 1", n)
	}
	if got := reply(); !strings.Contains(got.Text, "The request changed") {
		t.Fatalf("reply to a forged option = %q", got.Text)
	}

	h.local.setItems()
	h.svc.handle(bg, pressEnvelope(ts, buttons[0].value))
	if got := reply(); got.Text != "Already answered, or no longer pending." {
		t.Fatalf("reply to a stale button = %q", got.Text)
	}
	if n := len(h.local.calls(protocol.MethodRunInputRespond)); n != 1 {
		t.Fatalf("a stale button made %d run.input.respond calls in all, want 1", n)
	}
}

func TestActionsAreRateLimited(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	ts := h.message(run).TS
	frozen := time.Now()
	h.svc.now = func() time.Time { return frozen }

	for i := range writeBurst + 1 {
		h.svc.handle(bg, eventEnvelope("e", fmt.Sprintf("Ev%d", i), threadReply("U1", "more", ts)))
	}
	if n := len(h.local.calls(protocol.MethodRunInject)); n != writeBurst {
		t.Fatalf("%d run.inject calls, want %d", n, writeBurst)
	}
	posts := h.slack.posts()
	if last := posts[len(posts)-1]; !strings.Contains(last.Text, "used its 30 actions") {
		t.Fatalf("reply to action %d = %q, want the rate-limit message", writeBurst+1, last.Text)
	}
}

func TestNeedFailsWhileSlackRefusesIt(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.slack.refuseNext("chat.postMessage", "internal_error")
	if err := h.svc.Need(bg, run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"}); err == nil || !strings.Contains(err.Error(), "internal_error") {
		t.Fatalf("Need while Slack refuses = %v, want Slack's error so push offers it again", err)
	}
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	if got := h.message(run).NeedKey; got != "k" {
		t.Fatalf("NeedKey after the retry = %q, want k", got)
	}
}

func TestStaleMessagesAreRetiredWithNothingFromTheRun(t *testing.T) {
	h := newHarness(t)
	h.link()
	kept, handed := h.newRun("Fix auth"), h.newRun("Handed off")
	for _, run := range []*domain.Run{kept, handed} {
		h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	}
	retired := func(run *domain.Run) {
		t.Helper()
		const text = "This message no longer follows the run."
		updates := h.slack.calls("chat.update")
		last := updates[len(updates)-1]
		if last.Text != text || layoutOf(t, last).sections[0] != text || strings.Contains(last.Text, run.Title) {
			t.Fatalf("last update = %+v, want only %q", last, text)
		}
		if _, err := h.db.GetSlackMessage(bg, run.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s's message after it was retired: %v, want none", run.Title, err)
		}
	}

	if _, err := h.svc.Unlink(bg, h.ada.ID); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	h.clear(kept)
	retired(kept)

	// A message that no longer follows its run, as when the run changes
	// hands, is forgotten only once Slack has taken its retirement.
	h.slack.refuseNext("chat.update", "internal_error")
	if err := h.svc.Need(bg, handed, push.Need{Member: handed.MemberID, Key: "k2", Body: "Needs you"}); err == nil {
		t.Fatal("Need with a refused retirement = nil, want Slack's error so push tries again")
	}
	if got := h.message(handed); got.NeedKey != "k" {
		t.Fatalf("message after a refused retirement = %+v, want it kept", got)
	}
	h.need(handed, push.Need{Member: handed.MemberID, Key: "k2", Body: "Needs you"})
	retired(handed)
}

func TestClearFailsWhileSlackRefusesIt(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	h.slack.refuseNext("chat.update", "internal_error")
	if err := h.svc.Clear(bg, run.ID); err == nil || !strings.Contains(err.Error(), "internal_error") {
		t.Fatalf("Clear while Slack refuses = %v, want Slack's error so push tries again", err)
	}
	if got := h.message(run).NeedKey; got != "k" {
		t.Fatalf("NeedKey after a refused clear = %q, want k", got)
	}
	h.clear(run)
	if got := h.message(run).NeedKey; got != "" {
		t.Fatalf("NeedKey after the retry = %q, want empty", got)
	}
}

func TestRepliesReachTheRunInOrder(t *testing.T) {
	h := newHarness(t)
	h.link()
	h.start()
	run := h.newRun("Fix auth")
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	ts := h.message(run).TS

	for i := range 5 {
		h.slack.send(eventEnvelope(fmt.Sprintf("env-%d", i), fmt.Sprintf("Ev%d", i), threadReply("U1", strconv.Itoa(i), ts)))
	}
	eventually(t, "five replies to reach the run", func() bool { return len(h.local.calls(protocol.MethodRunInject)) == 5 })
	for i, raw := range h.local.calls(protocol.MethodRunInject) {
		var p protocol.RunInjectParams
		if err := json.Unmarshal(raw, &p); err != nil || p.Message != strconv.Itoa(i) {
			t.Fatalf("inject %d = %s (%v), want message %d", i, raw, err, i)
		}
	}
}

func TestAMessageTheBotCannotEditDoesNotHoldUpTheNextOne(t *testing.T) {
	h := newHarness(t)
	h.link()
	run := h.newRun("Fix auth")
	h.need(run, push.Need{Member: run.MemberID, Key: "k", Body: "Needs you"})
	// A new app's bot, in another DM, cannot edit the old app's message.
	if err := h.db.PutSlackLink(bg, &store.SlackLink{Member: h.ada.ID, TeamID: "T1", UserID: "U1", Channel: "D2", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("PutSlackLink: %v", err)
	}
	h.slack.refuseNext("chat.update", "cant_update_message")
	h.need(run, push.Need{Member: run.MemberID, Key: "k2", Body: "Still needs you"})
	posts := h.slack.posts()
	if last := posts[len(posts)-1]; last.Channel != "D2" || !strings.Contains(last.Text, "Still needs you") {
		t.Fatalf("last post = %+v, want the need in the new DM", last)
	}
	if m := h.message(run); m.Channel != "D2" || m.NeedKey != "k2" {
		t.Fatalf("stored message = %+v, want need k2 in D2", m)
	}
}

func TestNoticeIsANewMessageInTheLinkedDM(t *testing.T) {
	h := newHarness(t)
	n := push.Notice{Workspace: h.ws.ID, Title: "<!channel> aether", Body: "Budget warning: $8.50 of $10.00 spent"}
	if err := h.svc.Notice(bg, h.ada.ID, n); err != nil {
		t.Fatalf("Notice without a link: %v", err)
	}
	if posts := h.slack.posts(); len(posts) != 0 {
		t.Fatalf("an unlinked member got %d posts, want none", len(posts))
	}

	h.link()
	for range 2 {
		if err := h.svc.Notice(bg, h.ada.ID, n); err != nil {
			t.Fatalf("Notice: %v", err)
		}
	}
	posts := h.slack.posts()
	if len(posts) != 2 || posts[1].Channel != "D1" {
		t.Fatalf("posts = %+v, want two new messages in D1", posts)
	}
	l := layoutOf(t, posts[1])
	if l.sections[0] != "*&lt;!channel&gt; aether*\nBudget warning: $8.50 of $10.00 spent" {
		t.Fatalf("section = %q, want the escaped workspace and the line", l.sections[0])
	}
	if want := "<https://box.tail.ts.net/?page=workspace&id=" + string(h.ws.ID) + "|Open in Aether>"; l.context != want {
		t.Fatalf("context = %q, want %q", l.context, want)
	}
}
