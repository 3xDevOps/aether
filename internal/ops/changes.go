package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type changesFeedInput struct {
	Cursor      string   `json:"cursor,omitempty"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	SwarmID     string   `json:"swarm_id,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
	Limit       int      `json:"limit,omitempty" jsonschema:"default 20"`
}

var changesOps = []Op{
	opOf("aether_changes", "Changes", ScopeRead, true, nextRuns,
		"Use this when the user asks what changed: run status, reports, mail, pull requests, swarms, lifecycle; oldest first, paged by next_cursor. "+
			"CI checks: aether_pr_status."+untrustedRule, changesFeed).
		enum("kinds", protocol.ChangeKindStatus, protocol.ChangeKindReports, protocol.ChangeKindMessages, protocol.ChangeKindPR,
			protocol.ChangeKindSwarm, protocol.ChangeKindLifecycle).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodChangesList),
}

// changeRow is a change with its agent-written text under untrusted.
type changeRow struct {
	protocol.Change
	URL string `json:"url,omitempty"`
}

func changesFeed(ctx context.Context, c calls, in changesFeedInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var res protocol.ChangesListResult
	err = c.read(ctx, protocol.MethodChangesList, protocol.ChangesListParams{Cursor: in.Cursor, WorkspaceID: in.WorkspaceID, RunID: in.RunID,
		MissionID: in.SwarmID, Kinds: in.Kinds, Limit: rowLimit(in.Limit)}, &res)
	if err != nil {
		return "", nil, cursorAhead(err)
	}
	rows := make([]changeRow, 0, len(res.Changes))
	for _, ch := range res.Changes {
		if u := ch.Untrusted; u != nil {
			ch.Untrusted = &protocol.ChangeUntrusted{ReportID: u.ReportID, Summary: clip(u.Summary, maxReport), Reason: clip(u.Reason, maxReport)}
		}
		url := l.run(ch.RunID)
		if ch.RunID == "" {
			url = l.swarm(ch.MissionID)
		}
		rows = append(rows, changeRow{Change: ch, URL: url})
	}
	out := map[string]any{"changes": rows, "next_cursor": res.NextCursor, "more": res.More}
	summary := fmt.Sprintf("%d changes.", len(rows))
	if res.More {
		summary += " More remain: call again with next_cursor."
	}
	return summary, out, nil
}

// cursorAhead tells the model how to recover from a cursor the server's
// history no longer matches, such as one from before a restore.
func cursorAhead(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		return err
	}
	var data protocol.ChangesCursorAheadData
	if json.Unmarshal(perr.Data, &data) != nil || data.Reason != protocol.ChangesCursorAhead {
		return err
	}
	return &protocol.Error{Code: perr.Code, Data: perr.Data,
		Message: perr.Message + "; call aether_changes again without cursor to restart from the newest changes"}
}
