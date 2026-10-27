package ops

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type watchesInput struct {
	WatchID string `json:"watch_id,omitempty"`
}

type watchInput struct {
	WatchID    string   `json:"watch_id,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	SwarmID    string   `json:"swarm_id,omitempty"`
	PR         string   `json:"pr,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty" jsonschema:"RFC 3339"`
	Paused     *bool    `json:"paused,omitempty"`
}

type unwatchInput struct {
	WatchID string `json:"watch_id"`
}

const nextWatches = "call aether_watches for watch ids"

// maxWatches and maxNotices bound aether_watches' list and one watch's
// notices.
const (
	maxWatches = 50
	maxNotices = 10
)

var watchOps = []Op{
	opOf("aether_watches", "List watches", ScopeRead, true, nextWatches,
		"Use this when the user asks about their watches, or one watch_id's notices."+untrustedRule, listWatches).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodWatchList, protocol.MethodWatchGet),
	opOf("aether_watch", "Watch", ScopeWrite, false, nextWatches,
		"Use this when the user wants notices on a run, swarm or pr (owner/name#7); with watch_id, change or pause it.", watch).
		hints(false, false).offer(TierReversible).calling(protocol.MethodServerInfo, protocol.MethodWatchCreate, protocol.MethodWatchUpdate),
	opOf("aether_unwatch", "Delete watch", ScopeWrite, true, nextWatches,
		"Use this when the user deletes a watch.", unwatch).
		hints(true, false).offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodWatchDelete),
}

type watchTarget struct {
	Kind    string `json:"kind"`
	RunID   string `json:"run_id,omitempty"`
	SwarmID string `json:"swarm_id,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Number  int    `json:"number,omitempty"`
}

type watchRow struct {
	ID               string      `json:"id"`
	State            string      `json:"state"`
	Error            string      `json:"error,omitempty"`
	Target           watchTarget `json:"target"`
	Conditions       []string    `json:"conditions"`
	Label            string      `json:"label,omitempty"`
	Source           string      `json:"source"`
	ExpiresAt        string      `json:"expires_at"`
	LastCheckAt      string      `json:"last_check_at,omitempty"`
	LastNotification *noticeRow  `json:"last_notification,omitempty"`
	URL              string      `json:"url,omitempty"`
	Untrusted        watchText   `json:"untrusted"`
}

type watchText struct {
	TargetTitle string `json:"target_title"`
	Observed    string `json:"observed,omitempty"`
}

type noticeRow struct {
	ID         string                   `json:"id"`
	Condition  string                   `json:"condition"`
	CreatedAt  string                   `json:"created_at"`
	URL        string                   `json:"url,omitempty"`
	Deliveries []protocol.WatchDelivery `json:"deliveries"`
	Untrusted  noticeText               `json:"untrusted"`
}

type noticeText struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// at makes a dashboard path absolute; a GitHub URL stays as it is.
func (l links) at(path string) string {
	if !strings.HasPrefix(path, "/") {
		return path
	}
	if l == "" {
		return ""
	}
	return string(l) + path
}

func watchRowOf(w protocol.Watch, l links) watchRow {
	t := watchTarget{Kind: w.Target.Kind, RunID: w.Target.RunID, SwarmID: w.Target.MissionID, Repo: w.Target.Repo, Number: w.Target.Number}
	if t.Kind == protocol.WatchTargetMission {
		t.Kind = "swarm"
	}
	row := watchRow{ID: w.ID, State: w.State, Error: w.Error, Target: t, Conditions: w.Conditions, Label: w.Label, Source: w.Source,
		ExpiresAt: w.ExpiresAt, LastCheckAt: w.LastCheckAt, URL: l.at(w.Link),
		Untrusted: watchText{TargetTitle: clip(w.TargetTitle, maxReport), Observed: clip(w.Observed.Summary, maxReport)}}
	if n := w.LastNotification; n != nil {
		notice := noticeRowOf(*n, l)
		row.LastNotification = &notice
	}
	return row
}

func noticeRowOf(n protocol.WatchNotification, l links) noticeRow {
	return noticeRow{ID: n.ID, Condition: n.Condition, CreatedAt: n.CreatedAt, URL: l.at(n.Link), Deliveries: n.Deliveries,
		Untrusted: noticeText{Title: clip(n.Title, maxReport), Body: clip(n.Body, maxReport)}}
}

func listWatches(ctx context.Context, c calls, in watchesInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	if in.WatchID != "" {
		var res protocol.WatchGetResult
		if err := c.read(ctx, protocol.MethodWatchGet, protocol.WatchIDParams{WatchID: in.WatchID}, &res); err != nil {
			return "", nil, err
		}
		row := watchRowOf(res.Watch, l)
		row.LastNotification = nil
		notices := make([]noticeRow, 0, min(len(res.Notifications), maxNotices))
		for _, n := range res.Notifications[:min(len(res.Notifications), maxNotices)] {
			notices = append(notices, noticeRowOf(n, l))
		}
		out := map[string]any{"watch": row, "notifications": notices, "more_notifications": max(len(res.Notifications)-maxNotices, 0)}
		count := strconv.Itoa(len(res.Notifications))
		// watch.get returns only the newest WatchNotificationsMax notices.
		if len(res.Notifications) >= protocol.WatchNotificationsMax {
			count, out["more_notifications_capped"] = "at least "+count, true
		}
		return fmt.Sprintf("Watch %s is %s; %s notices, newest first.", row.ID, row.State, count), out, nil
	}
	var res protocol.WatchListResult
	if err := c.read(ctx, protocol.MethodWatchList, protocol.WatchListParams{}, &res); err != nil {
		return "", nil, err
	}
	rows := make([]watchRow, 0, min(len(res.Watches), maxWatches))
	for _, w := range res.Watches[:min(len(res.Watches), maxWatches)] {
		rows = append(rows, watchRowOf(w, l))
	}
	url := l.at("/?page=settings&id=watches")
	summary := fmt.Sprintf("%d watches, newest first.", len(res.Watches))
	if len(res.Watches) > maxWatches {
		summary = fmt.Sprintf("The newest %d of %d watches; the rest are listed in the dashboard at %s.", maxWatches, len(res.Watches), cmp.Or(url, "Settings → Watches"))
	}
	return summary, map[string]any{"watches": rows, "more": max(len(res.Watches)-maxWatches, 0), "url": url}, nil
}

func watch(ctx context.Context, c calls, in watchInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var res protocol.WatchResult
	if in.WatchID != "" {
		if in.RunID != "" || in.SwarmID != "" || in.PR != "" {
			return "", nil, invalid("a watch's target cannot change; leave out watch_id to create a new watch")
		}
		err = c.act(ctx, protocol.MethodWatchUpdate, protocol.WatchUpdateParams{WatchID: in.WatchID, Paused: in.Paused, Conditions: in.Conditions,
			ExpiresAt: in.ExpiresAt}, &res)
	} else {
		var given []string
		for field, v := range map[string]string{"run_id": in.RunID, "swarm_id": in.SwarmID, "pr": in.PR} {
			if v != "" {
				given = append(given, field)
			}
		}
		slices.Sort(given)
		// run_id may name the run that opened pr.
		if in.SwarmID != "" && len(given) > 1 {
			return "", nil, invalid("pass one target, not %s; run_id goes with pr only as the run that opened it", strings.Join(given, " and "))
		}
		var t protocol.WatchTarget
		switch {
		case in.Paused != nil:
			return "", nil, invalid("paused changes a watch; pass its watch_id")
		case in.PR != "":
			repo, number, ok := prRef(in.PR)
			if !ok {
				return "", nil, invalid("pr %q is not owner/name#number or a GitHub pull request URL", in.PR)
			}
			t = protocol.WatchTarget{Kind: protocol.WatchTargetPR, Repo: repo, Number: number, RunID: in.RunID}
		case in.SwarmID != "":
			t = protocol.WatchTarget{Kind: protocol.WatchTargetMission, MissionID: in.SwarmID}
		case in.RunID != "":
			t = protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: in.RunID}
		default:
			return "", nil, invalid("pass run_id, swarm_id or pr to create a watch, or watch_id to change one")
		}
		source := protocol.WatchSourceMCP
		if c.surface == Assistant {
			source = protocol.WatchSourceAssistant
		}
		// Without an idempotency key the server returns the member's live
		// watch on the same target and conditions, so a retry creates none.
		err = c.act(ctx, protocol.MethodWatchCreate, protocol.WatchCreateParams{Target: t, Conditions: in.Conditions, ExpiresAt: in.ExpiresAt,
			Source: source}, &res)
	}
	if err != nil {
		return "", nil, err
	}
	w := res.Watch
	out := map[string]any{"watch_id": w.ID, "state": w.State, "conditions": w.Conditions, "expires_at": w.ExpiresAt, "url": l.at(w.Link),
		"untrusted": map[string]any{"receipt": res.Receipt, "target_title": clip(w.TargetTitle, maxReport)}}
	summary := fmt.Sprintf("Watch %s is %s.", w.ID, w.State)
	if w.Error != "" {
		out["error"] = w.Error
		summary = fmt.Sprintf("Watch %s is %s: %s.", w.ID, w.State, w.Error)
	}
	return summary, out, nil
}

// prRef reads owner/name#7, or a GitHub pull request URL such as
// https://github.com/owner/name/pull/7/files?w=1#issuecomment-1.
func prRef(s string) (repo string, number int, ok bool) {
	n := ""
	if path, isURL := strings.CutPrefix(strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://"), "github.com/"); isURL {
		path, _, _ = strings.Cut(path, "#")
		path, _, _ = strings.Cut(path, "?")
		parts := strings.Split(path, "/")
		if len(parts) < 4 || parts[2] != "pull" {
			return "", 0, false
		}
		repo, n = parts[0]+"/"+parts[1], parts[3]
	} else {
		var found bool
		if repo, n, found = strings.Cut(s, "#"); !found {
			return "", 0, false
		}
	}
	number, err := strconv.Atoi(n)
	if err != nil || number <= 0 || strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, false
	}
	return repo, number, true
}

func unwatch(ctx context.Context, c calls, in unwatchInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	out := map[string]any{"watch_id": in.WatchID, "state": "deleted", "url": l.at("/?page=settings&id=watches")}
	err = c.act(ctx, protocol.MethodWatchDelete, protocol.WatchIDParams{WatchID: in.WatchID}, &struct{}{})
	// A retried delete finds the watch gone, which is what it asked for.
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Code == protocol.CodeNotFound {
		out["state"] = "not_found"
		return fmt.Sprintf("Watch %s is already gone; nothing was deleted.", in.WatchID), out, nil
	}
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Deleted watch %s.", in.WatchID), out, nil
}
