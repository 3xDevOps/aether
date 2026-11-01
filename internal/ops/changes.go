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
	Kinds       []string `json:"kinds,omitempty"`
	Limit       int      `json:"limit,omitempty" jsonschema:"default 20"`
}

var changesOps = []Op{
	opOf("aether_changes", "Changes", ScopeRead, true, nextRuns,
		"Use this when the user asks what changed since a cursor, paged by next_cursor. "+
			"CI checks: aether_pr_status."+untrustedRule, changesFeed).
		enum("kinds", protocol.ChangeKindStatus, protocol.ChangeKindReports, protocol.ChangeKindMessages, protocol.ChangeKindPR,
			protocol.ChangeKindLifecycle).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodChangesList),
}

// changeUntrusted adds a message's correlation ID, which for report mail
// embeds the agent's idempotency key.
type changeUntrusted struct {
	protocol.ChangeUntrusted
	CorrelationID string `json:"correlation_id,omitempty"`
}

// changeRow is a change with its agent-written text under untrusted.
type changeRow struct {
	protocol.Change
	Untrusted *changeUntrusted `json:"untrusted,omitempty"`
	URL       string           `json:"url,omitempty"`
}

func changesFeed(ctx context.Context, c calls, in changesFeedInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var res protocol.ChangesListResult
	err = c.read(ctx, protocol.MethodChangesList, protocol.ChangesListParams{Cursor: in.Cursor, WorkspaceID: in.WorkspaceID, RunID: in.RunID,
		Kinds: in.Kinds, Limit: rowLimit(in.Limit)}, &res)
	// A cursor the server's history no longer matches, such as one from
	// before a restore, gets how to recover.
	var perr *protocol.Error
	var ahead protocol.ChangesCursorAheadData
	if errors.As(err, &perr) && json.Unmarshal(perr.Data, &ahead) == nil && ahead.Reason == protocol.ChangesCursorAhead {
		return "", nil, &protocol.Error{Code: perr.Code, Data: perr.Data,
			Message: perr.Message + "; call aether_changes again without cursor to restart from the newest changes"}
	}
	if err != nil {
		return "", nil, err
	}
	rows := make([]changeRow, 0, len(res.Changes))
	for _, ch := range res.Changes {
		row := changeRow{Change: ch, URL: l.run(ch.RunID)}
		if u := ch.Untrusted; u != nil {
			row.Untrusted = &changeUntrusted{ChangeUntrusted: protocol.ChangeUntrusted{ReportID: u.ReportID, Summary: clip(u.Summary, maxReport),
				Reason: clip(u.Reason, maxReport)}}
		}
		if m := ch.Message; m != nil && m.CorrelationID != "" {
			message := *m
			row.Message, message.CorrelationID = &message, ""
			if row.Untrusted == nil {
				row.Untrusted = &changeUntrusted{}
			}
			row.Untrusted.CorrelationID = clip(m.CorrelationID, maxReport)
		}
		rows = append(rows, row)
	}
	out := map[string]any{"changes": rows, "next_cursor": res.NextCursor, "more": res.More}
	summary := fmt.Sprintf("%d changes.", len(rows))
	if res.More {
		summary += " More remain: call again with next_cursor."
	}
	return summary, out, nil
}
