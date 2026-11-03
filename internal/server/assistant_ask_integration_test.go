//go:build integration

package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/assistant"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// A connector turn's launch card is decided from that connector by echoing
// its text, and the launch runs once; every other way to decide it from a
// connector, or to reach assistant.ask from anywhere else, decides nothing.
func TestIntegrationAssistantAskCard(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	ws := seedAssistantWorkspace(t, ctx, env, "ask")
	const task = "launched from a connector card"
	env.rt.script(task, func(c *e2eContainer) {
		c.output("working\r\n")
		c.exitNow(0)
	})

	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	if perr := env.call(protocol.MethodPreferenceAdd, protocol.PreferenceAddParams{Text: "I live at 4 Elm Street"}, nil); perr != nil {
		t.Fatalf("preference.add: %v", perr)
	}
	write := []string{sshd.ConnectorScopeRead, sshd.ConnectorScopeWrite}
	a, conA := addConnector(t, env, write)
	_, conB := addConnector(t, env, write)
	ask := func(l *sshd.Local, p protocol.AssistantAskParams) (protocol.AssistantAskResult, *protocol.Error) {
		raw, _ := json.Marshal(p)
		var out protocol.AssistantAskResult
		result, perr := l.Call(ctx, protocol.MethodAssistantAsk, raw)
		if perr == nil {
			_ = json.Unmarshal(result, &out)
		}
		return out, perr
	}

	reply := acpmock.PromptRunPrefix + `printf 'I live at 4 Elm Street. '; head -c 5000 /dev/zero | tr '\0' a; exit`
	done, perr := ask(conA, protocol.AssistantAskParams{Prompt: reply, IdempotencyKey: "k1"})
	if perr != nil || done.State != protocol.AssistantAskDone || !strings.HasPrefix(done.Reply, "[preference withheld]. aaa") ||
		strings.Contains(done.Reply, "Elm") || len(done.Reply) > 4096 || !strings.Contains(done.Reply, "[reply cut here, at byte ") {
		t.Fatalf("prompt = %+v, %v, want done with the preference withheld and the reply cut at 4 KiB", done, perr)
	}
	if again, perr := ask(conA, protocol.AssistantAskParams{Prompt: reply, IdempotencyKey: "k1"}); perr != nil || again.TurnID != done.TurnID {
		t.Fatalf("retry with the same key = %+v, %v, want turn %s again", again, perr, done.TurnID)
	}
	if _, perr := ask(conB, protocol.AssistantAskParams{TurnID: done.TurnID}); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("another connector polling %s = %v, want not found", done.TurnID, perr)
	}

	turn, err := env.srv.assistant.Prompt(ctx, env.member.ID, acpmock.PromptWait, "connector:"+a+":wait", nil,
		assistant.ConnectorOrigin("ChatGPT", a, a, write))
	if err != nil {
		t.Fatalf("connector prompt: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(scheduler.AssistantSocketDir(filepath.Join(env.data, "assistant"), env.member.ID), "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	park := func(method string, params any) string {
		t.Helper()
		raw, _ := json.Marshal(params)
		req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
		if _, err := conn.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			t.Fatal(err)
		}
		var resp protocol.Response
		if err = json.Unmarshal(line, &resp); err != nil || resp.Error == nil || !strings.Contains(resp.Error.Message, "Waiting for approval as card ") {
			t.Fatalf("%s answered %s, want it parked", method, line)
		}
		_, id, _ := strings.Cut(resp.Error.Message, "Waiting for approval as card ")
		id, _, _ = strings.Cut(id, ";")
		return id
	}
	launch := park(protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchHeadless)})
	long := park(protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: strings.Repeat("long task ", 250), Harness: "fake", Mode: string(domain.LaunchHeadless)})
	save := park(protocol.MethodEnvSave, struct{}{})
	for _, method := range []string{protocol.MethodAssistantAsk, protocol.MethodAssistantConfirm} {
		if perr, line := env.toolCall(conn, r, method); perr == nil {
			t.Fatalf("the tool socket called %s: %s", method, line)
		}
	}

	dashboard, err := json.Marshal(protocol.AssistantConfirmation{ID: "card-dashboard", Method: protocol.MethodEnvSave, Params: json.RawMessage("{}"), Summary: "Save your environment"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = env.srv.Store().AddAssistantCard(ctx, &store.AssistantCard{ID: "card-dashboard", MemberID: env.member.ID, Method: protocol.MethodEnvSave, Params: "{}",
		Card: string(dashboard), Origin: `{"door":"dashboard"}`, State: protocol.AssistantCardPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	orphan, err := json.Marshal(protocol.AssistantConfirmation{ID: "card-orphan", Method: protocol.MethodEnvSave, Params: json.RawMessage("{}"), Summary: "Save your environment"})
	if err != nil {
		t.Fatal(err)
	}
	if err = env.srv.Store().AddAssistantCard(ctx, &store.AssistantCard{ID: "card-orphan", MemberID: env.member.ID, Method: protocol.MethodEnvSave, Params: "{}",
		Card: string(orphan), Origin: `{"door":"connector","client":"ChatGPT","connector_id":"` + a + `","grant":"` + a + `","scopes":["read","write"]}`,
		State: protocol.AssistantCardPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr != nil {
		t.Fatal(perr)
	}
	stopped, perr := ask(conA, protocol.AssistantAskParams{TurnID: turn.ID})
	if perr != nil || stopped.State != protocol.AssistantAskFailed || len(stopped.PendingCards) != 3 {
		t.Fatalf("poll of the stopped turn = %+v, %v, want failed with its 3 cards", stopped, perr)
	}
	shown := map[string]string{}
	for _, c := range stopped.PendingCards {
		shown[c.CardID] = c.Card
	}
	if shown[long] != protocol.AssistantAskWaitingInAether || shown[save] != protocol.AssistantAskWaitingInAether || !strings.Contains(shown[launch], task) {
		t.Fatalf("pending cards = %v, want the launch's text and the long and env.save cards waiting in Aether", shown)
	}
	card := shown[launch]

	for _, tc := range []struct {
		name   string
		caller *sshd.Local
		p      protocol.AssistantAskParams
		code   int
		want   string
	}{
		{"wrong echo", conA, protocol.AssistantAskParams{CardID: launch, Decision: protocol.AssistantAskApprove, Card: card + " "},
			protocol.CodeInvalidParams, "card text does not match card " + launch + "; show the person the card and pass its text exactly"},
		{"another grant", conB, protocol.AssistantAskParams{CardID: launch, Decision: protocol.AssistantAskApprove, Card: card},
			protocol.CodeNotFound, "assistant: no such pending confirmation: " + launch},
		{"a dashboard card", conA, protocol.AssistantAskParams{CardID: "card-dashboard", Decision: protocol.AssistantAskApprove, Card: "Save your environment"},
			protocol.CodeNotFound, "assistant: no such pending confirmation: card-dashboard"},
		{"a card no turn of the grant raised", conA, protocol.AssistantAskParams{CardID: "card-orphan", Decision: protocol.AssistantAskApprove, Card: "Save your environment"},
			protocol.CodeNotFound, "assistant: no such pending confirmation: card-orphan"},
		{"a long card", conA, protocol.AssistantAskParams{CardID: long, Decision: protocol.AssistantAskApprove, Card: "x"},
			protocol.CodeDenied, "card " + long + " is long or shows a saved preference, so it is decided only in Aether"},
		{"control without the scope", conA, protocol.AssistantAskParams{CardID: save, Decision: protocol.AssistantAskApprove, Card: textOf(t, env, a, save)},
			protocol.CodeDenied, "card " + save + " is decided only in Aether: env.save needs the control scope"},
		{"the dashboard", env.srv.ssh.Local(env.member.ID), protocol.AssistantAskParams{CardID: launch, Decision: protocol.AssistantAskApprove, Card: card},
			protocol.CodeDenied, "assistant.ask is for ChatGPT and Claude connectors"},
		{"the assistant", env.srv.ssh.AssistantLocal(env.member.ID), protocol.AssistantAskParams{CardID: launch, Decision: protocol.AssistantAskApprove, Card: card},
			protocol.CodeDenied, "assistant.ask is for ChatGPT and Claude connectors"},
	} {
		if _, perr := ask(tc.caller, tc.p); perr == nil || perr.Code != tc.code || !strings.HasPrefix(perr.Message, tc.want) {
			t.Errorf("%s: assistant.ask = %v, want %d %q", tc.name, perr, tc.code, tc.want)
		}
	}
	for _, id := range []string{launch, long, save, "card-dashboard", "card-orphan"} {
		if row, err := env.srv.Store().AssistantCard(ctx, env.member.ID, id); err != nil || row.State != protocol.AssistantCardPending {
			t.Fatalf("card %s after the refusals = %+v, %v, want it still pending", id, row, err)
		}
	}

	got, perr := ask(conA, protocol.AssistantAskParams{CardID: launch, Decision: protocol.AssistantAskApprove, Card: card})
	if perr != nil || got.CardID != launch || got.Decision != protocol.AssistantAskApprove {
		t.Fatalf("approving by echo = %+v, %v", got, perr)
	}
	if _, perr = ask(conA, protocol.AssistantAskParams{CardID: launch, Decision: protocol.AssistantAskApprove, Card: card}); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("approving it again = %v, want not found", perr)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		row, err := env.srv.Store().AssistantCard(ctx, env.member.ID, launch)
		if err != nil {
			t.Fatal(err)
		}
		if row.State == protocol.AssistantCardSucceeded {
			break
		}
		if row.State == protocol.AssistantCardFailed || time.Now().After(deadline) {
			t.Fatalf("launch card = %+v, want it to succeed", row)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var runs protocol.RunListResult
	if perr = env.call(protocol.MethodRunList, protocol.RunListParams{}, &runs); perr != nil {
		t.Fatal(perr)
	}
	launched := 0
	for _, run := range runs.Runs {
		if run.Task == task {
			launched++
		}
	}
	if launched != 1 {
		t.Fatalf("%d sessions run %q, want 1", launched, task)
	}
}

func textOf(t *testing.T, env *assistantEnv, grant, id string) string {
	t.Helper()
	c, err := env.srv.assistant.GrantCard(t.Context(), env.member.ID, grant, id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Text
}

// addConnector stores a ChatGPT connector of env's member with scopes and
// returns its id and a client calling as it.
func addConnector(t *testing.T, env *assistantEnv, scopes []string) (string, *sshd.Local) {
	t.Helper()
	now := time.Now()
	c := &store.Connector{Member: env.member.ID, ClientID: "client", ClientName: "ChatGPT", RedirectHost: "chatgpt.com", Resource: "https://aether.test/mcp",
		Scopes: scopes, CreatedAt: now}
	if err := env.srv.Store().(store.ConnectorStore).CreateConnector(t.Context(), c, store.ConnectorTokens{Access: rand.Text(), AccessExpiresAt: now.Add(time.Hour),
		Refresh: rand.Text(), RefreshExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return c.ID, env.srv.ssh.ConnectorLocal(sshd.ConnectorGrant{Member: env.member.ID}, c.ID, "ChatGPT", scopes)
}

// A connector turn passes calls on its grant as it is when they are made:
// once the connector is revoked, auto-approval no longer passes its turn's
// launch, and its waiting prompt fails rather than runs.
func TestIntegrationAssistantAskRevoked(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	ws := seedAssistantWorkspace(t, ctx, env, "revoked")
	for _, task := range []string{"before the revocation", "after the revocation"} {
		env.rt.script(task, func(c *e2eContainer) { c.exitNow(0) })
	}
	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	on := true
	if perr := env.call(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{AutoApprove: &on}, nil); perr != nil {
		t.Fatalf("turn auto-approval on: %v", perr)
	}
	write := []string{sshd.ConnectorScopeRead, sshd.ConnectorScopeWrite}
	id, con := addConnector(t, env, write)
	if _, err := env.srv.assistant.Prompt(ctx, env.member.ID, acpmock.PromptWait, "connector:"+id+":wait", nil,
		assistant.ConnectorOrigin("ChatGPT", id, id, write)); err != nil {
		t.Fatalf("connector prompt: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(scheduler.AssistantSocketDir(filepath.Join(env.data, "assistant"), env.member.ID), "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	launch := func(task string) protocol.Response {
		t.Helper()
		params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchHeadless)})
		req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: protocol.MethodRunLaunch, Params: params})
		if _, err := conn.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			t.Fatal(err)
		}
		var resp protocol.Response
		if err = json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := launch("before the revocation"); resp.Error != nil {
		t.Fatalf("launch while the connector is live = %+v, want auto-approval to pass it", resp.Error)
	}
	if err = env.srv.Store().(store.ConnectorStore).DeleteConnector(ctx, id); err != nil {
		t.Fatal(err)
	}
	if resp := launch("after the revocation"); resp.Error == nil || !strings.Contains(resp.Error.Message, "Waiting for approval as card ") {
		t.Fatalf("launch after the revocation = %+v, want a card", resp)
	}
	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr != nil {
		t.Fatal(perr)
	}
	raw, _ := json.Marshal(protocol.AssistantAskParams{Prompt: "anything"})
	// The refusal is the call's error, or the failed turn's when the queue
	// held the prompt behind the stopped turn.
	res, perr := con.Call(ctx, protocol.MethodAssistantAsk, raw)
	revoked := "assistant: ChatGPT connector " + id + ": it was revoked"
	var got protocol.AssistantAskResult
	if perr != nil {
		got.State, got.Error = protocol.AssistantAskFailed, perr.Message
	} else if err = json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != protocol.AssistantAskFailed || got.Error != revoked {
		t.Fatalf("prompt from the revoked connector = %s, %v, want it refused with the revocation", res, perr)
	}
}
