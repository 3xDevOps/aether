package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/secretscan"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// genericSecret is the gitleaks generic-api-key sample internal/secretscan
// tests with.
const genericSecret = "QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"

type fixture struct {
	t    *testing.T
	db   *store.DB
	log  *events.SQLiteLog
	bus  *events.InProc
	svc  *Service
	ws   *domain.Workspace
	dana *domain.Member
	seq  int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aether.db")
	db, err := storetest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log, err := events.OpenSQLiteLog(path)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	svc, err := New(Config{Store: db, Bus: bus, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ws := &domain.Workspace{Name: "api", BaseBranch: domain.DefaultBaseBranch}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, log: log, bus: bus, svc: svc, ws: ws}
	f.dana = f.member("Dana", domain.RoleCollaborator)
	return f
}

func (f *fixture) member(name string, role domain.Role) *domain.Member {
	f.t.Helper()
	f.seq++
	m := &domain.Member{DisplayName: name, TailnetLogin: fmt.Sprintf("member-%d@example.com", f.seq), Color: "#3cb44b", Role: role}
	if err := f.db.CreateMember(context.Background(), m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) run(owner *domain.Member, harness, task string) *domain.Run {
	f.t.Helper()
	f.seq++
	r := &domain.Run{WorkspaceID: f.ws.ID, MemberID: owner.ID, Task: task, Harness: harness, Mode: domain.LaunchTUI,
		Status: domain.RunRunning, Branch: fmt.Sprintf("aether/run-%d", f.seq)}
	if err := f.db.CreateRun(context.Background(), r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) finish(r *domain.Run, ago time.Duration) {
	f.t.Helper()
	at := time.Now().UTC().Add(-ago)
	if _, err := f.db.UpdateRunStatus(context.Background(), r.ID, domain.RunCompleted, "", "", nil, &at); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) agent(run *domain.Run, method string, params any) (json.RawMessage, error) {
	f.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		f.t.Fatal(err)
	}
	result, err := f.svc.HandleAgent(context.Background(), run.ID, method, raw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(result)
	if err != nil {
		f.t.Fatal(err)
	}
	return out, nil
}

func (f *fixture) agentAdd(run *domain.Run, p protocol.MemoryAddParams) (protocol.MemoryAddResult, error) {
	f.t.Helper()
	raw, err := f.agent(run, protocol.MethodMemoryAdd, p)
	var result protocol.MemoryAddResult
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

func (f *fixture) memberAdd(m *domain.Member, p protocol.MemoryAddParams) protocol.MemoryAddResult {
	f.t.Helper()
	p.WorkspaceID = string(f.ws.ID)
	result, err := f.svc.Add(context.Background(), m.ID, p)
	if err != nil {
		f.t.Fatalf("member add %q: %v", p.Text, err)
	}
	return result
}

func (f *fixture) changes() []events.Event {
	f.t.Helper()
	out, err := f.log.Read(context.Background(), events.Filter{Types: []events.Type{events.TypeMemoryChange}}, 0, 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func wantError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != code || rpcErr.Message != message {
		t.Fatalf("error = %v, want %d %q", err, code, message)
	}
}

func TestAgentAddFillsProvenanceAndListsSimilar(t *testing.T) {
	f := newFixture(t)
	run := f.run(f.dana, "codex", "fix reattach")
	if _, err := f.bus.Publish(context.Background(), events.Event{WorkspaceID: f.ws.ID, RunID: run.ID,
		Payload: events.RunDiffPayload{Head: "abc1234", Files: []events.FileDiffStat{{Path: "internal/scheduler/reattach.go"}}}}); err != nil {
		t.Fatal(err)
	}
	earlier := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha,
		Text: "Hold the scheduler lease before reattaching containers."})

	added, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindDecision,
		Text: "Reattach takes the scheduler lease first.", Files: []string{"internal/scheduler/reattach.go"}})
	if err != nil {
		t.Fatal(err)
	}
	item := added.Item
	if !strings.HasPrefix(item.ID, "note-") || item.WorkspaceID != string(f.ws.ID) || item.Source != protocol.MemorySourceAgent ||
		item.AuthorID != string(f.dana.ID) || item.RunID != string(run.ID) || item.Harness != "codex" || item.Commit != "abc1234" ||
		!slices.Equal(item.Files, []string{"internal/scheduler/reattach.go"}) || added.Duplicate {
		t.Fatalf("agent note = %+v", added)
	}
	if len(added.Similar) != 1 || added.Similar[0].ID != earlier.Item.ID {
		t.Fatalf("similar = %+v, want only %s", added.Similar, earlier.Item.ID)
	}
	again, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindDecision,
		Text: "reattach  takes the SCHEDULER lease first."})
	if err != nil || !again.Duplicate || again.Item.ID != item.ID {
		t.Fatalf("duplicate add = %+v, %v; want %s back", again, err, item.ID)
	}

	changes := f.changes()
	if len(changes) != 2 {
		t.Fatalf("memory.change events = %+v, want the member's add and the agent's add", changes)
	}
	agentChange := changes[1]
	payload, _ := agentChange.Payload.(events.MemoryChangePayload)
	if agentChange.RunID != run.ID || agentChange.ActorID != f.dana.ID ||
		payload != (events.MemoryChangePayload{Action: events.MemoryActionAdd, NoteID: item.ID, ActorID: f.dana.ID}) {
		t.Fatalf("agent add event = %+v", agentChange)
	}
}

func TestAgentReadsSearchShowAndBrief(t *testing.T) {
	f := newFixture(t)
	run := f.run(f.dana, "claude", "task")
	note := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: "Use bun in web/; never npm.", Pin: true})

	raw, err := f.agent(run, protocol.MethodMemorySearch, protocol.MemorySearchParams{Words: "bun"})
	var found protocol.MemorySearchResult
	if err != nil || json.Unmarshal(raw, &found) != nil || len(found.Items) != 1 || found.Items[0].ID != note.Item.ID {
		t.Fatalf("search = %s, %v", raw, err)
	}
	raw, err = f.agent(run, protocol.MethodMemoryShow, protocol.MemoryShowParams{ID: note.Item.ID})
	var shown protocol.MemoryShowResult
	if err != nil || json.Unmarshal(raw, &shown) != nil || shown.Item.PinnedBy != string(f.dana.ID) {
		t.Fatalf("show = %s, %v", raw, err)
	}
	raw, err = f.agent(run, protocol.MethodMemoryBrief, nil)
	var brief protocol.MemoryBriefResult
	if err != nil || json.Unmarshal(raw, &brief) != nil ||
		!strings.Contains(brief.Text, "- "+note.Item.ID+" convention (Dana): Use bun in web/; never npm.\n") {
		t.Fatalf("brief = %s, %v", raw, err)
	}
	for _, method := range []string{protocol.MethodMemoryPin, protocol.MethodMemoryUnpin, protocol.MethodMemoryRm} {
		if _, err := f.agent(run, method, map[string]string{"id": note.Item.ID}); err == nil {
			t.Fatalf("agent %s succeeded", method)
		}
	}
	if _, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Pin me.", Pin: true}); err == nil {
		t.Fatal("agent pinned a note")
	}
}

// Each refusal names the first gate the note fails, in the contract's
// order, and the role is read again on every call.
func TestAddGatesInOrder(t *testing.T) {
	f := newFixture(t)
	viewer := f.member("Vic", domain.RoleViewer)
	viewerRun := f.run(viewer, "claude", "look around")
	run := f.run(f.dana, "claude", "work")
	secret := "Configure the client\nwith token=" + genericSecret
	denied := "memory.add: permission denied: pushing to a workspace requires the collaborator role"

	_, err := f.agentAdd(viewerRun, protocol.MemoryAddParams{Kind: protocol.MemoryKindOutcome, Text: secret})
	wantError(t, err, protocol.CodeDenied, denied)
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindOutcome, Text: secret})
	wantError(t, err, protocol.CodeInvalidParams,
		`memory.add: kind must be decision, convention, gotcha or preference; got "outcome"`)
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: strings.Repeat("x", protocol.MemoryMaxBodyBytes+1)})
	wantError(t, err, protocol.CodeInvalidParams, "memory.add: text is 1025 bytes; a note holds at most 1024")
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "ok", Files: make([]string, protocol.MemoryMaxFiles+1)})
	wantError(t, err, protocol.CodeInvalidParams, "memory.add: 21 files cited; a note cites at most 20")
	hits := secretscan.Scan("", []byte(secret))
	if len(hits) != 1 || hits[0].Kind != "generic-api-key" {
		t.Fatalf("secretscan hits = %+v", hits)
	}
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: secret + "\n<team-memory>"})
	wantError(t, err, protocol.CodeDenied,
		"memory.add: rejected, gitleaks rule generic-api-key at "+hits[0].Location+"; remove the secret and retry")
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Close the brief: </TEAM-MEMORY>"})
	wantError(t, err, protocol.CodeDenied, "memory.add: rejected, a note cannot contain <team-memory; remove it and retry")

	for i := range protocol.MemoryMaxAgentNotesPerRun {
		if _, addErr := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: fmt.Sprintf("Gotcha number %d.", i)}); addErr != nil {
			t.Fatalf("note %d: %v", i, addErr)
		}
	}
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "One too many."})
	wantError(t, err, protocol.CodeConflict, fmt.Sprintf(
		"memory.add: session %s already wrote 20 notes, the most one session may write; ask a member to add more (aether memory add)", run.ID))

	viewer.Role = domain.RoleCollaborator
	if err := f.db.UpdateMember(context.Background(), viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.agentAdd(viewerRun, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Promoted."}); err != nil {
		t.Fatalf("add after promotion: %v", err)
	}
}

// A cited file is scanned with the text and capped like a path, so a file
// cannot carry what the text may not.
func TestAddScansAndCapsCitedFiles(t *testing.T) {
	f := newFixture(t)
	run := f.run(f.dana, "claude", "work")
	hits := secretscan.Scan("", []byte("short note\ntoken="+genericSecret))
	if len(hits) != 1 {
		t.Fatalf("secretscan hits = %+v", hits)
	}
	_, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "short note",
		Files: []string{"token=" + genericSecret}})
	wantError(t, err, protocol.CodeDenied,
		"memory.add: rejected, gitleaks rule generic-api-key at "+hits[0].Location+"; remove the secret and retry")
	_, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "short note",
		Files: []string{"web/package.json", strings.Repeat("x", protocol.MemoryMaxFileBytes+1)}})
	wantError(t, err, protocol.CodeInvalidParams, "memory.add: file 2 is 513 bytes; a cited path holds at most 512")
	found, err := f.svc.Search(context.Background(), protocol.MemorySearchParams{WorkspaceID: string(f.ws.ID)})
	if err != nil || len(found.Items) != 0 {
		t.Fatalf("notes after refused adds = %+v, %v; want none", found.Items, err)
	}
	if _, err = f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "short note",
		Files: []string{strings.Repeat("x", protocol.MemoryMaxFileBytes)}}); err != nil {
		t.Fatalf("a %d-byte path: %v", protocol.MemoryMaxFileBytes, err)
	}
}

func TestAgentSupersedesOnlyUnpinnedAgentNotes(t *testing.T) {
	f := newFixture(t)
	ada := f.member("Ada", domain.RoleCollaborator)
	writer, replacer := f.run(ada, "claude", "first"), f.run(ada, "codex", "second")
	agentNote, err := f.agentAdd(writer, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Use npm in web/."})
	if err != nil {
		t.Fatal(err)
	}
	pinnedNote, err := f.agentAdd(writer, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Run go test with -race."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Pin(context.Background(), f.dana.ID, protocol.MemoryPinParams{WorkspaceID: string(f.ws.ID), ID: pinnedNote.Item.ID}); err != nil {
		t.Fatal(err)
	}
	memberNote := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: "Squash before review."})

	for _, old := range []struct{ id, standing string }{
		{pinnedNote.Item.ID, "is pinned by Dana"},
		{memberNote.Item.ID, "was written by Dana"},
	} {
		_, refusal := f.agentAdd(replacer, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Replacement for " + old.id, Supersedes: old.id})
		wantError(t, refusal, protocol.CodeDenied, fmt.Sprintf(
			"memory.add: %s %s; ask a member to replace it (aether memory add --supersedes %s)", old.id, old.standing, old.id))
	}

	replaced, err := f.agentAdd(replacer, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
		Text: "Use bun in web/; never npm.", Supersedes: agentNote.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	shown, err := f.svc.Show(context.Background(), protocol.MemoryShowParams{WorkspaceID: string(f.ws.ID), ID: agentNote.Item.ID})
	if err != nil || shown.Item.SupersededBy != replaced.Item.ID || len(shown.Chain) != 1 {
		t.Fatalf("replaced note = %+v, %v", shown, err)
	}
	byMember := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha,
		Text: "Run go test with -race -count=1.", Supersedes: pinnedNote.Item.ID})
	if byMember.Item.Source != protocol.MemorySourceMember || byMember.Item.PinnedAt == nil || byMember.Item.PinnedBy != string(f.dana.ID) {
		t.Fatalf("member replacement = %+v, want it to carry the pin", byMember.Item)
	}
	shown, err = f.svc.Show(context.Background(), protocol.MemoryShowParams{WorkspaceID: string(f.ws.ID), ID: pinnedNote.Item.ID})
	if err != nil || shown.Item.PinnedAt != nil || shown.Item.SupersededBy != byMember.Item.ID {
		t.Fatalf("replaced pinned note = %+v, %v; want it unpinned and replaced", shown.Item, err)
	}
	var actions []events.MemoryAction
	for _, e := range f.changes() {
		actions = append(actions, e.Payload.(events.MemoryChangePayload).Action)
	}
	want := []events.MemoryAction{"add", "add", "pin", "add", "add", "supersede", "add", "supersede", "pin"}
	if !slices.Equal(actions, want) {
		t.Fatalf("memory.change actions = %v, want %v", actions, want)
	}
}

// An agent's replacement that another live note already holds would hide a
// note without writing one the run's limit counts, so it is refused.
func TestAgentSupersedeOntoDuplicateIsRefused(t *testing.T) {
	f := newFixture(t)
	writer, bad := f.run(f.dana, "claude", "first"), f.run(f.dana, "codex", "second")
	kept, err := f.agentAdd(bad, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Use npm in web/."})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.agentAdd(writer, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Run go test with -race."})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.agentAdd(bad, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "use NPM in web/.", Supersedes: target.Item.ID})
	wantError(t, err, protocol.CodeConflict, fmt.Sprintf(
		"memory.add: store: conflict: memory note %s already holds this text; ask a member to replace %s with it (aether memory add --supersedes %s)",
		kept.Item.ID, target.Item.ID, target.Item.ID))
	shown, err := f.svc.Show(context.Background(), protocol.MemoryShowParams{WorkspaceID: string(f.ws.ID), ID: target.Item.ID})
	if err != nil || shown.Item.InvalidAt != nil || shown.Item.SupersededBy != "" {
		t.Fatalf("target after a refused replacement = %+v, %v; want it live", shown.Item, err)
	}
	byMember := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "use NPM in web/.", Supersedes: target.Item.ID})
	if !byMember.Duplicate || byMember.Item.ID != kept.Item.ID {
		t.Fatalf("member replacement = %+v, want the live duplicate %s", byMember, kept.Item.ID)
	}
}

// Replacing an unpinned note with the text of a pinned one moves no pin,
// so the activity feed gets no pin event.
func TestSupersedeOntoPinnedDuplicateMovesNoPin(t *testing.T) {
	f := newFixture(t)
	bob := f.member("Bob", domain.RoleCollaborator)
	pinned := f.memberAdd(bob, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: "Use bun in web/; never npm.", Pin: true})
	loose := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: "Use npm in web/."})
	before := len(f.changes())
	replaced := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
		Text: "use bun in web/;  never npm.", Supersedes: loose.Item.ID})
	if !replaced.Duplicate || replaced.Item.ID != pinned.Item.ID || replaced.Item.PinnedBy != string(bob.ID) {
		t.Fatalf("replacement = %+v, want Bob's pinned %s", replaced, pinned.Item.ID)
	}
	var got []events.MemoryChangePayload
	for _, e := range f.changes()[before:] {
		got = append(got, e.Payload.(events.MemoryChangePayload))
	}
	want := []events.MemoryChangePayload{{Action: events.MemoryActionSupersede, NoteID: loose.Item.ID, ActorID: f.dana.ID}}
	if !slices.Equal(got, want) {
		t.Fatalf("memory.change = %+v, want %+v", got, want)
	}
}

func TestMembersPinUnpinAndRemove(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	viewer := f.member("Vic", domain.RoleViewer)
	run := f.run(f.dana, "claude", "work")
	ws := string(f.ws.ID)
	// A pin costs its title, at most 120 bytes, plus a 64-byte line.
	note := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
		Text: strings.Repeat("a", 120) + "\nThe body past the title costs nothing."})

	_, err := f.svc.Pin(ctx, viewer.ID, protocol.MemoryPinParams{WorkspaceID: ws, ID: note.Item.ID})
	wantError(t, err, protocol.CodeDenied, "memory.pin: permission denied: pushing to a workspace requires the collaborator role")
	if _, err = f.svc.Pin(ctx, f.dana.ID, protocol.MemoryPinParams{WorkspaceID: ws, ID: note.Item.ID}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: fmt.Sprintf("%03d", i) + strings.Repeat("c", 117), Pin: true})
	}
	_, err = f.svc.Add(ctx, f.dana.ID, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindGotcha,
		Text: strings.Repeat("b", 50), Pin: true})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeConflict || !strings.HasSuffix(rpcErr.Message,
		" is stored but not pinned: memory.pin: pinned workspace notes use 920 of 1024 bytes; this note needs 114; unpin one first") {
		t.Fatalf("pin over budget = %v", err)
	}
	unpinned, err := f.svc.Unpin(ctx, f.dana.ID, protocol.MemoryUnpinParams{WorkspaceID: ws, ID: note.Item.ID})
	if err != nil || unpinned.Item.PinnedAt != nil {
		t.Fatalf("unpin = %+v, %v", unpinned, err)
	}

	first, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "First."})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Second."})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Remove(ctx, viewer.ID, protocol.MemoryRmParams{WorkspaceID: ws, ID: note.Item.ID})
	wantError(t, err, protocol.CodeDenied, "memory.rm: permission denied: pushing to a workspace requires the collaborator role")
	removed, err := f.svc.Remove(ctx, f.dana.ID, protocol.MemoryRmParams{WorkspaceID: ws, RunID: string(run.ID)})
	wantIDs := []string{first.Item.ID, second.Item.ID}
	slices.Sort(wantIDs)
	if err != nil || !slices.Equal(removed.IDs, wantIDs) {
		t.Fatalf("rm --run = %+v, %v; want %v", removed, err, wantIDs)
	}
	if removed, err = f.svc.Remove(ctx, f.dana.ID, protocol.MemoryRmParams{WorkspaceID: ws, ID: note.Item.ID}); err != nil ||
		!slices.Equal(removed.IDs, []string{note.Item.ID}) {
		t.Fatalf("rm = %+v, %v", removed, err)
	}
	_, err = f.svc.Show(ctx, protocol.MemoryShowParams{WorkspaceID: ws, ID: note.Item.ID})
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeNotFound {
		t.Fatalf("show removed = %v", err)
	}
	var rms []string
	for _, e := range f.changes() {
		if p := e.Payload.(events.MemoryChangePayload); p.Action == events.MemoryActionRm {
			rms = append(rms, p.NoteID)
		}
	}
	if len(rms) != 3 {
		t.Fatalf("rm events = %v, want one per removed note", rms)
	}
}

func TestBriefShape(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ada := f.member("Ada", domain.RoleCollaborator)
	if text, err := f.svc.Brief(ctx, f.ws.ID, ada.ID, ""); err != nil || text != "" {
		t.Fatalf("empty workspace brief = %q, %v", text, err)
	}
	self := f.run(ada, "claude", "my own task")
	f.run(f.dana, "claude", "still going")
	team := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
		Text: "Use bun in web/; never npm.\nnpm writes a lockfile bun ignores.", Pin: true})
	mine := f.memberAdd(ada, protocol.MemoryAddParams{Kind: protocol.MemoryKindPreference, Scope: protocol.MemoryScopeMember,
		Text: "No emoji in commit messages.", Pin: true})
	f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindPreference, Scope: protocol.MemoryScopeMember,
		Text: "Dana's own preference.", Pin: true})
	done := f.run(f.dana, "codex", "Fix reattach lease race\nwith the details below")
	if _, err := f.agentAdd(done, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "An agent's gotcha."}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AppendCoordReport(ctx, &store.CoordReport{WorkspaceID: f.ws.ID, RunID: done.ID,
		Outcome: store.CoordOutcomeSuccess, Summary: "Fixed it.", IdempotencyKey: "success-1"}); err != nil {
		t.Fatal(err)
	}
	f.finish(done, 3*time.Hour+time.Minute)
	finished := time.Now().UTC().Add(-3 * time.Hour)
	if err := f.db.RecordRunPullRequest(ctx, &store.RunPullRequest{RunID: done.ID, Repository: "3xDevOps/aether", Number: 312,
		URL: "https://github.com/3xDevOps/aether/pull/312", State: "merged", MergedAt: &finished, ObservedAt: finished}); err != nil {
		t.Fatal(err)
	}

	text, err := f.svc.Brief(ctx, f.ws.ID, ada.ID, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := `<team-memory workspace="api">
` + coordhooks.MemoryRule + `
Pinned:
- ` + team.Item.ID + ` convention (Dana): Use bun in web/; never npm.
- ` + mine.Item.ID + ` preference (yours): No emoji in commit messages.
Recent sessions (48h):
- Dana codex success ` + "`" + done.Branch + "`" + ` PR #312 merged, 3h: "Fix reattach lease race"
1 live session: aether-internal status. 5 team notes (1 unpinned agent note): aether-internal memory search <words>
</team-memory>`
	if text != want {
		t.Fatalf("brief =\n%s\nwant\n%s", text, want)
	}
}

// With both pin budgets full and twenty finished runs the brief fits,
// keeps every pin and a run line, and carries no unpinned agent text, no
// report prose, no run title and nothing of its own run.
func TestBriefFitsFullPinBudgets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dana := f.member("Dana Longsurname-Over-Its-Cut", domain.RoleCollaborator)
	ada := f.member("Ada", domain.RoleCollaborator)
	self := f.run(ada, "claude", "CANARY-SELF-TASK")
	f.finish(self, 0)
	// A 64-byte title costs 128 bytes: 8 fill the workspace budget, 4 Ada's.
	title := func(i int) string { return fmt.Sprintf("Rule %02d ", i) + strings.Repeat("x", 56) }
	var pins []string
	for i := range 8 {
		pins = append(pins, f.memberAdd(dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
			Text: title(i) + "\nCANARY-PINNED-BODY", Pin: true}).Item.ID)
	}
	for i := 8; i < 12; i++ {
		pins = append(pins, f.memberAdd(ada, protocol.MemoryAddParams{Kind: protocol.MemoryKindPreference,
			Scope: protocol.MemoryScopeMember, Text: title(i), Pin: true}).Item.ID)
	}
	for _, scope := range []string{protocol.MemoryScopeWorkspace, protocol.MemoryScopeMember} {
		if _, err := f.svc.Add(ctx, ada.ID, protocol.MemoryAddParams{WorkspaceID: string(f.ws.ID), Kind: protocol.MemoryKindGotcha,
			Scope: scope, Text: "One more " + scope + " pin.", Pin: true}); err == nil {
			t.Fatalf("%s pin past a full budget succeeded", scope)
		}
	}
	for i := range 20 {
		r := f.run(dana, "codex", fmt.Sprintf(`Task %02d "quoted" typed by a member `, i)+strings.Repeat("y", 80))
		if err := f.db.SetRunTitle(ctx, r.ID, "CANARY-RUN-TITLE"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.agentAdd(r, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha,
			Text: fmt.Sprintf("CANARY-AGENT-NOTE %d", i)}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.AppendCoordReport(ctx, &store.CoordReport{WorkspaceID: f.ws.ID, RunID: r.ID, Outcome: store.CoordOutcomeFailure,
			Summary: "CANARY-REPORT-SUMMARY", NextAction: "CANARY-NEXT-ACTION", IdempotencyKey: "failure-1"}); err != nil {
			t.Fatal(err)
		}
		f.finish(r, time.Duration(i+1)*time.Hour)
	}

	text, err := f.svc.Brief(ctx, f.ws.ID, ada.ID, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > protocol.MemoryBriefMaxBytes {
		t.Fatalf("brief is %d bytes, over %d:\n%s", len(text), protocol.MemoryBriefMaxBytes, text)
	}
	for _, id := range pins {
		if !strings.Contains(text, "\n- "+id+" ") {
			t.Errorf("brief lacks pin %s:\n%s", id, text)
		}
	}
	for _, want := range []string{
		" convention (Dana Longsurname-Over-It): Rule 00 x", " preference (yours): Rule 08 x",
		"Recent sessions (48h):\n- Dana Longsurname-Over-It codex failure `", `, 1h: "Task 00 \"quoted\" typed by a member yyy`,
		"20 unpinned agent notes", "</team-memory>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("brief lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "did not fit") || strings.Contains(text, "CANARY") {
		t.Fatalf("brief dropped a pin, or carries a pinned body, unpinned agent text, report prose, a session title or its own run:\n%s", text)
	}
}

// A brief reaches terminals and launch argv, so a pinned title's control
// characters (C0, DEL and C1) become spaces.
func TestBriefStripsControlCharacters(t *testing.T) {
	f := newFixture(t)
	pinned := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
		Text: "\x1b]0;owned\x07\x1b[2J Use bun\x7f\u009b in web/\tnever npm", Pin: true})
	text, err := f.svc.Brief(context.Background(), f.ws.ID, f.dana.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "\n- "+pinned.Item.ID+" convention (Dana): ]0;owned [2J Use bun in web/ never npm\n") {
		t.Fatalf("brief lacks the cleaned pin line:\n%q", text)
	}
	if strings.ContainsFunc(text, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }) {
		t.Fatalf("brief carries a control character:\n%q", text)
	}
}

// A NUL is refused before the insert, so no note holds one and the
// similar-notes search cannot fail after the note is stored.
func TestAddRefusesNUL(t *testing.T) {
	f := newFixture(t)
	run := f.run(f.dana, "claude", "work")
	_, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Run make db\x00"})
	wantError(t, err, protocol.CodeInvalidParams, "memory.add: text contains a NUL byte; a note cannot hold one")
	if found, err := f.svc.Search(context.Background(), protocol.MemorySearchParams{WorkspaceID: string(f.ws.ID)}); err != nil || len(found.Items) != 0 {
		t.Fatalf("notes after a refused NUL = %+v, %v; want none", found.Items, err)
	}
}

// bypassedPins adds pinned rows the budgets never measured, as a direct
// insert would.
type bypassedPins struct {
	*store.DB
	extra []*store.Memory
}

func (b bypassedPins) ReadMemoryBrief(ctx context.Context, q store.MemoryBriefQuery) (*store.MemoryBrief, error) {
	brief, err := b.DB.ReadMemoryBrief(ctx, q)
	if err == nil {
		brief.Pinned = append(brief.Pinned, b.extra...)
	}
	return brief, err
}

func TestBriefDropsPinsOnlyPastBudgets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.run(f.dana, "codex", "Fix the lease")
	f.finish(r, time.Hour)
	kept := f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: "Use bun in web/; never npm.", Pin: true})
	var extra []*store.Memory
	for i := range 50 {
		extra = append(extra, &store.Memory{ID: fmt.Sprintf("note-bypass%05d", i), Kind: store.MemoryConvention,
			Scope: store.MemoryScopeWorkspace, Title: strings.Repeat("z", protocol.MemoryMaxTitleBytes), PinnedByName: "Dana"})
	}
	svc, err := New(Config{Store: bypassedPins{DB: f.db, extra: extra}, Bus: f.bus, Log: f.log})
	if err != nil {
		t.Fatal(err)
	}
	text, err := svc.Brief(ctx, f.ws.ID, f.dana.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > protocol.MemoryBriefMaxBytes || !strings.Contains(text, "- "+kept.Item.ID+" convention (Dana): Use bun in web/; never npm.\n") ||
		!strings.Contains(text, " more pinned notes did not fit\n") || strings.Contains(text, "Recent sessions") {
		t.Fatalf("brief over the pin budgets is %d bytes:\n%s", len(text), text)
	}
}

// The pin budgets (1024 + 512) leave 512 bytes of the 3072-byte brief for
// its frame and 1024 for run lines; the frame keeps to its share whatever
// the workspace is named and however many notes it holds.
func TestBriefFrameFitsItsShare(t *testing.T) {
	head, tail := frame(strings.Repeat("\"é\x00 ", 100), 9999999, 9999999, 9999999)
	if size := len(head) + len("Pinned:\n") + len("Recent sessions (48h):\n") + len(tail); size > 512 {
		t.Fatalf("frame is %d bytes, over 512:\n%s%s", size, head, tail)
	}
}

func TestSearchFiltersSourceAndReplaced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	run := f.run(f.dana, "claude", "work")
	agentNote, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Use npm in web/."})
	if err != nil {
		t.Fatal(err)
	}
	f.memberAdd(f.dana, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention, Text: "Use bun in web/.", Supersedes: agentNote.Item.ID})
	ws := string(f.ws.ID)
	for _, c := range []struct {
		p    protocol.MemorySearchParams
		want []string
	}{
		{protocol.MemorySearchParams{WorkspaceID: ws, Source: protocol.MemorySourceAgent}, nil},
		{protocol.MemorySearchParams{WorkspaceID: ws, Source: protocol.MemorySourceAgent, Replaced: true}, []string{agentNote.Item.ID}},
	} {
		found, searchErr := f.svc.Search(ctx, c.p)
		var got []string
		for _, item := range found.Items {
			got = append(got, item.ID)
		}
		if searchErr != nil || !slices.Equal(got, c.want) {
			t.Errorf("search %+v = %v, %v; want %v", c.p, got, searchErr, c.want)
		}
	}
	_, err = f.svc.Search(ctx, protocol.MemorySearchParams{WorkspaceID: ws, Source: "lore"})
	wantError(t, err, protocol.CodeInvalidParams, `memory.search: source must be agent, member, report or answer; got "lore"`)
}

func TestAgentAddFindsPrimaryHeadBehindManyExtraSnapshots(t *testing.T) {
	f := newFixture(t)
	run := f.run(f.dana, "codex", "fix reattach")
	publish := func(p events.RunDiffPayload) {
		t.Helper()
		if _, err := f.bus.Publish(context.Background(), events.Event{WorkspaceID: f.ws.ID, RunID: run.ID, Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	publish(events.RunDiffPayload{Head: "abc1234"})
	for range 70 {
		publish(events.RunDiffPayload{Head: "fff0000", Repo: "ws-extra"})
	}
	added, err := f.agentAdd(run, protocol.MemoryAddParams{Kind: protocol.MemoryKindDecision, Text: "Keep the primary commit link."})
	if err != nil || added.Item.Commit != "abc1234" {
		t.Fatalf("agent note = %+v, %v; want commit abc1234", added.Item, err)
	}
}
