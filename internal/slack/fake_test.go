package slack

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type slackCall struct {
	Method   string
	Channel  string           `json:"channel"`
	TS       string           `json:"ts"`
	ThreadTS string           `json:"thread_ts"`
	Text     string           `json:"text"`
	Blocks   []map[string]any `json:"blocks"`
}

type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server
	out chan []byte

	mu      sync.Mutex
	chat    []slackCall
	acks    []string
	opens   int
	sockets int
	counter int
	// refuse is the error Slack answers the next call of a method with.
	refuse map[string]string
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{t: t, out: make(chan []byte), refuse: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/socket" {
		f.socket(w, r)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	want := "Bearer xoxb-test"
	if method == "apps.connections.open" {
		want = "Bearer xapp-test"
	}
	if got := r.Header.Get("Authorization"); got != want {
		f.t.Errorf("%s Authorization = %q, want %q", method, got, want)
	}
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "auth.test":
		_, _ = fmt.Fprint(w, `{"ok":true,"user_id":"UBOT","user":"aether","team_id":"T1","team":"Acme"}`)
	case "apps.connections.open":
		f.mu.Lock()
		f.opens++
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"ok":true,"url":"ws://%s/socket"}`, r.Host)
	case "chat.postMessage", "chat.update":
		var c slackCall
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			f.t.Errorf("%s: decode body: %v", method, err)
		}
		c.Method = method
		f.mu.Lock()
		f.chat = append(f.chat, c)
		refusal := f.refuse[method]
		delete(f.refuse, method)
		f.counter++
		ts := fmt.Sprintf("100.%06d", f.counter)
		f.mu.Unlock()
		if refusal != "" {
			_, _ = fmt.Fprintf(w, `{"ok":false,"error":%q}`, refusal)
			return
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"channel":%q,"ts":%q}`, c.Channel, ts)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSlack) socket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx := context.WithoutCancel(r.Context())
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`)); err != nil {
		return
	}
	f.mu.Lock()
	f.sockets++
	f.mu.Unlock()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var ack struct {
				EnvelopeID string `json:"envelope_id"`
			}
			if err := json.Unmarshal(raw, &ack); err != nil {
				f.t.Errorf("decode ack %q: %v", raw, err)
				return
			}
			f.mu.Lock()
			f.acks = append(f.acks, ack.EnvelopeID)
			f.mu.Unlock()
		}
	}()
	for {
		select {
		case raw := <-f.out:
			if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
				return
			}
		case <-closed:
			return
		}
	}
}

func (f *fakeSlack) send(msg any) {
	f.t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		f.t.Fatalf("marshal %v: %v", msg, err)
	}
	select {
	case f.out <- raw:
	case <-time.After(5 * time.Second):
		f.t.Fatalf("timed out sending %s: no socket is connected", raw)
	}
}

func (f *fakeSlack) refuseNext(method, code string) {
	f.mu.Lock()
	f.refuse[method] = code
	f.mu.Unlock()
}

func (f *fakeSlack) calls(method string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slackCall
	for _, c := range f.chat {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeSlack) posts() []slackCall { return f.calls("chat.postMessage") }

func (f *fakeSlack) waitPosts(n int) []slackCall {
	f.t.Helper()
	eventually(f.t, fmt.Sprintf("%d posted messages", n), func() bool { return len(f.posts()) >= n })
	return f.posts()
}

func (f *fakeSlack) connections() (opens, sockets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.sockets
}

func (f *fakeSlack) acked(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.acks {
		if a == id {
			return true
		}
	}
	return false
}

type localCall struct {
	Method string
	Params json.RawMessage
}

// fakeLocal is both Config.Local and the Caller it returns.
type fakeLocal struct {
	mu    sync.Mutex
	err   error
	items []protocol.AttentionItem
	log   []localCall
}

func (l *fakeLocal) open(_ context.Context, _ sshd.ConnectorGrant, _ string) (Caller, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	return l, nil
}

func (l *fakeLocal) setErr(err error) {
	l.mu.Lock()
	l.err = err
	l.mu.Unlock()
}

func (l *fakeLocal) setItems(items ...protocol.AttentionItem) {
	l.mu.Lock()
	l.items = items
	l.mu.Unlock()
}

func (l *fakeLocal) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log = append(l.log, localCall{method, params})
	switch method {
	case protocol.MethodServerInfo:
		return json.RawMessage(`{"dashboard_url":"https://box.tail.ts.net"}`), nil
	case protocol.MethodAttentionList:
		raw, _ := json.Marshal(protocol.AttentionListResult{Items: l.items})
		return raw, nil
	case protocol.MethodRunInject:
		return json.RawMessage(`{"message":{"id":"m1","state":"sent"}}`), nil
	case protocol.MethodRunInputRespond:
		return json.RawMessage(`{"status":"answered"}`), nil
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "unexpected method " + method}
}

func (l *fakeLocal) calls(method string) []json.RawMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []json.RawMessage
	for _, c := range l.log {
		if c.Method == method {
			out = append(out, c.Params)
		}
	}
	return out
}

type harness struct {
	t     *testing.T
	slack *fakeSlack
	local *fakeLocal
	db    *store.DB
	svc   *Service
	ada   *domain.Member
	ws    *domain.Workspace
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{t: t, slack: newFakeSlack(t), local: &fakeLocal{}, db: db}
	h.svc, err = New(Config{Store: db, AppToken: "xapp-test", BotToken: "xoxb-test", Name: "build-box",
		Local: h.local.open, API: h.slack.srv.URL + "/api/", HTTP: h.slack.srv.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.svc.Close() })

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	h.ada = &domain.Member{DisplayName: "Ada", PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))),
		Color: "#e6194b", Role: domain.RoleCollaborator}
	h.ws = &domain.Workspace{Name: "aether"}
	ctx := context.Background()
	if err := db.CreateMember(ctx, h.ada); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	if err := db.CreateWorkspace(ctx, h.ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return h
}

func (h *harness) start() {
	h.t.Helper()
	if err := h.svc.Start(context.Background()); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	h.connected()
}

func (h *harness) connected() {
	h.t.Helper()
	eventually(h.t, "the bot to connect", func() bool {
		st, err := h.svc.Status(context.Background(), h.ada.ID)
		return err == nil && st.Connected
	})
}

func (h *harness) newRun(title string) *domain.Run {
	h.t.Helper()
	run := &domain.Run{Title: title, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(context.Background(), run); err != nil {
		h.t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func (h *harness) link() {
	h.t.Helper()
	err := h.db.PutSlackLink(context.Background(), &store.SlackLink{Member: h.ada.ID, TeamID: "T1", UserID: "U1",
		Channel: "D1", CreatedAt: time.Now()})
	if err != nil {
		h.t.Fatalf("PutSlackLink: %v", err)
	}
}

func (h *harness) need(run *domain.Run, n push.Need) {
	h.t.Helper()
	if err := h.svc.Need(bg, run, n); err != nil {
		h.t.Fatalf("Need: %v", err)
	}
}

func (h *harness) clear(run *domain.Run) {
	h.t.Helper()
	if err := h.svc.Clear(bg, run.ID); err != nil {
		h.t.Fatalf("Clear: %v", err)
	}
}

func (h *harness) message(run *domain.Run) *store.SlackMessage {
	h.t.Helper()
	m, err := h.db.GetSlackMessage(context.Background(), run.ID)
	if err != nil {
		h.t.Fatalf("GetSlackMessage: %v", err)
	}
	return m
}

func permission(run *domain.Run, title string) protocol.AttentionItem {
	return protocol.AttentionItem{Kind: protocol.AttentionKindPermission, RunID: string(run.ID), WorkspaceID: string(run.WorkspaceID),
		Request: &protocol.AttentionRequest{ID: "req1", Kind: protocol.AttentionKindPermission, Title: title,
			Options: []protocol.AttentionOption{
				{ID: "opt-allow", Label: "Allow", Kind: "allow_once"},
				{ID: "opt-always", Label: "Always allow", Kind: "allow_always"},
				{ID: "opt-reject", Label: "Reject", Kind: "reject_once"},
			}}}
}

func eventEnvelope(id, eventID string, ev map[string]any) envelope {
	payload, _ := json.Marshal(map[string]any{"team_id": "T1", "event_id": eventID, "event": ev})
	return envelope{Type: "events_api", EnvelopeID: id, Payload: payload}
}

func dm(user, text string) map[string]any {
	return map[string]any{"type": "message", "channel_type": "im", "channel": "D1", "user": user, "text": text, "ts": "1.0"}
}

func threadReply(user, text, threadTS string) map[string]any {
	ev := dm(user, text)
	ev["thread_ts"] = threadTS
	return ev
}

func pressEnvelope(ts, value string) envelope {
	payload, _ := json.Marshal(map[string]any{"type": "block_actions", "user": map[string]any{"id": "U1"},
		"team": map[string]any{"id": "T1"}, "container": map[string]any{"channel_id": "D1", "message_ts": ts},
		"actions": []map[string]any{{"value": value}}})
	return envelope{Type: "interactive", EnvelopeID: "env-press", Payload: payload}
}

type button struct{ label, value string }

type layout struct {
	sections []string
	buttons  []button
	actions  int
	context  string
}

func layoutOf(t *testing.T, c slackCall) layout {
	t.Helper()
	var l layout
	for _, b := range c.Blocks {
		switch b["type"] {
		case "section":
			l.sections = append(l.sections, b["text"].(map[string]any)["text"].(string))
		case "actions":
			l.actions++
			for _, e := range b["elements"].([]any) {
				el := e.(map[string]any)
				l.buttons = append(l.buttons, button{el["text"].(map[string]any)["text"].(string), el["value"].(string)})
			}
		case "context":
			l.context = b["elements"].([]any)[0].(map[string]any)["text"].(string)
		default:
			t.Fatalf("unexpected block %v", b)
		}
	}
	return l
}
