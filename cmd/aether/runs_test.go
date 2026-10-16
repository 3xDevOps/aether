package main

import (
	"bytes"
	"strings"
	"testing"

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
