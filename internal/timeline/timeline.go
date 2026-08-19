// Package timeline reads session history back out of the persisted event
// log: one chronological, filterable feed per session, and the audit
// story behind it. It is a reader only - nothing publishes here and it
// owns no storage of its own.
package timeline

import (
	"context"
	"fmt"

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
	// post-filtering, as a multiple of limit: a session whose log is
	// mostly detail events costs a few extra round trips instead of one
	// unbounded scan. A page that stops on the budget reports More.
	scanBudget = 20
	// minBatch keeps small pages from reading the log one row at a time.
	minBatch = 64
)

// detailTypes are the per-run firehoses the feed leaves out by default:
// diff snapshots and adapter activity are run detail, not session
// history. Asking for either by type still returns it.
var detailTypes = map[events.Type]bool{
	events.TypeRunDiff:    true,
	events.TypeAgentEvent: true,
}

// Filter narrows a timeline page. Member matches an event's actor - who
// did it - not the owner of the run it concerns.
type Filter struct {
	Session domain.SessionID
	Run     domain.RunID
	Member  domain.MemberID
	Types   []events.Type
}

// Page is one slice of history. NextSeq is the cursor to pass as the next
// call's afterSeq; More reports that history remains past it.
type Page struct {
	Events  []events.Event
	NextSeq uint64
	More    bool
}

// Reader pages a session's history out of the event log.
type Reader struct {
	log events.EventLog
}

// NewReader returns a Reader over log.
func NewReader(log events.EventLog) *Reader { return &Reader{log: log} }

// Page returns up to limit events matching f with Seq > afterSeq, oldest
// first. Reads are bounded by the log head sampled at entry, so paging
// stays stable while new events arrive.
func (r *Reader) Page(ctx context.Context, f Filter, afterSeq uint64, limit int) (Page, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	head, err := r.log.LastSeq(ctx)
	if err != nil {
		return Page{}, fmt.Errorf("timeline: read log head: %w", err)
	}
	cursor := afterSeq
	if cursor >= head {
		return Page{NextSeq: head}, nil
	}
	batch := max(limit, minBatch)
	logFilter := events.Filter{Session: f.Session, Run: f.Run, Types: f.Types}
	out := make([]events.Event, 0, limit)
	scanned := 0
	for len(out) < limit && cursor < head && scanned < limit*scanBudget {
		got, rerr := r.log.Read(ctx, logFilter, cursor, head, batch)
		if rerr != nil {
			return Page{}, fmt.Errorf("timeline: read session %s history: %w", f.Session, rerr)
		}
		consumed := 0
		for _, e := range got {
			consumed++
			cursor = e.Seq
			scanned++
			if f.Member != "" && e.ActorID != f.Member {
				continue
			}
			if len(f.Types) == 0 && detailTypes[e.Type] {
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
