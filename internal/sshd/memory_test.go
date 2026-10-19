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
		t.Fatalf("removed = %v, want the run's notes %v", removed.IDs, byID)
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

func TestMemoryUnavailableWithoutService(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	var pe *protocol.Error
	err := controlClient(t, e).Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: string(e.ws.ID)}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable || pe.Message != "memory.search: team memory is not available" {
		t.Fatalf("memory.search = %v, want CodeUnavailable", err)
	}
}
