package coord

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/memory"
	"github.com/3xDevOps/Aether/internal/protocol"
)

var memoryMethods = []string{protocol.MethodMemoryAdd, protocol.MethodMemorySearch, protocol.MethodMemoryShow, protocol.MethodMemoryBrief}

// newMemoryHarness serves real team memory on every run's socket, with
// conflict coordination disabled to show memory does not depend on it.
func newMemoryHarness(t *testing.T, runs int) *coordHarness {
	t.Helper()
	h := newHarness(t, runs, func(c *Config) { c.Disabled = true })
	log, err := events.OpenSQLiteLog(filepath.Join(h.dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	mem, err := memory.New(memory.Config{Store: h.db, Bus: h.bus, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.cfg.Memory = mem
	h.start()
	for _, r := range h.runs {
		if _, err := h.svc.Provision(t.Context(), r.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func wantRPCError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != code || (message != "" && rpcErr.Message != message) {
		t.Fatalf("error = %v, want %d %q", err, code, message)
	}
}

func TestMemoryOverRunSocket(t *testing.T) {
	h := newMemoryHarness(t, 1)
	client := h.dial(t, h.run(0))
	var status protocol.CoordStatusResult
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil {
		t.Fatal(err)
	}
	for _, method := range memoryMethods {
		if !slices.Contains(status.Capabilities, method) {
			t.Errorf("status capabilities %v lack %s", status.Capabilities, method)
		}
	}

	var added protocol.MemoryAddResult
	if err := client.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{Kind: protocol.MemoryKindConvention,
		Text: "Use bun in web/; never npm.", Files: []string{"web/package.json"}}, &added); err != nil {
		t.Fatal(err)
	}
	if added.Item.RunID != string(h.run(0)) || added.Item.AuthorID != string(h.runs[0].MemberID) ||
		added.Item.WorkspaceID != string(h.workspace) || added.Item.Harness != "claude" || added.Item.Source != protocol.MemorySourceAgent {
		t.Fatalf("added = %+v, want it attributed to the socket's run", added.Item)
	}
	var found protocol.MemorySearchResult
	if err := client.Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{Words: "npm"}, &found); err != nil ||
		len(found.Items) != 1 || found.Items[0].ID != added.Item.ID {
		t.Fatalf("search = %+v, %v", found, err)
	}
	var shown protocol.MemoryShowResult
	if err := client.Call(protocol.MethodMemoryShow, protocol.MemoryShowParams{ID: added.Item.ID}, &shown); err != nil ||
		shown.Item.ID != added.Item.ID || !slices.Equal(shown.Item.Files, []string{"web/package.json"}) {
		t.Fatalf("show = %+v, %v", shown, err)
	}
	var brief protocol.MemoryBriefResult
	if err := client.Call(protocol.MethodMemoryBrief, nil, &brief); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(brief.Text, `<team-memory workspace="proj">`) ||
		!strings.Contains(brief.Text, "1 team note (1 unpinned agent note)") || strings.Contains(brief.Text, "never npm") {
		t.Fatalf("brief = %q", brief.Text)
	}

	for method, params := range map[string]any{
		protocol.MethodMemorySearch: map[string]string{"workspace_id": "other"},
		protocol.MethodMemoryShow:   map[string]string{"RUN_ID": "run-other", "id": added.Item.ID},
		protocol.MethodMemoryBrief:  map[string]string{"Member_ID": "mem-other"},
		protocol.MethodMemoryAdd:    []string{"not an object"},
	} {
		wantRPCError(t, client.Call(method, params, nil), protocol.CodeInvalidParams, "")
	}
	for _, method := range []string{protocol.MethodMemoryPin, protocol.MethodMemoryUnpin, protocol.MethodMemoryRm} {
		wantRPCError(t, client.Call(method, map[string]string{"id": added.Item.ID}, nil), protocol.CodeMethodNotFound, "method not found: "+method)
	}

	if _, err := h.db.UpdateRunStatus(t.Context(), h.run(0), domain.RunCompleted, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	wantRPCError(t, client.Call(protocol.MethodMemorySearch, nil, nil), protocol.CodeUnavailable, "memory.search: session has finished")
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil || slices.Contains(status.Capabilities, protocol.MethodMemorySearch) {
		t.Fatalf("finished session capabilities = %v, %v", status.Capabilities, err)
	}
}

func TestMemoryDeniesViewerOwnedRun(t *testing.T) {
	h := newMemoryHarness(t, 1)
	h.ownBy(0, h.member(domain.RoleViewer).ID, false)
	client := h.dial(t, h.run(0))
	var status protocol.CoordStatusResult
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(status.Capabilities, protocol.MethodMemoryAdd) || !slices.Contains(status.Capabilities, protocol.MethodMemorySearch) {
		t.Fatalf("viewer-owned run capabilities = %v, want reads without memory.add", status.Capabilities)
	}
	err := client.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Viewers cannot write."}, nil)
	wantRPCError(t, err, protocol.CodeDenied, "memory.add: permission denied: pushing to a workspace requires the collaborator role")
}

func TestUnconfiguredMemoryIsNotDispatched(t *testing.T) {
	h := newHarness(t, 1)
	h.start()
	if _, err := h.svc.Provision(t.Context(), h.run(0), nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, h.run(0))
	for _, method := range memoryMethods {
		wantRPCError(t, client.Call(method, nil, nil), protocol.CodeMethodNotFound, "method not found: "+method)
	}
}

func TestIsolatedRunCannotWriteWhereOtherRunsRead(t *testing.T) {
	h := newMemoryHarness(t, 1)
	isolated := *h.runs[0]
	isolated.ID, isolated.Permission = "", domain.PermissionIsolated
	if err := h.db.CreateRun(t.Context(), &isolated); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Provision(t.Context(), isolated.ID, nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, isolated.ID)
	err := client.Call(protocol.MethodCoordSend, sendParams(h.run(0), "hello"), nil)
	wantRPCError(t, err, protocol.CodeDenied, "coord.send: an isolated session cannot message other sessions or write team memory")
	err = client.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{Kind: protocol.MemoryKindGotcha, Text: "Planted note."}, nil)
	wantRPCError(t, err, protocol.CodeDenied, "memory.add: an isolated session cannot message other sessions or write team memory")
	if err := client.Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{Words: "note"}, nil); err != nil {
		t.Fatalf("memory.search from an isolated session: %v", err)
	}
}
