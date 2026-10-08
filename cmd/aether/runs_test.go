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
		{ID: "r1", Status: "running", Harness: "claude", MemberID: "m1", Title: "Title one", Task: "task one"},
		{ID: "r2", Status: "needs-attention", Harness: "claude", MemberID: "m2", Title: "Title two", Task: "task two"},
		{ID: "r3", Status: "abandoned", Harness: "claude", MemberID: "m3", Title: "Title three", Task: "task three",
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
			want: "ID  STATUS     AGENT   MEMBER  OVERLAP        TITLE      TASK\n" +
				"r1  running    claude  m1                     Title one  task one\n" +
				"r2  needs-you  claude  m2      a.go with bob  Title two  task two\n",
		},
		{
			name:     "--needs-you hides archived runs",
			needsYou: true,
			want: "ID  STATUS     AGENT   MEMBER  OVERLAP        TITLE      TASK\n" +
				"r2  needs-you  claude  m2      a.go with bob  Title two  task two\n",
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
