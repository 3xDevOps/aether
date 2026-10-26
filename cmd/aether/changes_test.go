package main

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestChangesResumeKeepsTheFeedsFilters(t *testing.T) {
	for _, tc := range []struct {
		params protocol.ChangesListParams
		want   string
	}{
		{protocol.ChangesListParams{Cursor: "12", Limit: changesDefaultLimit}, "aether changes --since=12"},
		{
			protocol.ChangesListParams{Cursor: "40", WorkspaceID: "cost$TEAM", Kinds: []string{"pr", "reports"}, Limit: 50},
			"aether changes --since=40 --workspace='cost$TEAM' --kinds=pr,reports --limit=50",
		},
		{protocol.ChangesListParams{Cursor: "7", RunID: "run-1", MissionID: "mis-1", Limit: changesDefaultLimit}, "aether changes --since=7 --run=run-1 --swarm=mis-1"},
	} {
		if got := changesResume(tc.params); got != tc.want {
			t.Errorf("changesResume(%+v) = %s, want %s", tc.params, got, tc.want)
		}
	}
}
