package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

func TestConnectorReplyWithholdsPreferencesAndIsCapped(t *testing.T) {
	dir := t.TempDir()
	db, err := storetest.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ada := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(t.Context(), ada); err != nil {
		t.Fatal(err)
	}
	prefs := preference.New(liveStore{db}, filepath.Join(dir, "tombstones"))
	for _, text := range []string{"I live at 4 Elm Street", "Elm Street apartment nine", "my door code is 1234", "my door code is 1234 5678", "I prefer table output", "I bank with AT&T Credit", "zip codes 90210 10001 60601"} {
		if _, err = prefs.Add(t.Context(), ada.ID, text, "", protocol.PreferenceSourceSettings, ""); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := prefs.List(t.Context(), ada.ID, protocol.PreferenceListParams{Query: "table output"})
	if err != nil || len(listed.Preferences) != 1 {
		t.Fatal(listed, err)
	}
	off := false
	if _, err = prefs.Update(t.Context(), ada.ID, protocol.PreferenceUpdateParams{ID: listed.Preferences[0].ID, Enabled: &off}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Dir: filepath.Join(dir, "assistant"), Store: db, Host: &fakeHost{}, Preferences: prefs})
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.ConnectorReply(t.Context(), ada.ID, "Done.\nYou said i LIVE at 4\u200b Elm  street, so I launched it.")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Done.\nYou said " + preferenceWithheld + ", so I launched it."; got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}

	for reply, want := range map[string]string{
		"Note: I live at 4 Elm Street apartment nine, ok.": "Note: " + preferenceWithheld + ", ok.",
		"It is my door code is 1234 5678 now.":             "It is " + preferenceWithheld + " now.",
	} {
		if got, err = s.ConnectorReply(t.Context(), ada.ID, reply); err != nil || got != want {
			t.Fatalf("reply %q = %q, %v, want %q: overlapping and prefix matches withheld as one", reply, got, err, want)
		}
	}

	// The check reads each value as sent, before the full text escapes a
	// line break, and counts a disabled preference.
	card := Card{AssistantConfirmation: protocol.AssistantConfirmation{Method: protocol.MethodRunLaunch, Summary: `Launch claude in api: "Fix report…"`,
		Params: []byte(`{"task":"Fix report\nI prefer table output"}`)}}
	if c, cerr := s.connectorCard(t.Context(), ada.ID, card); cerr != nil || !c.AetherOnly {
		t.Fatalf("card showing a disabled preference after a line break = %+v, %v, want it decided only in Aether", c, cerr)
	}
	nested := Card{AssistantConfirmation: protocol.AssistantConfirmation{Method: protocol.MethodTemplateLaunch, Summary: `Launch template "notes"`,
		Params: []byte(`{"values":{"note":"I bank with AT\u0026T Credit"}}`)}}
	reason := Card{AssistantConfirmation: protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: `Stop run-1. The assistant's reason: "my door\ncode is 1234"`,
		Reason: "my door\ncode is 1234", Params: []byte(`{"run_id":"run-1"}`)}}
	numbers := Card{AssistantConfirmation: protocol.AssistantConfirmation{Method: protocol.MethodTemplateLaunch, Summary: `Launch template "zip codes"`,
		Params: []byte(`{"values":{"zip codes":[90210,10001,60601]}}`)}}
	for name, c := range map[string]Card{"a nested value": nested, "the stated reason": reason, "numbers": numbers} {
		if got, cerr := s.connectorCard(t.Context(), ada.ID, c); cerr != nil || !got.AetherOnly {
			t.Fatalf("card showing a preference in %s = %+v, %v, want it decided only in Aether", name, got, cerr)
		}
	}

	long := strings.Repeat("é", maxConnectorReply)
	got, err = s.ConnectorReply(t.Context(), ada.ID, long)
	if err != nil {
		t.Fatal(err)
	}
	body, note, ok := strings.Cut(got, "\n[reply cut here, at byte ")
	if !ok || len(got) > maxConnectorReply || !strings.HasPrefix(long, body) || note == "" ||
		!strings.HasSuffix(got, " of 8192]") {
		t.Fatalf("long reply = %d bytes ending %q, want at most %d ending in where it was cut", len(got), got[len(got)-60:], maxConnectorReply)
	}
}

// A card's full text shows a value that holds a line break or a character
// not printed as itself quoted, so a task cannot pose as another field; a
// summary holding one is decided only in Aether.
func TestConnectorCardTextCannotPoseAsAField(t *testing.T) {
	s := New(Config{Dir: filepath.Join(t.TempDir(), "assistant"), Store: newMemStore(), Host: &fakeHost{}})
	t.Cleanup(func() { _ = s.Close() })
	c := Card{AssistantConfirmation: protocol.AssistantConfirmation{Method: protocol.MethodRunLaunch, Summary: `Launch claude in api: "fix it…"`,
		Params: []byte(`{"workspace_id":"ws-1","task":"fix it\nworkspace_id: ws-evil"}`)}}
	got, err := s.connectorCard(t.Context(), "mem-1", c)
	if want := `Launch claude in api: "fix it…"` + "\ntask: \"fix it\\nworkspace_id: ws-evil\"\nworkspace_id: ws-1"; err != nil || got.AetherOnly || got.Text != want {
		t.Fatalf("card = %+v, %v, want text %q", got, err, want)
	}
	nested := Card{AssistantConfirmation: protocol.AssistantConfirmation{Method: protocol.MethodRunInputRespond, Summary: "Answer run-1",
		Params: []byte("{\"run_id\":\"run-1\",\"values\":{\"note\":\"ok \u202eexe.txt\"}}")}}
	if got, err = s.connectorCard(t.Context(), "mem-1", nested); err != nil || !got.AetherOnly {
		t.Fatalf("card with a bidi override in a nested value = %+v, %v, want it decided only in Aether", got, err)
	}
	c.Summary = "Launch claude in api\npermission: default"
	if got, err = s.connectorCard(t.Context(), "mem-1", c); err != nil || !got.AetherOnly {
		t.Fatalf("card whose summary holds a line break = %+v, %v, want it decided only in Aether", got, err)
	}
	c.Summary = "Launch claude in api: \"fix \u202egnp.exe\""
	if got, err = s.connectorCard(t.Context(), "mem-1", c); err != nil || !got.AetherOnly {
		t.Fatalf("card with a bidi override in its summary = %+v, %v, want it decided only in Aether", got, err)
	}
}

// A call that passes without a card fails in a connector turn whose grant
// is gone, and passes in any other.
func TestGrantGateRefusesARevokedConnectorsTurn(t *testing.T) {
	s := New(Config{Dir: filepath.Join(t.TempDir(), "assistant"), Store: newMemStore(), Host: &fakeHost{},
		ConnectorGrant: func(context.Context, domain.MemberID, string) ([]string, error) {
			return nil, errors.New("it was revoked")
		}})
	t.Cleanup(func() { _ = s.Close() })
	m := s.member("mem-1")
	gate := grantGate{Caller: &actionServer{}, s: s, m: m}
	params := json.RawMessage(`{"run_id":"run-1"}`)
	if _, perr := gate.Call(t.Context(), protocol.MethodRunPause, params); perr != nil {
		t.Fatalf("pause in a dashboard turn = %v", perr)
	}
	m.mu.Lock()
	m.origin = ConnectorOrigin("ChatGPT", "con-1", "con-1", []string{"write"})
	m.mu.Unlock()
	if _, perr := gate.Call(t.Context(), protocol.MethodRunPause, params); perr == nil || perr.Message != "run.pause was not run: assistant: ChatGPT connector con-1: it was revoked" {
		t.Fatalf("pause in a revoked connector's turn = %v", perr)
	}
	if _, perr := gate.Call(t.Context(), protocol.MethodRunGet, params); perr != nil {
		t.Fatalf("read in a revoked connector's turn = %v", perr)
	}
	watch := json.RawMessage(`{"target":{"kind":"run","run_id":"run-1"},"relay_to":["run-2"]}`)
	m.actions.hold(context.Background(), protocol.AssistantAction{Method: protocol.MethodWatchCreate}, watch, time.Time{})
	if _, perr := gate.Call(t.Context(), protocol.MethodWatchCreate, watch); perr != nil {
		t.Fatalf("approved relaying watch in a revoked connector's turn = %v, want it run as approved", perr)
	}
}

// A keyless retry finds the connector's own prompt while it waits or runs,
// and nothing once it ended or for another connector.
func TestGrantTurnFindsTheConnectorsWaitingPrompt(t *testing.T) {
	w := newQueueWorld(t)
	origin := ConnectorOrigin("ChatGPT", "con-1", "con-1", []string{"write"})
	e, err := w.svc.Prompt(t.Context(), w.member, acpmock.PromptWait, "k0", nil, origin)
	if err != nil {
		t.Fatal(err)
	}
	since := e.CreatedAt.Add(-time.Second)
	if got, ok, gerr := w.svc.GrantTurn(t.Context(), w.member, "con-1", acpmock.PromptWait, since); gerr != nil || !ok || got.ID != e.ID {
		t.Fatalf("retry while it runs = %+v, %t, %v, want %s", got, ok, gerr, e.ID)
	}
	if _, ok, gerr := w.svc.GrantTurn(t.Context(), w.member, "con-2", acpmock.PromptWait, since); gerr != nil || ok {
		t.Fatalf("another connector's retry = %t, %v, want none", ok, gerr)
	}
	if err = w.svc.Cancel(t.Context(), w.member); err != nil {
		t.Fatal(err)
	}
	w.until(e.ID, protocol.AssistantQueueFailed)
	if _, ok, gerr := w.svc.GrantTurn(t.Context(), w.member, "con-1", acpmock.PromptWait, since); gerr != nil || ok {
		t.Fatalf("retry after it ended = %t, %v, want none", ok, gerr)
	}
}

// A connector turn reads no dashboard view context; any other turn does.
func TestConnectorTurnSeesNoViewContext(t *testing.T) {
	s := New(Config{Dir: filepath.Join(t.TempDir(), "assistant"), Store: newMemStore(), Host: &fakeHost{}})
	t.Cleanup(func() { _ = s.Close() })
	m := s.member("mem-1")
	m.mu.Lock()
	m.view, m.viewSource = &protocol.AssistantViewContext{Route: protocol.AssistantViewEntry{Name: "run", RunID: "run-1"}}, "prompt"
	m.mu.Unlock()
	if got := s.turnView(m); got.ViewContext == nil {
		t.Fatalf("view in a dashboard turn = %+v, want the held view", got)
	}
	m.mu.Lock()
	m.origin = ConnectorOrigin("ChatGPT", "con-1", "con-1", []string{"write"})
	m.mu.Unlock()
	if got := s.turnView(m); got.ViewContext != nil {
		t.Fatalf("view in a connector turn = %+v, want none", got)
	}
}

// In a live connector turn, a call that passes without a card still needs
// a scope the grant holds: no connector may save a preference.
func TestGrantGateRefusesWhatTheScopesDoNotCover(t *testing.T) {
	s := New(Config{Dir: filepath.Join(t.TempDir(), "assistant"), Store: newMemStore(), Host: &fakeHost{},
		ConnectorGrant: func(context.Context, domain.MemberID, string) ([]string, error) {
			return []string{"read", "write"}, nil
		},
		ConnectorScopes: func(method string) []string {
			return map[string][]string{protocol.MethodRunPause: {"write"}}[method]
		}})
	t.Cleanup(func() { _ = s.Close() })
	m := s.member("mem-1")
	m.mu.Lock()
	m.origin = ConnectorOrigin("ChatGPT", "con-1", "con-1", []string{"read", "write"})
	m.mu.Unlock()
	gate := grantGate{Caller: &actionServer{}, s: s, m: m}
	if _, perr := gate.Call(t.Context(), protocol.MethodRunPause, json.RawMessage(`{"run_id":"run-1"}`)); perr != nil {
		t.Fatalf("pause under write = %v", perr)
	}
	if _, perr := gate.Call(t.Context(), protocol.MethodAssistantRemember, json.RawMessage(`{"text":"I prefer tables"}`)); perr == nil ||
		perr.Message != "assistant.remember was not run: ChatGPT connector con-1 may not call it; ask the member to do it in Aether" {
		t.Fatalf("remember in a connector turn = %v, want it refused", perr)
	}
}
