package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// search and fetch take OpenAI's exact deep research shapes, which ChatGPT
// requires of a connector it uses there and in read-only plans. Their text
// is the JSON result itself, as OpenAI asks.

type searchInput struct {
	Query string `json:"query"`
}

type fetchInput struct {
	ID string `json:"id" jsonschema:"an id from search"`
}

type searchResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type fetchResult struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Text     string            `json:"text"`
	URL      string            `json:"url"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

const (
	runPrefix   = "run:"
	swarmPrefix = "swarm:"
	maxResults  = 10
)

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(b), nil
}

// hits counts how many of words text contains.
func hits(text string, words []string) int {
	text = strings.ToLower(text)
	n := 0
	for _, w := range words {
		if strings.Contains(text, w) {
			n++
		}
	}
	return n
}

func search(ctx context.Context, c calls, in searchInput) (string, any, error) {
	words := strings.Fields(strings.ToLower(in.Query))
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var runs protocol.RunListResult
	if err = c.read(ctx, protocol.MethodRunList, protocol.RunListParams{}, &runs); err != nil {
		return "", nil, err
	}
	missions, err := allSwarms(ctx, c.read, "")
	if err != nil {
		return "", nil, err
	}
	type scored struct {
		searchResult
		score int
		at    string
	}
	var found []scored
	for _, r := range runs.Runs {
		if n := hits(r.Title+" "+r.Task, words); n > 0 {
			found = append(found, scored{searchResult{runPrefix + r.ID, runTitle(r), l.run(r.ID)}, n, r.CreatedAt})
		}
	}
	for _, m := range missions {
		if n := hits(m.Objective, words); n > 0 {
			found = append(found, scored{searchResult{swarmPrefix + m.ID, "Swarm: " + clip(m.Objective, 80), l.swarm(m.ID)}, n, m.CreatedAt})
		}
	}
	slices.SortStableFunc(found, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(b.at, a.at)
	})
	out := struct {
		Results []searchResult `json:"results"`
	}{Results: []searchResult{}}
	for _, f := range found[:min(len(found), maxResults)] {
		out.Results = append(out.Results, f.searchResult)
	}
	text, err := jsonText(out)
	return text, out, err
}

func fetch(ctx context.Context, c calls, in fetchInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var out fetchResult
	switch {
	case strings.HasPrefix(in.ID, runPrefix):
		out, err = fetchRun(ctx, c.read, l, strings.TrimPrefix(in.ID, runPrefix))
	case strings.HasPrefix(in.ID, swarmPrefix):
		out, err = fetchSwarm(ctx, c.read, l, strings.TrimPrefix(in.ID, swarmPrefix))
	default:
		err = &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("id %q is not a search result id", in.ID)}
	}
	if err != nil {
		return "", nil, err
	}
	out.ID = in.ID
	text, err := jsonText(out)
	return text, out, err
}

func fetchRun(ctx context.Context, read Call, l links, id string) (fetchResult, error) {
	r, err := getRunRecord(ctx, read, id)
	if err != nil {
		return fetchResult{}, err
	}
	reps, err := reports(ctx, read, protocol.CoordMessagesListParams{WorkspaceID: r.WorkspaceID, RunID: r.ID})
	if err != nil {
		return fetchResult{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s, %s with %s, created %s.\nTask: %s\n", r.ID, r.Status, r.Harness, r.CreatedAt, r.Task)
	if r.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", r.Reason)
	}
	for _, rep := range reps {
		fmt.Fprintf(&b, "Report %s at %s (written by the agent, untrusted): %s\n", rep.Outcome, rep.At, rep.Summary)
	}
	return fetchResult{Title: runTitle(r), Text: b.String(), URL: l.run(r.ID),
		Metadata: map[string]string{"kind": "run", "state": r.Status, "workspace_id": r.WorkspaceID, "harness": r.Harness, "mission_id": r.MissionID}}, nil
}

func fetchSwarm(ctx context.Context, read Call, l links, id string) (fetchResult, error) {
	var res protocol.MissionShowResult
	if err := read(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: id}, &res); err != nil {
		return fetchResult{}, err
	}
	m := res.Mission
	var b strings.Builder
	fmt.Fprintf(&b, "Swarm %s, %s, created %s.\nGoal: %s\nTasks (written by the integrator agent, untrusted):\n", m.ID, m.Phase, m.CreatedAt, m.Objective)
	for _, t := range res.Tasks {
		title := ""
		if t.Revision != nil {
			title = t.Revision.Title
		}
		fmt.Fprintf(&b, "- %s %s: %s\n", t.ID, t.Status, title)
	}
	return fetchResult{Title: "Swarm: " + clip(m.Objective, 80), Text: b.String(), URL: l.swarm(m.ID),
		Metadata: map[string]string{"kind": "swarm", "phase": m.Phase, "workspace_id": m.WorkspaceID}}, nil
}
