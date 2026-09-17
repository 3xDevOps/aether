package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestRenderRuns(t *testing.T) {
	deletesAt := "2026-09-20T00:00:00Z"
	archivedAt := "2026-09-10T00:00:00Z"
	runs := []protocol.Run{
		{ID: "r1", Status: "running", Harness: "claude", MemberID: "m1", Title: "Title one", Task: "task one"},
		{ID: "r2", Status: "needs-attention", Harness: "claude", MemberID: "m2", Title: "Title two", Task: "task two"},
		{ID: "r3", Status: "abandoned", Harness: "claude", MemberID: "m3", Title: "Title three", Task: "task three",
			ArchivedAt: &archivedAt, DeletesAt: &deletesAt},
	}
	memberName := func(id string) string { return id }
	overlapOf := map[string]string{"r2": "a.go with bob"}

	cases := []struct {
		name      string
		attention bool
		archived  bool
		want      string
	}{
		{
			name: "default hides archived runs",
			want: "ID  STATUS           HARNESS  MEMBER  OVERLAP        TITLE      TASK\n" +
				"r1  running          claude   m1                     Title one  task one\n" +
				"r2  needs-attention  claude   m2      a.go with bob  Title two  task two\n",
		},
		{
			name:      "--attention hides archived runs",
			attention: true,
			want: "ID  STATUS           HARNESS  MEMBER  OVERLAP        TITLE      TASK\n" +
				"r2  needs-attention  claude   m2      a.go with bob  Title two  task two\n",
		},
		{
			name:     "--archived shows only archived runs with the DELETES header and a literal deletion value",
			archived: true,
			want: "ID  STATUS     HARNESS  MEMBER  DELETES               TITLE        TASK\n" +
				"r3  abandoned  claude   m3      2026-09-20T00:00:00Z  Title three  task three\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderRuns(&buf, runs, memberName, overlapOf, tc.attention, tc.archived); err != nil {
				t.Fatalf("renderRuns: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("renderRuns() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunsRejectsAttentionAndArchivedTogether(t *testing.T) {
	err := runRuns([]string{"--attention", "--archived"})
	if err == nil {
		t.Fatal("--attention with --archived succeeded, want a usage error")
	}
	if !strings.Contains(err.Error(), "--attention and --archived cannot be used together") {
		t.Errorf("error = %v, want the exact combination message", err)
	}
}
