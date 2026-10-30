package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
	_ "time/tzdata" // a server without system zoneinfo still buckets in the member's time zone

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	insightsMaxDays     = 400
	insightsMaxDayRange = 92
	insightsHeatmapDays = 182
)

func init() {
	registerGuarded(protocol.MethodInsightsGet, permissions.View, nil, (*Server).insightsGet)
}

func (s *Server) insightsGet(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.InsightsParams](raw)
	if perr != nil {
		return nil, perr
	}
	q := store.InsightsQuery{Workspace: domain.WorkspaceID(p.WorkspaceID), Now: time.Now().UTC()}
	switch p.Scope {
	case protocol.InsightsScopeMine:
		q.Member = member
	case protocol.InsightsScopeEveryone:
	default:
		return nil, invalidParams(fmt.Sprintf("scope %q: want %q or %q", p.Scope, protocol.InsightsScopeMine, protocol.InsightsScopeEveryone))
	}
	if p.TimeZone == "Local" {
		return nil, invalidParams(`time_zone "Local": name an IANA time zone`)
	}
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		return nil, invalidParams("time_zone: " + err.Error())
	}
	if q.From, err = time.Parse(time.RFC3339, p.From); err != nil {
		return nil, invalidParams("from: " + err.Error())
	}
	if q.To, err = time.Parse(time.RFC3339, p.To); err != nil {
		return nil, invalidParams("to: " + err.Error())
	}
	if !q.From.Before(q.To) {
		return nil, invalidParams(fmt.Sprintf("to %s is not after from %s", p.To, p.From))
	}
	first, last := localDay(q.From, loc), localDay(q.To.Add(-time.Nanosecond), loc)
	span := daysBetween(first, last) + 1
	if span > insightsMaxDays {
		return nil, invalidParams(fmt.Sprintf("range spans %d days; the limit is %d", span, insightsMaxDays))
	}
	if p.WorkspaceID != "" {
		if _, err = s.cfg.Store.GetWorkspace(ctx, q.Workspace); err != nil {
			return nil, rpcError(err)
		}
	}

	q.Previous = store.Interval{Start: q.From.Add(-q.To.Sub(q.From)), End: q.From}
	if q.From.Equal(first) && q.To.Equal(localDay(q.To, loc)) {
		q.Previous.Start = first.AddDate(0, 0, -daysBetween(first, localDay(q.To, loc)))
	}
	bucket, days := "day", 1
	if span > insightsMaxDayRange {
		bucket, days = "week", 7
		first = first.AddDate(0, 0, -(int(first.Weekday())+6)%7)
	}
	var labels []string
	for start := first; start.Before(q.To); start = start.AddDate(0, 0, days) {
		end := start.AddDate(0, 0, days)
		q.Buckets = append(q.Buckets, store.Interval{Start: later(start, q.From), End: earlier(end, q.To)})
		labels = append(labels, start.Format(time.DateOnly))
	}
	heatmap := make([]protocol.InsightsHeatmapDay, insightsHeatmapDays)
	for i := range heatmap {
		day := last.AddDate(0, 0, i-insightsHeatmapDays+1)
		q.Days = append(q.Days, store.Interval{Start: day, End: day.AddDate(0, 0, 1)})
		heatmap[i].Date = day.Format(time.DateOnly)
	}

	earliest := q.Previous.Start
	if q.Days[0].Start.Before(earliest) {
		earliest = q.Days[0].Start
	}
	if earliest.Before(time.Unix(0, math.MinInt64)) || q.To.After(time.Unix(0, math.MaxInt64)) {
		return nil, invalidParams(fmt.Sprintf("range with its comparison and heatmap must lie within %s and %s",
			time.Unix(0, math.MinInt64).UTC().Format(time.RFC3339), time.Unix(0, math.MaxInt64).UTC().Format(time.RFC3339)))
	}
	got, err := s.cfg.Store.Insights(ctx, q)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.InsightsResult{
		GeneratedAt: q.Now.Format(time.RFC3339),
		From:        q.From.In(loc).Format(time.RFC3339),
		To:          q.To.In(loc).Format(time.RFC3339),
		TimeZone:    loc.String(),
		Bucket:      bucket,
		Totals:      insightsTotals(got.Totals),
		Previous:    insightsTotals(got.Previous),
		Series:      make([]protocol.InsightsSeriesPoint, len(got.Series)),
		Heatmap:     heatmap,
		ByWorkspace: make([]protocol.InsightsWorkspace, 0, len(got.ByWorkspace)),
		ByAgent:     make([]protocol.InsightsAgent, 0, len(got.ByAgent)),
		Durations: protocol.InsightsDurations{
			RunsMeasured:          got.Durations.RunsMeasured,
			MedianRunSeconds:      seconds(got.Durations.MedianRun),
			P90RunSeconds:         seconds(got.Durations.P90Run),
			StateRunsMeasured:     got.Durations.StateRunsMeasured,
			MedianWorkingSeconds:  seconds(got.Durations.MedianWorking),
			MedianWaitingSeconds:  seconds(got.Durations.MedianWaiting),
			MedianResponseSeconds: seconds(got.Durations.MedianResponse),
		},
		Cost: protocol.InsightsCost{
			MeteredRuns: got.Cost.MeteredRuns, UnmeteredRuns: got.Cost.UnmeteredRuns, CostUSD: got.Cost.CostUSD,
			InputTokens: got.Cost.InputTokens, OutputTokens: got.Cost.OutputTokens,
			TotalTokens: got.Cost.InputTokens + got.Cost.OutputTokens,
			ByAgent:     make([]protocol.InsightsCostAgent, 0, len(got.Cost.ByAgent)),
		},
		PullRequests: protocol.InsightsPullRequests(got.PullRequests),
		Success: protocol.InsightsSuccess{
			Runs: protocol.InsightsSuccessCount{Succeeded: got.Success.RunsSucceeded, Finished: got.Success.RunsFinished},
		},
		Outcomes: protocol.InsightsOutcomes{
			Series:  make([]protocol.InsightsOutcomeBucket, len(got.Series)),
			Members: make([]protocol.InsightsMemberOutcome, 0, len(got.Members)),
		},
		Recent:   make([]protocol.InsightsRecentRun, 0, len(got.Recent)),
		NeedsYou: s.unpaused(got.NeedsYou),
		Coverage: protocol.InsightsCoverage{
			StateTimesSince:   optionalRFC3339(got.StateTimesSince),
			PullRequestsSince: optionalRFC3339(got.PullRequestsSince),
		},
	}
	for i, c := range got.Series {
		out.Series[i] = protocol.InsightsSeriesPoint{
			Start: labels[i], Launched: c.Launched, Completed: c.Completed, Merged: c.Merged,
			Failed: c.Failed, Interrupted: c.Interrupted, Abandoned: c.Abandoned,
		}
	}
	for _, a := range got.Cost.ByAgent {
		out.Cost.ByAgent = append(out.Cost.ByAgent, protocol.InsightsCostAgent{
			Harness: a.Harness, MeteredRuns: a.MeteredRuns, UnmeteredRuns: a.UnmeteredRuns, CostUSD: a.CostUSD,
			InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, TotalTokens: a.InputTokens + a.OutputTokens,
		})
	}
	for i, c := range got.Series {
		runs := store.InsightsOutcome{Done: c.Completed + c.Merged, Failed: c.Failed, Abandoned: c.Abandoned + c.Interrupted}
		out.Outcomes.Series[i] = protocol.InsightsOutcomeBucket{Start: labels[i], Runs: outcome(runs)}
		addOutcome(&out.Outcomes.Runs, runs)
	}
	if p.Scope == protocol.InsightsScopeEveryone {
		for _, m := range got.Members {
			member := protocol.InsightsMemberOutcome{
				MemberID: string(m.ID), DisplayName: m.Name, Color: m.Color,
				Series: make([]protocol.InsightsOutcomeBucket, len(labels)),
			}
			for i, label := range labels {
				member.Series[i] = protocol.InsightsOutcomeBucket{Start: label, Runs: outcome(m.Runs[i])}
				addOutcome(&member.Runs, m.Runs[i])
			}
			out.Outcomes.Members = append(out.Outcomes.Members, member)
		}
	}
	for i, n := range got.Heatmap {
		out.Heatmap[i].Runs = n
	}
	for _, w := range got.ByWorkspace {
		out.ByWorkspace = append(out.ByWorkspace, protocol.InsightsWorkspace{
			WorkspaceID: string(w.ID), Name: w.Name, Launched: w.Launched, Finished: w.Finished, Failed: w.Failed,
		})
	}
	for _, a := range got.ByAgent {
		out.ByAgent = append(out.ByAgent, protocol.InsightsAgent{
			Harness: a.Harness, Model: a.Model, Launched: a.Launched, Finished: a.Finished, Failed: a.Failed,
		})
	}
	for _, r := range got.Recent {
		start := r.CreatedAt
		if r.StartedAt != nil {
			start = *r.StartedAt
		}
		out.Recent = append(out.Recent, protocol.InsightsRecentRun{
			RunID: string(r.ID), Title: push.RunTitle(&domain.Run{Title: r.Title, Task: r.Task}), WorkspaceID: string(r.WorkspaceID), Status: string(r.Status),
			Reason: r.Reason, Harness: r.Harness, Model: r.Model,
			CreatedAt: r.CreatedAt.Format(time.RFC3339), FinishedAt: r.FinishedAt.Format(time.RFC3339),
			DurationSeconds: seconds(r.FinishedAt.Sub(start)), PRURL: r.PRURL, PRState: r.PRState,
		})
	}
	return out, nil
}

func insightsTotals(c store.InsightsCounts) protocol.InsightsTotals {
	return protocol.InsightsTotals{
		Launched: c.Launched, Completed: c.Completed, Merged: c.Merged, Failed: c.Failed,
		Interrupted: c.Interrupted, Abandoned: c.Abandoned, Killed: c.Killed, Active: c.Active,
		Finished: c.Completed + c.Merged,
	}
}

func outcome(o store.InsightsOutcome) protocol.InsightsOutcome { return protocol.InsightsOutcome(o) }

func addOutcome(sum *protocol.InsightsOutcome, o store.InsightsOutcome) {
	sum.Done += o.Done
	sum.Failed += o.Failed
	sum.Abandoned += o.Abandoned
}

func localDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return int(time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC).Sub(time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)) / (24 * time.Hour))
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func seconds(d time.Duration) int64 { return int64(d / time.Second) }

func optionalRFC3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) unpaused(runs []domain.RunID) int {
	n := 0
	for _, id := range runs {
		if !s.cfg.Runs.Paused(id) {
			n++
		}
	}
	return n
}
