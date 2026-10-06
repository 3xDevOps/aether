// Package timeline reads workspace history back out of the persisted event
// log: one chronological, filterable feed per workspace, and the audit
// story behind it. It is a reader only - nothing publishes here and it
// owns no storage of its own.
package timeline

import (
	"context"
	"fmt"
	"slices"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const (
	// DefaultLimit is the page size used when a caller asks for none.
	DefaultLimit = 100
	// MaxLimit caps one page, so a client cannot ask the server to
	// materialize an unbounded slice of history.
	MaxLimit = 1000
	// scanBudget bounds how many stored rows one Page reads while
	// post-filtering, as a multiple of limit: a workspace whose log is
	// mostly detail events costs a few extra round trips instead of one
	// unbounded scan. A page that stops on the budget reports More.
	scanBudget = 20
	// minBatch keeps small pages from reading the log one row at a time.
	minBatch = 64
)

// detailTypes are the firehoses the feed leaves out by default: diff
// snapshots, adapter activity, mail acknowledgements, swarm refresh hints
// and presence are detail, not workspace history. Asking for one by type still returns it.
var detailTypes = map[events.Type]bool{
	events.TypeRunDiff:           true,
	events.TypeRunTitle:          true,
	events.TypeAgentEvent:        true,
	events.TypeCoordMessageAcked: true,
	events.TypeMissionChanged:    true,
	events.TypePresence:          true,
}

// Filter narrows a timeline page. Member matches an event's actor - who
// did it - not the owner of the run it concerns. Run matches agent messages
// on either side; MissionID matches every run that has served the mission.
type Filter struct {
	Workspace domain.WorkspaceID
	Run       domain.RunID
	MissionID domain.MissionID
	Member    domain.MemberID
	Types     []events.Type
}

type MissionRuns interface {
	ListMissionRunIDs(context.Context, domain.MissionID) ([]domain.RunID, error)
}

// Page is one slice of history, oldest first. NextSeq is the cursor to
// pass as the next call's afterSeq; More reports that history remains past
// it. A page read backward by Before sets NextSeq to the log head, More to
// older history remaining, and OlderSeq to the next call's beforeSeq.
type Page struct {
	Events   []events.Event
	NextSeq  uint64
	More     bool
	OlderSeq uint64
}

// Reader pages a workspace's history out of the event log.
type Reader struct {
	log      events.EventLog
	missions MissionRuns
}

// NewReader returns a Reader over log that resolves mission filters through
// missions, which must not be nil.
func NewReader(log events.EventLog, missions MissionRuns) *Reader {
	return &Reader{log: log, missions: missions}
}

// Page returns up to limit events matching f with Seq > afterSeq, oldest
// first. Reads are bounded by the log head sampled at entry, so paging
// stays stable while new events arrive.
func (r *Reader) Page(ctx context.Context, f Filter, afterSeq uint64, limit int) (Page, error) {
	limit = clampLimit(limit)
	head, logFilter, ok, err := r.prepare(ctx, f)
	if err != nil {
		return Page{}, err
	}
	cursor := afterSeq
	if !ok || cursor >= head {
		return Page{NextSeq: head}, nil
	}
	batch := max(limit, minBatch)
	out := make([]events.Event, 0, limit)
	scanned := 0
	for len(out) < limit && cursor < head && scanned < limit*scanBudget {
		got, rerr := r.log.Read(ctx, logFilter, cursor, head, batch)
		if rerr != nil {
			return Page{}, fmt.Errorf("timeline: read workspace %s history: %w", f.Workspace, rerr)
		}
		consumed := 0
		for _, e := range got {
			consumed++
			cursor = e.Seq
			scanned++
			if !f.keeps(e) {
				continue
			}
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
		// A short batch fully consumed means the log holds nothing else
		// matching up to head: skip the cursor there so callers are not
		// asked to page over the gap event by event.
		if consumed == len(got) && len(got) < batch {
			cursor = head
		}
	}
	return Page{Events: out, NextSeq: cursor, More: cursor < head}, nil
}

// Before returns the newest limit events matching f with Seq < beforeSeq,
// oldest first; beforeSeq zero reads back from the log head.
func (r *Reader) Before(ctx context.Context, f Filter, beforeSeq uint64, limit int) (Page, error) {
	limit = clampLimit(limit)
	head, logFilter, ok, err := r.prepare(ctx, f)
	if err != nil {
		return Page{}, err
	}
	cursor := beforeSeq
	if cursor == 0 || cursor > head {
		cursor = head + 1
	}
	if !ok {
		return Page{NextSeq: head}, nil
	}
	batch := max(limit, minBatch)
	out := make([]events.Event, 0, limit)
	scanned := 0
	for len(out) < limit && cursor > 1 && scanned < limit*scanBudget {
		got, rerr := r.log.ReadBefore(ctx, logFilter, cursor, batch)
		if rerr != nil {
			return Page{}, fmt.Errorf("timeline: read workspace %s history: %w", f.Workspace, rerr)
		}
		consumed := 0
		for _, e := range got {
			consumed++
			cursor = e.Seq
			scanned++
			if !f.keeps(e) {
				continue
			}
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
		if consumed == len(got) && len(got) < batch {
			cursor = 0
		}
	}
	slices.Reverse(out)
	page := Page{Events: out, NextSeq: head, More: cursor > 1}
	if page.More {
		page.OlderSeq = cursor
	}
	return page, nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	}
	return limit
}

// prepare samples the log head and turns f into a log filter; ok is false
// when f can match nothing.
func (r *Reader) prepare(ctx context.Context, f Filter) (uint64, events.Filter, bool, error) {
	head, err := r.log.LastSeq(ctx)
	if err != nil {
		return 0, events.Filter{}, false, fmt.Errorf("timeline: read log head: %w", err)
	}
	logFilter := events.Filter{Workspace: f.Workspace, Types: f.Types}
	if f.Run != "" {
		logFilter.Runs = []domain.RunID{f.Run}
	}
	if f.MissionID != "" {
		runs, merr := r.missions.ListMissionRunIDs(ctx, f.MissionID)
		if merr != nil {
			return 0, events.Filter{}, false, fmt.Errorf("timeline: resolve mission %s: %w", f.MissionID, merr)
		}
		if f.Run != "" {
			runs = slices.DeleteFunc(runs, func(run domain.RunID) bool { return run != f.Run })
		}
		if len(runs) == 0 {
			return head, logFilter, false, nil
		}
		logFilter.Runs = runs
	}
	return head, logFilter, true, nil
}

func (f Filter) keeps(e events.Event) bool {
	if f.Member != "" && e.ActorID != f.Member {
		return false
	}
	return len(f.Types) > 0 || !detailTypes[e.Type]
}
