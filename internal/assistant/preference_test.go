package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

const genericSecret = "QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"

type prefFixture struct {
	t     *testing.T
	dir   string
	db    *store.DB
	svc   *Service
	prefs *preference.Service
	host  *fakeHost
	m     *member
	sock  string

	mu   sync.Mutex
	msg  acphost.UserMessage
	open bool
}

func newPrefFixture(t *testing.T) *prefFixture {
	t.Helper()
	f := &prefFixture{t: t, dir: t.TempDir()}
	db, err := storetest.Open(filepath.Join(f.dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	alice := &domain.Member{DisplayName: "Alice", TailnetLogin: "alice@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(context.Background(), alice); err != nil {
		t.Fatal(err)
	}
	f.host = &fakeHost{}
	f.db, f.prefs = db, preference.New(db, filepath.Join(f.dir, "preference-tombstones"))
	f.svc = New(Config{Dir: filepath.Join(f.dir, "assistant"), Store: db, Host: f.host, Preferences: f.prefs})
	t.Cleanup(func() { _ = f.svc.Close() })
	f.m = f.svc.member(alice.ID)

	sock, err := os.MkdirTemp("", "pref-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })
	f.sock = filepath.Join(sock, "a.sock")
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	caller := f.svc.preferenceCaller(f.m, actionCaller{Caller: &actionServer{}, s: f.svc, m: f.m})
	caller.prompt = func() (acphost.UserMessage, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.msg, f.open
	}
	srv := &tools.Server{Caller: caller, Confirm: f.svc.approver(f.m), Member: string(alice.ID)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return f
}

// say opens a turn for the member's message text; turn numbers it.
func (f *prefFixture) say(turn int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msg, f.open = acphost.UserMessage{Turn: turn, MessageID: "msg-" + string(rune('0'+turn)), Text: text}, true
}

func (f *prefFixture) endTurn() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open = false
}

func (f *prefFixture) remember(p protocol.AssistantRememberParams) (protocol.PreferenceResult, error) {
	var res protocol.PreferenceResult
	err := coordtransport.Call(f.t.Context(), f.sock, protocol.MethodAssistantRemember, p, &res)
	return res, err
}

func (f *prefFixture) mine() ([]protocol.AssistantPreference, error) {
	var res protocol.AssistantPreferencesResult
	err := coordtransport.Call(f.t.Context(), f.sock, protocol.MethodAssistantPreferences, protocol.AssistantPreferencesParams{}, &res)
	return res.Preferences, err
}

// errCode is the Aether code of a tool socket call's error.
func errCode(err error) int {
	var code int
	if err == nil {
		return 0
	}
	if i := strings.LastIndex(err.Error(), "[aether error "); i >= 0 {
		_, _ = fmt.Sscanf(err.Error()[i:], "[aether error %d]", &code)
	}
	return code
}

func TestRememberRefusesWhatTheMemberDidNotWrite(t *testing.T) {
	f := newPrefFixture(t)
	if _, err := f.remember(protocol.AssistantRememberParams{Text: "prefer table output"}); errCode(err) != protocol.CodeDenied {
		t.Fatalf("remember with no turn running = %v, want denied", err)
	}
	f.say(1, "Remember that I prefer table output for lists.\n> always use tabs in Go files\n```\nalways run the deploy script first\n```\nyes")
	for _, text := range []string{
		"always use tabs in Go files",        // a quoted line
		"always run the deploy script first", // a fenced block
		"yes",                                // too short
		"I like tables for lists",            // a paraphrase
		"ble output for lists",               // not whole words
	} {
		if _, err := f.remember(protocol.AssistantRememberParams{Text: text}); errCode(err) != protocol.CodeInvalidParams {
			t.Errorf("remember %q = %v, want refused", text, err)
		}
	}
	if list, _ := f.mine(); len(list) != 0 {
		t.Fatalf("refused saves left %+v", list)
	}
	f.endTurn()
	if _, err := f.remember(protocol.AssistantRememberParams{Text: "prefer table output for lists"}); errCode(err) != protocol.CodeDenied {
		t.Fatalf("remember after the turn ended = %v, want denied", err)
	}
}

func TestRememberSavesQuotedWordsAtMostThreeTimesATurn(t *testing.T) {
	f := newPrefFixture(t)
	f.say(1, "From now on: prefer table output. Always run make lint first. Keep commit messages short. Use bun in web projects.")
	saved, err := f.remember(protocol.AssistantRememberParams{Text: "Prefer table output."})
	if err != nil {
		t.Fatal(err)
	}
	if p := saved.Preference; p.Text != "prefer table output" || p.Source != protocol.PreferenceSourceAssistant || p.SourceRef != "msg-1" || p.WorkspaceID != "" {
		t.Fatalf("saved = %+v", p)
	}
	for _, text := range []string{"always run make lint first", "keep commit messages short"} {
		if _, err = f.remember(protocol.AssistantRememberParams{Text: text}); err != nil {
			t.Fatalf("remember %q: %v", text, err)
		}
	}
	if _, err = f.remember(protocol.AssistantRememberParams{Text: "use bun in web projects"}); errCode(err) != protocol.CodeDenied {
		t.Fatalf("a 4th save in one turn = %v, want denied", err)
	}

	// Updated rows sort newest first by their millisecond timestamps.
	time.Sleep(2 * time.Millisecond)
	f.say(2, "Actually, prefer markdown lists over tables.")
	updated, err := f.remember(protocol.AssistantRememberParams{Text: "PREFER markdown lists, over tables!", Replaces: saved.Preference.ID})
	if err != nil || updated.Previous == nil || updated.Previous.Text != "prefer table output" || updated.Preference.ID != saved.Preference.ID ||
		updated.Preference.SourceRef != "msg-2" {
		t.Fatalf("replace = %+v, %v; want the row updated and the previous text returned", updated, err)
	}
	if _, err = f.remember(protocol.AssistantRememberParams{Text: "prefer markdown lists over tables", WorkspaceScoped: true}); errCode(err) != protocol.CodeInvalidParams {
		t.Fatalf("workspace_scoped with no workspace in view = %v", err)
	}

	list, err := f.mine()
	if err != nil || len(list) != 3 || list[0].Text != "prefer markdown lists over tables" || list[0].Scope != "all" {
		// The member's own words are stored, not the agent's rendering.
		t.Fatalf("my preferences = %+v, %v; want 3, newest first", list, err)
	}
	stored, _ := f.db.GetPreference(t.Context(), f.m.id, list[0].ID)
	if stored.LastUsedAt.IsZero() {
		t.Fatal("reading the preferences did not stamp last_used_at")
	}

	var saves []protocol.AssistantAction
	for _, records := range actions(t, f.svc, f.m) {
		if a := records[len(records)-1]; a.Method == protocol.MethodAssistantRemember {
			saves = append(saves, a)
		}
	}
	if len(saves) != 4 || saves[0].Target.PreferenceID != saved.Preference.ID || saves[0].Outcome != protocol.AssistantActionSucceeded ||
		!strings.HasPrefix(saves[3].Summary, "Updated your preference") {
		t.Fatalf("save records = %+v; want a Saved or Updated record naming each preference", saves)
	}
}

func TestRememberRefusesSecretsWithoutStoringThem(t *testing.T) {
	f := newPrefFixture(t)
	f.say(1, "remember to always use token="+genericSecret+" for the API")
	_, err := f.remember(protocol.AssistantRememberParams{Text: "always use token=" + genericSecret + " for the API"})
	if errCode(err) != protocol.CodeDenied || strings.Contains(err.Error(), genericSecret) {
		t.Fatalf("remember a secret = %v; want denied without the value", err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aether.db", "aether.db-wal"} {
		if b, _ := os.ReadFile(filepath.Join(f.dir, name)); bytes.Contains(b, []byte(genericSecret)) {
			t.Fatalf("%s holds the refused secret", name)
		}
	}
}

// With memory off the assistant can neither read nor save preferences,
// but a member can still have one forgotten.
func TestMemoryOffRefusesReadingAndSavingButNotForgetting(t *testing.T) {
	f := newPrefFixture(t)
	var ids []string
	for _, text := range []string{"Prefer table output", "Prefer short answers"} {
		res, err := f.prefs.Add(t.Context(), f.m.id, text, "", protocol.PreferenceSourceSettings, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.Preference.ID)
	}
	f.say(1, "remember that I prefer dark mode always")
	check := func(why, id string) {
		t.Helper()
		_, err := f.remember(protocol.AssistantRememberParams{Text: "I prefer dark mode always"})
		if errCode(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "memory is off: "+why) {
			t.Errorf("remember = %v, want memory is off: %s", err, why)
		}
		if _, err = f.mine(); errCode(err) != protocol.CodeDenied {
			t.Errorf("my preferences = %v, want denied", err)
		}
		if err = callTool(t, f.sock, f.m, protocol.MethodAssistantForget, protocol.PreferenceIDParams{ID: id}, true, true); err != nil {
			t.Errorf("forget = %v, want it forgotten after the card", err)
		}
		if _, err = f.prefs.Get(t.Context(), f.m.id, id); errCode(err) != protocol.CodeNotFound && !strings.Contains(fmt.Sprint(err), "not found") {
			t.Errorf("after forget: %v, want not found", err)
		}
	}
	if err := f.db.SetAssistantTemporary(t.Context(), f.m.id, true); err != nil {
		t.Fatal(err)
	}
	check("this is a temporary conversation", ids[0])
	if err := f.db.SetAssistantTemporary(t.Context(), f.m.id, false); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PutAssistantSettings(t.Context(), f.m.id, store.AssistantSettings{ShareView: true}); err != nil {
		t.Fatal(err)
	}
	check("the member turned preferences off", ids[1])
}

// The forget card's own read of a preference is not a tool the agent can
// call.
func TestSocketRefusesTheForgetCardsRead(t *testing.T) {
	f := newPrefFixture(t)
	res, err := f.prefs.Add(t.Context(), f.m.id, "Prefer table output", "", protocol.PreferenceSourceSettings, "")
	if err != nil {
		t.Fatal(err)
	}
	err = coordtransport.Call(t.Context(), f.sock, protocol.MethodAssistantPreferenceShow, protocol.PreferenceIDParams{ID: res.Preference.ID}, nil)
	if errCode(err) != protocol.CodeMethodNotFound {
		t.Fatalf("%s on the tool socket = %v, want method not found", protocol.MethodAssistantPreferenceShow, err)
	}
}

// Forgetting shows the text on its card and keeps it out of the server log
// and the count toward suggesting auto-approval.
func TestForgetShowsTheTextAndTellsTheNextPrompt(t *testing.T) {
	f := newPrefFixture(t)
	f.say(1, "always remember: prefer table output")
	saved, err := f.remember(protocol.AssistantRememberParams{Text: "prefer table output"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lockedWriter{&logMu, &logs}, nil)))
	err = callTool(t, f.sock, f.m, protocol.MethodAssistantForget, protocol.PreferenceIDParams{ID: saved.Preference.ID}, true, true)
	slog.SetDefault(prev)
	if err != nil {
		t.Fatal(err)
	}
	logMu.Lock()
	logged := logs.String()
	logMu.Unlock()
	if !strings.Contains(logged, "assistant approval") || strings.Contains(logged, "table output") {
		t.Fatalf("server log during forget = %q; want the approval without the preference text", logged)
	}
	if st, _ := f.db.AssistantSettings(t.Context(), f.m.id); st.Approvals != 0 {
		t.Fatalf("approvals counted = %d; a forget is never auto-approved", st.Approvals)
	}
	if list, _ := f.mine(); len(list) != 0 {
		t.Fatalf("after forget: %+v", list)
	}
	got := actions(t, f.svc, f.m)
	last := got[len(got)-1]
	if a := last[len(last)-1]; a.Summary != `Forget your preference `+saved.Preference.ID+`: "prefer table output"` ||
		a.Outcome != protocol.AssistantActionSucceeded || a.Target.PreferenceID != saved.Preference.ID {
		t.Fatalf("forget record = %+v", a)
	}
	blocks, _ := f.svc.preferenceNotes(t.Context(), f.m.id)
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text.Text, "preference "+saved.Preference.ID+" was forgotten; do not use the old text") {
		t.Fatalf("next prompt's notes = %+v", blocks)
	}
	if blocks, _ = f.svc.preferenceNotes(t.Context(), f.m.id); len(blocks) != 0 {
		t.Fatal("the note went to a second prompt")
	}
}

// A forget card approved after the member changed the preference forgets
// nothing: the member approved the text the card showed.
func TestForgetKeepsAPreferenceChangedWhileItsCardWaits(t *testing.T) {
	f := newPrefFixture(t)
	saved, err := f.prefs.Add(t.Context(), f.m.id, "Prefer table output", "", protocol.PreferenceSourceSettings, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- coordtransport.Call(t.Context(), f.sock, protocol.MethodAssistantForget, protocol.PreferenceIDParams{ID: saved.Preference.ID}, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.m.confirms.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no card was shown")
		}
		time.Sleep(time.Millisecond)
	}
	text := "Prefer short answers"
	if _, err = f.prefs.Update(t.Context(), f.m.id, protocol.PreferenceUpdateParams{ID: saved.Preference.ID, Text: &text}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.m.confirms.decide(f.m.confirms.snapshot()[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err = wait(t, done); errCode(err) != protocol.CodeInvalidState || !strings.Contains(err.Error(), "changed after it was shown") {
		t.Fatalf("forget of the changed preference = %v; want refused", err)
	}
	if got, err := f.prefs.Get(t.Context(), f.m.id, saved.Preference.ID); err != nil || got.Text != text {
		t.Fatalf("after the refused forget: %+v, %v; want the newer text kept", got, err)
	}
}

// With auto-approval on, a launch carrying a saved preference still shows
// its card; one without passes.
func TestAutoApprovalNeverPassesAPreference(t *testing.T) {
	f := newPrefFixture(t)
	f.say(1, "remember that I prefer table output")
	if _, err := f.remember(protocol.AssistantRememberParams{Text: "I prefer table output"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PutAssistantSettings(t.Context(), f.m.id, store.AssistantSettings{AutoApprove: true, ShareView: true, PreferencesEnabled: true}); err != nil {
		t.Fatal(err)
	}
	carrying := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "Fix the report. I PREFER TABLE\n  OUTPUT."}
	if err := callTool(t, f.sock, f.m, protocol.MethodRunLaunch, carrying, true, true); err != nil {
		t.Fatal(err)
	}
	plain := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "Fix the report."}
	if err := callTool(t, f.sock, f.m, protocol.MethodRunLaunch, plain, false, false); err != nil {
		t.Fatal(err)
	}
	got := actions(t, f.svc, f.m)
	if len(got) < 2 || got[len(got)-2][0].Approval != "card" || got[len(got)-1][0].Approval != "auto" {
		t.Fatalf("launch records = %+v; want the one carrying a preference carded and the other auto-approved", got)
	}
}

func TestTemporaryIsSetInSettingsAndEndsWithANewConversation(t *testing.T) {
	f := newPrefFixture(t)
	on, off := true, false
	got, err := f.svc.SetSettings(t.Context(), f.m.id, protocol.AssistantSettingsParams{Temporary: &on, PreferencesEnabled: &off})
	if err != nil || !got.Temporary || got.PreferencesEnabled {
		t.Fatalf("SetSettings = %+v, %v", got, err)
	}
	f.m.op.Lock()
	err = f.svc.resetLocked(t.Context(), f.m)
	f.m.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.host.discarded, []domain.MemberID{f.m.id}) {
		t.Fatalf("ending a temporary conversation discarded agent state for %v; want %v", f.host.discarded, f.m.id)
	}
	if got, err = f.svc.Settings(t.Context(), f.m.id); err != nil || got.Temporary || got.PreferencesEnabled {
		t.Fatalf("after a new conversation = %+v, %v; want temporary cleared and preferences still off", got, err)
	}
}

// A workspace's preferences apply only while its workspace is in view, and
// a workspace-scoped save takes the workspace the message was sent from.
func TestWorkspacePreferencesFollowTheView(t *testing.T) {
	f := newPrefFixture(t)
	ctx := t.Context()
	ws := &domain.Workspace{Name: "api", BaseBranch: domain.DefaultBaseBranch}
	if err := f.db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prefs.Add(ctx, f.m.id, "Prefer pnpm here", string(ws.ID), protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prefs.Add(ctx, f.m.id, "Prefer table output", "", protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.mine(); len(list) != 1 || list[0].Scope != "all" {
		t.Fatalf("with no workspace in view = %+v; want only the preference for all workspaces", list)
	}
	view := &protocol.AssistantViewContext{Version: 1, Route: protocol.AssistantViewEntry{Name: "workspace", WorkspaceID: string(ws.ID)}}
	f.m.holdView(view, viewFromNavigation, time.Now())
	if list, _ := f.mine(); len(list) != 2 {
		t.Fatalf("with api in view = %+v; want both", list)
	}

	f.say(1, "from now on prefer bun for scripts")
	if _, err := f.remember(protocol.AssistantRememberParams{Text: "prefer bun for scripts", WorkspaceScoped: true}); errCode(err) != protocol.CodeInvalidParams {
		t.Fatalf("workspace_scoped with a navigation view = %v; want refused", err)
	}
	f.m.holdView(view, viewFromPrompt, time.Now())
	saved, err := f.remember(protocol.AssistantRememberParams{Text: "prefer bun for scripts", WorkspaceScoped: true})
	if err != nil || saved.Preference.WorkspaceID != string(ws.ID) {
		t.Fatalf("workspace_scoped with the message's view = %+v, %v", saved, err)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Only lines the member typed can be quoted: not quoted lines or their lazy
// continuations, fenced blocks up to a fence as long as the one that opened
// them, or indented code.
func TestQuotedSkipsPastedText(t *testing.T) {
	const want = "prefer table output always"
	for name, message := range map[string]string{
		"quoted line":       "> prefer table output always",
		"lazy continuation": "> from the review:\nprefer table output always",
		"long fence":        "````\n```\nprefer table output always\n````",
		"tilde fence":       "~~~\n```\nprefer table output always\n~~~",
		"unclosed fence":    "```go\nprefer table output always",
		"indented code":     "see:\n    prefer table output always",
		"tab indented code": "\tprefer table output always",
	} {
		if span, err := quoted(message, want); err == nil {
			t.Errorf("%s: quoted = %q, want refused", name, span)
		}
	}
	for message, want := range map[string]string{
		"From now on: always use C++.":      "always use C++",
		"Remember (always use C#), please":  "always use C#",
		"Keep coverage at 90%. Thanks":      "Keep coverage at 90%",
		"I said \"always tag #ops first\"!": "always tag #ops first",
	} {
		if span, err := quoted(message, strings.Trim(want, "+#%")); err != nil || span != want {
			t.Errorf("quoted(%q) = %q, %v; want %q", message, span, err, want)
		}
	}
	for name, message := range map[string]string{
		"after a quote":     "> from the review\n\nPrefer table output, always.",
		"after a fence":     "````\nx\n```\n````\nPrefer table output, always.",
		"after indentation": "    x := 1\nPrefer table output, always.",
	} {
		if span, err := quoted(message, want); err != nil || span != "Prefer table output, always" {
			t.Errorf("%s: quoted = %q, %v; want the member's own span", name, span, err)
		}
	}
}

// A launch carries a preference when it holds the preference's words in
// order, whatever the punctuation and case, and not when they run into
// other words.
func TestCarriesPreferenceMatchesWholeWords(t *testing.T) {
	f := newPrefFixture(t)
	if _, err := f.prefs.Add(t.Context(), f.m.id, "Prefer table output", "", protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	for task, want := range map[string]bool{
		"Fix it. PREFER: table, output!":   true,
		"Fix it; prefer\ntable output":     true,
		"Fix it and prefer table outputs":  false,
		"Fix it and unprefer table output": false,
	} {
		params, _ := json.Marshal(protocol.RunLaunchParams{Task: task})
		if got := f.svc.carriesPreference(t.Context(), f.m.id, protocol.AssistantConfirmation{Method: protocol.MethodRunLaunch, Params: params}); got != want {
			t.Errorf("carriesPreference(%q) = %v, want %v", task, got, want)
		}
	}
}
