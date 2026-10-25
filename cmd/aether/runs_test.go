package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestRenderRuns(t *testing.T) {
	archivedAt := "2026-09-10T00:00:00Z"
	runs := []protocol.Run{
		{ID: "r1", Status: "running", Activity: "working", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Title one", Task: "task one"},
		{ID: "r2", Status: "needs-attention", Activity: "needs-you", Harness: "claude", MemberID: "m2", AccountMemberID: "m1", RequestedAccountMemberID: "m2",
			Title: "Title two", Task: "task two"},
		{ID: "r4", Status: "needs-attention", Activity: "waiting-agents", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Integrate", Task: "task four"},
		{ID: "r5", Status: "needs-attention", Activity: "needs-you", Reason: "needs input: which?", MissionRole: "worker", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Worker", Task: "task five"},
		{ID: "r6", Status: "needs-attention", Activity: "failed", OutcomeUnseen: true, Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Parked", Task: "task six"},
		{ID: "r7", Status: "failed", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Ended", Task: "task seven"},
		{ID: "r8", Status: "needs-attention", Activity: "finished", Review: "ready", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Done", Task: "task eight"},
		{ID: "r9", Status: "completed", Review: "later", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Later", Task: "task nine"},
		{ID: "r10", Status: "needs-attention", Activity: "waiting-ci", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "CI", Task: "task ten"},
		{ID: "r11", Status: "needs-attention", Activity: "idle", Harness: "claude", MemberID: "m1", AccountMemberID: "m1", Title: "Legacy", Task: "task eleven"},
		{ID: "r3", Status: "abandoned", Harness: "claude", MemberID: "m3", AccountMemberID: "m3", Title: "Title three", Task: "task three",
			ArchivedAt: &archivedAt},
	}
	memberName := func(id string) string { return id }
	overlapOf := map[string]string{"r2": "a.go with bob"}

	cases := []struct {
		name     string
		needsYou bool
		archived bool
		want     string
		wantIDs  []string
	}{
		{
			name: "default hides archived runs",
			want: "ID   STATUS              AGENT   MEMBER  ACCOUNT        OVERLAP        TITLE      TASK\n" +
				"r1   Working             claude  m1      m1                            Title one  task one\n" +
				"r2   Needs you           claude  m2      m1 (fallback)  a.go with bob  Title two  task two\n" +
				"r4   Waiting for agents  claude  m1      m1                            Integrate  task four\n" +
				"r5   Needs you           claude  m1      m1                            Worker     task five\n" +
				"r6   Failed              claude  m1      m1                            Parked     task six\n" +
				"r7   Failed              claude  m1      m1                            Ended      task seven\n" +
				"r8   Ready for review    claude  m1      m1                            Done       task eight\n" +
				"r9   Ready for review    claude  m1      m1                            Later      task nine\n" +
				"r10  Waiting for CI      claude  m1      m1                            CI         task ten\n" +
				"r11  Waiting             claude  m1      m1                            Legacy     task eleven\n",
		},
		{
			name:     "--needs-you hides archived, waiting, worker, ended and put off runs",
			needsYou: true,
			wantIDs:  []string{"r2", "r6", "r8"},
		},
		{
			name:     "--archived shows only archived runs",
			archived: true,
			wantIDs:  []string{"r3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderRuns(&buf, runs, memberName, overlapOf, tc.needsYou, tc.archived); err != nil {
				t.Fatalf("renderRuns: %v", err)
			}
			if got := buf.String(); tc.want != "" && got != tc.want {
				t.Errorf("renderRuns() = %q, want %q", got, tc.want)
			}
			if tc.wantIDs != nil {
				rows := strings.Split(strings.TrimSpace(buf.String()), "\n")
				if len(rows) != len(tc.wantIDs)+1 {
					t.Fatalf("renderRuns() rows = %q, want a header and %d runs", rows, len(tc.wantIDs))
				}
				for i, id := range tc.wantIDs {
					if fields := strings.Fields(rows[i+1]); len(fields) == 0 || fields[0] != id {
						t.Errorf("renderRuns() row = %q, want run %s", rows[i+1], id)
					}
				}
			}
		})
	}
}

func TestRunsRejectsNeedsYouAndArchivedTogether(t *testing.T) {
	err := runRuns([]string{"--needs-you", "--archived"})
	if err == nil {
		t.Fatal("--needs-you with --archived succeeded, want a usage error")
	}
	if !strings.Contains(err.Error(), "--needs-you and --archived cannot be used together") {
		t.Errorf("error = %v, want the exact combination message", err)
	}
}

func permissionItem() protocol.AttentionItem {
	return protocol.AttentionItem{Kind: protocol.AttentionKindPermission, RunID: "r1", WorkspaceID: "ws1", Reason: "Permission requested",
		Request: &protocol.AttentionRequest{ID: "req-1", Kind: "permission", Title: "Run rm -rf build", Options: []protocol.AttentionOption{
			{ID: "allow-1", Label: "Allow once", Kind: "allow_once"},
			{ID: "always-1", Label: "Always allow", Kind: "allow_always"},
			{ID: "reject-1", Label: "Reject", Kind: "reject_once"},
		}}}
}

func reportEvent(t *testing.T, runID, outcome, summary string) protocol.Event {
	t.Helper()
	raw, err := json.Marshal(events.TimelinePayload{Kind: events.TimelineReport, Outcome: outcome, Summary: summary})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Event{RunID: runID, Type: string(events.TypeTimeline), Payload: raw}
}

// A run's view carries the latest report and only the options aether
// answer accepts, and its text form ends with the commands that answer it.
func TestRunViewShowsReportAndAnswerableOptions(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodWorkspaceList: {result: protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{{ID: "ws1", Name: "demo"}}}},
		protocol.MethodWorkspaceTimeline: {result: protocol.WorkspaceTimelineResult{Events: []protocol.Event{
			reportEvent(t, "r1", "failure", "older"), reportEvent(t, "r1", "blocked", "no build dir"),
		}}},
	})
	r := protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "needs-attention", Activity: "needs-you", Reason: "needs input: which build dir?",
		Branch: "aether/run-x", PendingInputs: []domain.RunInputRequest{{ID: "req-1", Kind: "permission"}}}
	items := []protocol.AttentionItem{permissionItem()}
	v, err := runViewOf(c, r, items)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, v); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "r1", "workspace_id": "ws1", "workspace": "demo", "status": "Needs you", "state": "needs-attention",
		"reason": "needs input: which build dir?", "branch": "aether/run-x",
		"pending_inputs": []any{map[string]any{"kind": "permission", "request_id": "req-1", "options": []any{
			map[string]any{"id": "allow-1", "label": "Allow once", "kind": "allow_once"},
			map[string]any{"id": "reject-1", "label": "Reject", "kind": "reject_once"},
		}}},
		"report": map[string]any{"outcome": "blocked", "summary": "no build dir"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runs <id> --json = %s\nwant %v", buf.String(), want)
	}

	buf.Reset()
	if err := printRunView(&buf, v, items); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, line := range []string{"status     Needs you\n", "report     blocked: no build dir\n", "pending    permission req-1\n",
		"r1 permission req-1: Run rm -rf build\n", "  aether answer r1 req-1 allow-1    # Allow once\n", "  aether answer r1 req-1 reject-1    # Reject\n"} {
		if !strings.Contains(text, line) {
			t.Errorf("runs <id> = %q, want the line %q", text, line)
		}
	}
	if strings.Contains(text, "always-1") {
		t.Errorf("runs <id> offers an always option: %q", text)
	}
}
