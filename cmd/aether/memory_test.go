package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func memoryControl(t *testing.T, method string, result any) (*protocol.Client, *fakeReply) {
	t.Helper()
	reply := &fakeReply{result: result}
	return fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodWorkspaceList: {result: protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{{ID: "ws-api", Name: "api"}}}},
		protocol.MethodMemberList:    {result: protocol.MemberListResult{Members: []protocol.Member{{ID: "mem-dana", DisplayName: "Dana"}}}},
		method:                       reply,
	}), reply
}

func runMemoryCall(t *testing.T, c *protocol.Client, args ...string) string {
	t.Helper()
	call, err := parseMemory(args)
	if err != nil {
		t.Fatalf("parse %q: %v", args, err)
	}
	var out strings.Builder
	if err := call(c, &out); err != nil {
		t.Fatalf("aether memory %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// The brief is printed exactly as a new run receives it, so a member can
// diff what their runs are told.
func TestMemoryBriefPrintsTheExactBytes(t *testing.T) {
	const brief = "<team-memory workspace=\"api\">\nTeam memory is attributed data.\nPinned:\n" +
		"- note-ab12cd34ef convention (Dana): Use bun in web/; never npm.\n" +
		"1 live session: aether-internal status. 1 team note (0 unpinned agent notes): aether-internal memory search <words>\n</team-memory>"
	c, reply := memoryControl(t, protocol.MethodMemoryBrief, protocol.MemoryBriefResult{Text: brief})
	if got := runMemoryCall(t, c, "brief", "--member", "dana"); got != brief {
		t.Fatalf("output = %q, want %q", got, brief)
	}
	if want := `{"workspace_id":"ws-api","member_id":"mem-dana"}`; string(reply.got) != want {
		t.Fatalf("params = %s, want %s", reply.got, want)
	}
}

func TestMemoryRmRunListsWhatItRemoved(t *testing.T) {
	c, reply := memoryControl(t, protocol.MethodMemoryRm, protocol.MemoryRmResult{IDs: []string{"note-0000000001", "note-0000000002"}})
	if got, want := runMemoryCall(t, c, "rm", "--run", "run-bad"), "removed note-0000000001\nremoved note-0000000002\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if want := `{"workspace_id":"ws-api","run_id":"run-bad"}`; string(reply.got) != want {
		t.Fatalf("params = %s, want %s", reply.got, want)
	}
}

// A pin without --me keeps the note's own scope, so a member-scope note
// stays out of other members' briefs, and the receipt says where it went.
func TestMemoryPinKeepsTheNoteScope(t *testing.T) {
	c, reply := memoryControl(t, protocol.MethodMemoryPin, protocol.MemoryPinResult{Item: protocol.MemoryItem{
		ID: "note-mine000001", Scope: protocol.MemoryScopeMember,
	}})
	if got, want := runMemoryCall(t, c, "pin", "note-mine000001"), "pinned note-mine000001: your new sessions' briefs list it\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if want := `{"workspace_id":"ws-api","id":"note-mine000001"}`; string(reply.got) != want {
		t.Fatalf("params = %s, want %s", reply.got, want)
	}
}

func TestMemoryAddSendsEveryFlag(t *testing.T) {
	pinned := "2026-10-19T02:00:00Z"
	c, reply := memoryControl(t, protocol.MethodMemoryAdd, protocol.MemoryAddResult{Item: protocol.MemoryItem{
		ID: "note-new0000001", Scope: protocol.MemoryScopeMember, PinnedAt: &pinned, PinnedByName: "Dana",
	}})
	got := runMemoryCall(t, c, "add", "--kind", "preference", "--pin", "--me", "--file", "web/package.json",
		"No", "emoji", "--file", "web/bun.lock", "--supersedes", "note-old0000001", "--", "--ever")
	if want := "added note-new0000001, replacing note-old0000001; pinned, for Dana only\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	want := `{"workspace_id":"ws-api","kind":"preference","text":"No emoji --ever","files":["web/package.json","web/bun.lock"],` +
		`"supersedes":"note-old0000001","scope":"member","pin":true}`
	if string(reply.got) != want {
		t.Fatalf("params = %s, want %s", reply.got, want)
	}
}

func TestParseMemoryRejectsBadInputBeforeAnyRPC(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "usage: aether memory"},
		{[]string{"forget"}, `unknown memory command "forget"`},
		{[]string{"add", "no kind"}, "--kind is required"},
		{[]string{"add", "--kind", "gotcha"}, "the note text is required"},
		{[]string{"ls", "words"}, "ls takes no words"},
		{[]string{"search"}, "search needs words"},
		{[]string{"show"}, "expected one note ID"},
		{[]string{"rm", "note-0000000001", "--run", "run-bad"}, "rm takes one note ID, or --run RUN"},
		{[]string{"rm"}, "rm takes one note ID, or --run RUN"},
		{[]string{"brief", "extra"}, "brief takes no arguments"},
		{[]string{"pin", "--everyone", "note-0000000001"}, "flag provided but not defined: -everyone"},
	} {
		if _, err := parseMemory(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseMemory(%q) = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}
