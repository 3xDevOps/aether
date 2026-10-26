package ops

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func attention(ctx context.Context, read Call, workspaceID string) ([]protocol.AttentionItem, error) {
	var res protocol.AttentionListResult
	if err := read(ctx, protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: workspaceID}, &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// attentionRow is an attention.list item with what answers it. answer_id
// is the id aether_answer or aether_decide_approval takes. reason is a
// fixed label for the kind; what the agent or teammate wrote, the server's
// reason included, is under untrusted.
type attentionRow struct {
	Kind        string         `json:"kind"`
	Reason      string         `json:"reason"`
	RunID       string         `json:"run_id"`
	MissionID   string         `json:"mission_id,omitempty"`
	WorkspaceID string         `json:"workspace_id"`
	Harness     string         `json:"harness"`
	Since       string         `json:"since,omitempty"`
	AnswerWith  string         `json:"answer_with"`
	AnswerID    string         `json:"answer_id,omitempty"`
	URL         string         `json:"url,omitempty"`
	Untrusted   *attentionText `json:"untrusted"`
}

type attentionText struct {
	Title   string                     `json:"title,omitempty"`
	Reason  string                     `json:"reason,omitempty"`
	Text    string                     `json:"text,omitempty"`
	Options []protocol.AttentionOption `json:"options,omitempty"`
	LinkURL string                     `json:"link_url,omitempty"`
	Schema  json.RawMessage            `json:"schema,omitempty"`
}

var attentionLabels = map[string]string{
	protocol.AttentionKindPermission:     "The agent asks permission",
	protocol.AttentionKindQuestion:       "The agent asks a question",
	protocol.AttentionKindQueuedMessage:  "A message awaits your approval to deliver",
	protocol.AttentionKindRoomQuestion:   "A teammate asked a question",
	protocol.AttentionKindSwarmQuestion:  "The integrator has a question",
	protocol.AttentionKindIntegratorDown: "The swarm's integrator stopped",
	protocol.AttentionKindControlHold:    "You hold control of a worker",
	protocol.AttentionKindDisconnected:   "Agent disconnected",
	protocol.AttentionKindBlocked:        "The agent reports it is blocked",
	protocol.AttentionKindNeedsInput:     "The agent needs your input",
	protocol.AttentionKindReady:          "The agent reports the task finished",
	protocol.AttentionKindFailed:         "The agent reported failure",
}

func attentionLabel(kind string) string {
	return cmp.Or(attentionLabels[kind], kind)
}

func attentionRowOf(it protocol.AttentionItem, l links) attentionRow {
	text := &attentionText{Title: clip(it.Title, maxReport), Reason: clip(it.Reason, maxText)}
	row := attentionRow{Kind: it.Kind, Reason: attentionLabel(it.Kind), RunID: it.RunID, MissionID: it.MissionID, WorkspaceID: it.WorkspaceID,
		Harness: it.Harness, Since: it.Since, URL: l.run(it.RunID), Untrusted: text}
	switch {
	case it.Request != nil:
		row.AnswerID = it.Request.ID
		text.Text, text.LinkURL, text.Schema = clip(it.Request.Title, maxText), it.Request.URL, it.Request.Schema
		text.Options = it.Request.Options
	case it.Message != nil:
		row.AnswerID = it.Message.ID
		text.Text = clip(it.Message.Body, maxText)
	}
	row.AnswerWith = answerWith(it)
	return row
}

func answerWith(it protocol.AttentionItem) string {
	const dashboard = "the run in the Aether dashboard"
	switch it.Kind {
	case protocol.AttentionKindPermission:
		if it.Request != nil && len(it.Request.Options) > 0 {
			return "aether_decide_approval"
		}
		return "the run's terminal in the Aether dashboard"
	case protocol.AttentionKindQuestion:
		switch {
		case it.Request == nil || len(it.Request.Options) == 0:
			return "the run's terminal in the Aether dashboard"
		case it.Request.URL != "":
			return "open untrusted.link_url, then aether_answer with an option"
		}
		return "aether_answer"
	case protocol.AttentionKindSwarmQuestion, protocol.AttentionKindRoomQuestion:
		if it.Message != nil {
			return "aether_answer"
		}
		return dashboard
	case protocol.AttentionKindNeedsInput, protocol.AttentionKindBlocked:
		return "aether_message_run"
	case protocol.AttentionKindReady:
		return "aether_pr_status, then aether_mark_reviewed or aether_stop_run with outcome"
	case protocol.AttentionKindFailed:
		return "aether_get_run, then aether_message_run or aether_stop_run with outcome"
	case protocol.AttentionKindIntegratorDown:
		return "aether_replace_integrator"
	case protocol.AttentionKindControlHold:
		return "aether_swarm_manage with release_worker"
	}
	return dashboard
}

func needsAttention(ctx context.Context, c calls, in workspaceFilterInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	items, err := attention(ctx, c.read, in.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	rows := make([]attentionRow, 0, min(len(items), maxAttention))
	for _, it := range items[:min(len(items), maxAttention)] {
		rows = append(rows, attentionRowOf(it, l))
	}
	out := map[string]any{"items": rows}
	switch {
	case len(items) == 0:
		return "Nothing needs you.", out, nil
	case len(items) > len(rows):
		out["total"], out["truncated"] = len(items), true
		return fmt.Sprintf("%d of %d items that need you, oldest first.", len(rows), len(items)), out, nil
	}
	return fmt.Sprintf("%d items need you, oldest first.", len(rows)), out, nil
}
