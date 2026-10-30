package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/memory"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestMemoryMembersCurateAndViewersRead drives team memory the way members
// do over SSH: a collaborator adds a pinned note that the brief then
// carries, a viewer reads but every write is refused with the permission
// rule, removing a run's notes lists what went, and every write lands in
// the activity feed naming who made it.
func TestMemoryMembersCurateAndViewersRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, svc, log := newMemoryEnv(t)
	var err error
	ws := string(e.ws.ID)
	danaKey, dana := addMember(t, e, "Dana", domain.RoleCollaborator, false)
	vicKey, _ := addMember(t, e, "Vic", domain.RoleViewer, false)
	danaC, vicC := controlAs(t, e, danaKey), controlAs(t, e, vicKey)

	var added protocol.MemoryAddResult
	if err = danaC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{
		WorkspaceID: ws, Kind: protocol.MemoryKindConvention, Text: "Use bun in web/; never npm.", Pin: true,
	}, &added); err != nil {
		t.Fatalf("memory.add: %v", err)
	}
	pinned := added.Item
	if pinned.PinnedAt == nil || pinned.PinnedBy != string(dana.ID) || pinned.Source != protocol.MemorySourceMember {
		t.Fatalf("added note = %+v, want a member note pinned by Dana", pinned)
	}

	var brief protocol.MemoryBriefResult
	if err = vicC.Call(protocol.MethodMemoryBrief, protocol.MemoryBriefParams{WorkspaceID: ws}, &brief); err != nil {
		t.Fatalf("memory.brief: %v", err)
	}
	if line := "- " + pinned.ID + " convention (Dana): Use bun in web/; never npm.\n"; !strings.HasPrefix(brief.Text, `<team-memory workspace="proj">`) ||
		!strings.Contains(brief.Text, line) {
		t.Fatalf("brief =\n%s\nwant the workspace frame and %q", brief.Text, line)
	}

	var found protocol.MemorySearchResult
	if err = vicC.Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: ws, Words: "bun"}, &found); err != nil {
		t.Fatalf("viewer memory.search: %v", err)
	}
	if len(found.Items) != 1 || found.Items[0].ID != pinned.ID {
		t.Fatalf("viewer search = %+v, want %s", found.Items, pinned.ID)
	}
	const denied = "permission denied: pushing to a workspace requires the collaborator role"
	for method, params := range map[string]any{
		protocol.MethodMemoryAdd:   protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindGotcha, Text: "Viewers write too."},
		protocol.MethodMemoryPin:   protocol.MemoryPinParams{WorkspaceID: ws, ID: pinned.ID},
		protocol.MethodMemoryUnpin: protocol.MemoryUnpinParams{WorkspaceID: ws, ID: pinned.ID},
		protocol.MethodMemoryRm:    protocol.MemoryRmParams{WorkspaceID: ws, ID: pinned.ID},
	} {
		var pe *protocol.Error
		if err = vicC.Call(method, params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied || pe.Message != method+": "+denied {
			t.Fatalf("viewer %s = %v, want CodeDenied %q", method, err, method+": "+denied)
		}
	}

	var unpinned protocol.MemoryUnpinResult
	if err = danaC.Call(protocol.MethodMemoryUnpin, protocol.MemoryUnpinParams{WorkspaceID: ws, ID: pinned.ID}, &unpinned); err != nil {
		t.Fatalf("memory.unpin: %v", err)
	}
	if unpinned.Item.PinnedAt != nil {
		t.Fatalf("unpinned note still pinned: %+v", unpinned.Item)
	}
	var replaced protocol.MemoryAddResult
	if err = danaC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{
		WorkspaceID: ws, Kind: protocol.MemoryKindConvention, Text: "Use bun in web/ and docs/; never npm.", Supersedes: pinned.ID,
	}, &replaced); err != nil {
		t.Fatalf("memory.add --supersedes: %v", err)
	}

	var agentNotes []string
	for _, text := range []string{"The reattach lease races the sweeper.", "Retry the lease once before failing."} {
		params, _ := json.Marshal(protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: text})
		out, aerr := svc.HandleAgent(ctx, e.run.ID, protocol.MethodMemoryAdd, params)
		if aerr != nil {
			t.Fatalf("agent memory.add: %v", aerr)
		}
		agentNotes = append(agentNotes, out.(protocol.MemoryAddResult).Item.ID)
	}
	byID := slices.Sorted(slices.Values(agentNotes))
	var removed protocol.MemoryRmResult
	if err = danaC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, RunID: string(e.run.ID)}, &removed); err != nil {
		t.Fatalf("memory.rm --run: %v", err)
	}
	if !slices.Equal(removed.IDs, byID) {
		t.Fatalf("removed = %v, want the session's notes %v", removed.IDs, byID)
	}
	if err = danaC.Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: ws, All: true}, &found); err != nil {
		t.Fatalf("memory.search --all: %v", err)
	}
	var left []string
	for _, item := range found.Items {
		left = append(left, item.ID)
	}
	if want := []string{replaced.Item.ID, pinned.ID}; !slices.Equal(left, want) {
		t.Fatalf("notes left = %v, want %v", left, want)
	}

	changes, err := log.Read(ctx, events.Filter{Workspace: e.ws.ID, Types: []events.Type{events.TypeMemoryChange}}, 0, 0, 100)
	if err != nil {
		t.Fatalf("read memory.change events: %v", err)
	}
	type change struct {
		action events.MemoryAction
		note   string
		actor  domain.MemberID
		run    domain.RunID
	}
	var got []change
	for _, ev := range changes {
		p := ev.Payload.(events.MemoryChangePayload)
		if ev.ActorID != p.ActorID {
			t.Fatalf("event actor %q differs from payload actor %q", ev.ActorID, p.ActorID)
		}
		got = append(got, change{p.Action, p.NoteID, p.ActorID, ev.RunID})
	}
	want := []change{
		{events.MemoryActionAdd, pinned.ID, dana.ID, ""},
		{events.MemoryActionPin, pinned.ID, dana.ID, ""},
		{events.MemoryActionUnpin, pinned.ID, dana.ID, ""},
		{events.MemoryActionAdd, replaced.Item.ID, dana.ID, ""},
		{events.MemoryActionSupersede, pinned.ID, dana.ID, ""},
	}
	for _, id := range agentNotes {
		want = append(want, change{events.MemoryActionAdd, id, e.member.ID, e.run.ID})
	}
	for _, id := range byID {
		want = append(want, change{events.MemoryActionRm, id, dana.ID, ""})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("memory.change events = %+v, want %+v", got, want)
	}
}

// newMemoryEnv serves team memory over a test server whose own member is an
// admin.
func newMemoryEnv(t *testing.T) (*testEnv, *memory.Service, *events.SQLiteLog) {
	t.Helper()
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	var svc *memory.Service
	e := newTestEnv(t, func(c *Config) {
		c.Bus = bus
		db, ok := c.Store.(*store.DB)
		if !ok {
			t.Fatalf("store is %T, want *store.DB", c.Store)
		}
		if svc, err = memory.New(memory.Config{Store: db, Bus: bus, Log: log}); err != nil {
			t.Fatalf("memory: %v", err)
		}
		c.Services.Memory = svc
	})
	return e, svc, log
}

// TestMemoryMemberNotesStayTheirOwners checks that every member reads a
// member note but only its owner or an admin reads the owner's brief or
// pins, unpins, removes or replaces the note, from a member or an agent,
// while workspace notes stay everyone's to change and undo but not to take
// for oneself.
func TestMemoryMemberNotesStayTheirOwners(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, svc, _ := newMemoryEnv(t)
	ws := string(e.ws.ID)
	aliceKey, alice := addMember(t, e, "Alice", domain.RoleCollaborator, false)
	bobKey, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	vicKey, _ := addMember(t, e, "Vic", domain.RoleViewer, false)
	aliceC, bobC, vicC, adminC := controlAs(t, e, aliceKey), controlAs(t, e, bobKey), controlAs(t, e, vicKey), controlClient(t, e)
	denied := func(who string, err error, want string) {
		t.Helper()
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied || pe.Message != want {
			t.Fatalf("%s = %v, want CodeDenied %q", who, err, want)
		}
	}

	var mine protocol.MemoryAddResult
	if err := aliceC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindPreference,
		Scope: protocol.MemoryScopeMember, Text: "Prefer table output.", Pin: true}, &mine); err != nil {
		t.Fatalf("alice memory.add --me --pin: %v", err)
	}
	note := mine.Item.ID
	line := "- " + note + " preference (yours): Prefer table output.\n"

	const briefDenied = "memory.brief: another member's brief: permission denied: workspace administration requires the admin role"
	others := protocol.MemoryBriefParams{WorkspaceID: ws, MemberID: string(alice.ID)}
	denied("bob memory.brief --member alice", bobC.Call(protocol.MethodMemoryBrief, others, nil), briefDenied)
	denied("viewer memory.brief --member alice", vicC.Call(protocol.MethodMemoryBrief, others, nil), briefDenied)
	for who, c := range map[string]*protocol.Client{"alice": aliceC, "admin": adminC} {
		var brief protocol.MemoryBriefResult
		if err := c.Call(protocol.MethodMemoryBrief, others, &brief); err != nil || !strings.Contains(brief.Text, line) {
			t.Fatalf("%s memory.brief --member alice = %q, %v; want %q", who, brief.Text, err, line)
		}
	}

	var found protocol.MemoryShowResult
	if err := bobC.Call(protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: ws, ID: note}, &found); err != nil || found.Item.Body != "Prefer table output." {
		t.Fatalf("bob memory.show of alice's member note = %+v, %v; want it readable", found.Item, err)
	}
	rule := func(method, verb string) string {
		return method + ": permission denied: " + note + " is a member note of Alice; only they or an admin may " + verb + " it"
	}
	denied("bob memory.pin", bobC.Call(protocol.MethodMemoryPin, protocol.MemoryPinParams{WorkspaceID: ws, ID: note, Scope: protocol.MemoryScopeWorkspace}, nil),
		rule(protocol.MethodMemoryPin, "pin"))
	denied("bob memory.unpin", bobC.Call(protocol.MethodMemoryUnpin, protocol.MemoryUnpinParams{WorkspaceID: ws, ID: note}, nil),
		rule(protocol.MethodMemoryUnpin, "unpin"))
	denied("bob memory.rm", bobC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, ID: note}, nil),
		rule(protocol.MethodMemoryRm, "remove"))
	denied("bob memory.add --supersedes", bobC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws,
		Kind: protocol.MemoryKindPreference, Text: "Prefer JSON output.", Supersedes: note}, nil), rule(protocol.MethodMemoryAdd, "replace"))
	if err := bobC.Call(protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: ws, ID: note}, &found); err != nil ||
		found.Item.PinnedBy != string(alice.ID) || found.Item.Scope != protocol.MemoryScopeMember || found.Item.SupersededBy != "" {
		t.Fatalf("alice's note after bob's refused changes = %+v, %v; want it live and pinned for alice", found.Item, err)
	}

	var replaced protocol.MemoryAddResult
	if err := aliceC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindPreference,
		Scope: protocol.MemoryScopeMember, Text: "Prefer table output with headers.", Supersedes: note}, &replaced); err != nil {
		t.Fatalf("alice memory.add --supersedes: %v", err)
	}
	if replaced.Item.PinnedBy != string(alice.ID) || replaced.Item.Scope != protocol.MemoryScopeMember {
		t.Fatalf("alice's replacement = %+v, want it pinned for alice", replaced.Item)
	}
	note = replaced.Item.ID
	var unpinned protocol.MemoryUnpinResult
	if err := adminC.Call(protocol.MethodMemoryUnpin, protocol.MemoryUnpinParams{WorkspaceID: ws, ID: note}, &unpinned); err != nil || unpinned.Item.PinnedAt != nil {
		t.Fatalf("admin memory.unpin = %+v, %v", unpinned.Item, err)
	}
	denied("bob memory.pin of alice's unpinned note", bobC.Call(protocol.MethodMemoryPin, protocol.MemoryPinParams{WorkspaceID: ws, ID: note}, nil),
		rule(protocol.MethodMemoryPin, "pin"))
	if err := aliceC.Call(protocol.MethodMemoryPin, protocol.MemoryPinParams{WorkspaceID: ws, ID: note}, nil); err != nil {
		t.Fatalf("alice memory.pin: %v", err)
	}
	for _, scope := range []string{"", protocol.MemoryScopeMember} {
		denied("admin memory.pin --me of alice's member note", adminC.Call(protocol.MethodMemoryPin,
			protocol.MemoryPinParams{WorkspaceID: ws, ID: note, Scope: scope}, nil), rule(protocol.MethodMemoryPin, "pin"))
	}
	if err := adminC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, ID: note}, nil); err != nil {
		t.Fatalf("admin memory.rm: %v", err)
	}

	aliceRun := &domain.Run{WorkspaceID: e.ws.ID, MemberID: alice.ID, Task: "tables", Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-a-tables"}
	bobRun := &domain.Run{WorkspaceID: e.ws.ID, MemberID: bob.ID, Task: "json", Harness: "codex",
		Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-b-json"}
	for _, r := range []*domain.Run{aliceRun, bobRun} {
		if err := e.store.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	agentAdd := func(run domain.RunID, p protocol.MemoryAddParams) (protocol.MemoryAddResult, error) {
		raw, _ := json.Marshal(p)
		out, err := svc.HandleAgent(ctx, run, protocol.MethodMemoryAdd, raw)
		if err != nil {
			return protocol.MemoryAddResult{}, err
		}
		return out.(protocol.MemoryAddResult), nil
	}
	agents, err := agentAdd(aliceRun.ID, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Scope: protocol.MemoryScopeMember, Text: "The table writer pads to 80 columns."})
	if err != nil {
		t.Fatalf("alice's agent memory.add --me: %v", err)
	}
	_, err = agentAdd(bobRun.ID, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "The table writer pads to 120 columns.", Supersedes: agents.Item.ID})
	denied("bob's agent memory.add --supersedes", err, "memory.add: "+agents.Item.ID+" is a member note of Alice from session "+string(aliceRun.ID)+
		"; ask a member to replace it (aether memory add --supersedes "+agents.Item.ID+")")
	denied("bob memory.rm --run alice's run", bobC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, RunID: string(aliceRun.ID)}, nil),
		"memory.rm: permission denied: "+agents.Item.ID+" is a member note of Alice; only they or an admin may remove it")
	if _, err = agentAdd(aliceRun.ID, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Scope: protocol.MemoryScopeMember,
		Text: "The table writer pads to 100 columns.", Supersedes: agents.Item.ID}); err != nil {
		t.Fatalf("alice's agent replacing its own member note: %v", err)
	}

	var shared, edited protocol.MemoryAddResult
	if err = aliceC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindConvention,
		Text: "Use bun in web/.", Pin: true}, &shared); err != nil {
		t.Fatalf("alice memory.add --pin: %v", err)
	}
	if err = bobC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindConvention,
		Text: "Use bun in web/ and docs/.", Supersedes: shared.Item.ID}, &edited); err != nil {
		t.Fatalf("bob replacing alice's workspace note: %v", err)
	}
	if err = bobC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, ID: edited.Item.ID}, nil); err != nil {
		t.Fatalf("bob removing his replacement: %v", err)
	}
	if err = bobC.Call(protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: ws, ID: shared.Item.ID}, &found); err != nil ||
		found.Item.SupersededBy != "" || found.Item.InvalidAt != nil {
		t.Fatalf("alice's workspace note after bob removed its replacement = %+v, %v; want it live again", found.Item, err)
	}
	denied("bob memory.pin --me of alice's workspace note", bobC.Call(protocol.MethodMemoryPin, protocol.MemoryPinParams{WorkspaceID: ws,
		ID: shared.Item.ID, Scope: protocol.MemoryScopeMember}, nil),
		"memory.pin: permission denied: "+shared.Item.ID+" is a workspace note by Alice; only they or an admin may pin it for themselves")
	if err = bobC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, ID: shared.Item.ID}, nil); err != nil {
		t.Fatalf("bob removing alice's workspace note: %v", err)
	}

	var hers, his protocol.MemoryAddResult
	if err = aliceC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindPreference,
		Scope: protocol.MemoryScopeMember, Text: "Prefer short commit subjects."}, &hers); err != nil {
		t.Fatalf("alice memory.add --me: %v", err)
	}
	if err = bobC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindPreference,
		Scope: protocol.MemoryScopeMember, Text: "Prefer long commit subjects.", Pin: true}, &his); err != nil {
		t.Fatalf("bob memory.add --me --pin: %v", err)
	}
	denied("bob moving his pin onto alice's note by its text", bobC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws,
		Kind: protocol.MemoryKindPreference, Scope: protocol.MemoryScopeMember, Text: "Prefer short commit subjects.", Supersedes: his.Item.ID}, nil),
		"memory.add: permission denied: "+hers.Item.ID+" is a member note of Alice; only they or an admin may replace it")

	var bobs protocol.MemoryAddResult
	if err = bobC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindPreference,
		Text: "Prefer squash merges."}, &bobs); err != nil {
		t.Fatalf("bob memory.add: %v", err)
	}
	if err = aliceC.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindPreference,
		Scope: protocol.MemoryScopeMember, Text: "Prefer squash merges.", Supersedes: hers.Item.ID}, nil); err != nil {
		t.Fatalf("alice replacing her note with bob's text: %v", err)
	}
	if err = bobC.Call(protocol.MethodMemoryRm, protocol.MemoryRmParams{WorkspaceID: ws, ID: bobs.Item.ID}, nil); err != nil {
		t.Fatalf("bob removing his note: %v", err)
	}
	if err = bobC.Call(protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: ws, ID: hers.Item.ID}, &found); err != nil || found.Item.InvalidAt == nil {
		t.Fatalf("alice's note after bob removed its replacement = %+v, %v; want it still replaced", found.Item, err)
	}
}

func TestMemoryUnavailableWithoutService(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	var pe *protocol.Error
	err := controlClient(t, e).Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: string(e.ws.ID)}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable || pe.Message != "memory.search: team memory is not available" {
		t.Fatalf("memory.search = %v, want CodeUnavailable", err)
	}
}
