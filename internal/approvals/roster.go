package approvals

import (
	"sort"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// Presence is one member's live presence: online since their last
// heartbeat, watching when they hold an attach on at least one run.
type Presence struct {
	Member   domain.MemberID
	Session  domain.SessionID
	State    events.PresenceState
	Watching []domain.RunID
	LastSeen time.Time
}

// roster is the in-memory presence table. Members enter it by heartbeat
// or by attaching, and leave it when their heartbeat goes stale - unless
// they still hold an attach, which is itself proof of a live connection
// (the SSH server publishes the closing presence event when it drops).
//
// Presence is per session: the same member can be attached in two
// sessions at once, so each (member, session) pair carries its own row.
type roster struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	members map[rosterKey]*rosterEntry
}

type rosterKey struct {
	member  domain.MemberID
	session domain.SessionID
}

// rosterEntry counts attaches per run rather than holding a set: one
// member may hold several attaches on the same run, and the run stays
// watched until the last of them ends.
type rosterEntry struct {
	lastSeen time.Time
	watching map[domain.RunID]int
}

func newRoster(ttl time.Duration, now func() time.Time) *roster {
	return &roster{ttl: ttl, now: now, members: map[rosterKey]*rosterEntry{}}
}

// entry returns the member's row in session, creating it. Callers hold mu.
func (r *roster) entry(member domain.MemberID, session domain.SessionID) (*rosterEntry, bool) {
	k := rosterKey{member: member, session: session}
	e, ok := r.members[k]
	if !ok {
		e = &rosterEntry{watching: map[domain.RunID]int{}}
		r.members[k] = e
	}
	e.lastSeen = r.now()
	return e, !ok
}

// beat refreshes a member's heartbeat, reporting whether they were absent
// (a transition to online worth publishing).
func (r *roster) beat(member domain.MemberID, session domain.SessionID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, fresh := r.entry(member, session)
	return fresh
}

// watch records member as watching run, which also counts as a heartbeat.
func (r *roster) watch(member domain.MemberID, session domain.SessionID, run domain.RunID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, _ := r.entry(member, session)
	e.watching[run]++
}

// unwatch releases one attach on run, keeping the member online. The run
// stays watched while any of their other attaches on it is still live.
func (r *roster) unwatch(member domain.MemberID, session domain.SessionID, run domain.RunID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, _ := r.entry(member, session)
	if e.watching[run] <= 1 {
		delete(e.watching, run)
		return
	}
	e.watching[run]--
}

// expire removes members whose heartbeat went stale and who hold no
// attach, returning them so the caller can publish the offline events.
func (r *roster) expire() []Presence {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-r.ttl)
	var gone []Presence
	for k, e := range r.members {
		if len(e.watching) > 0 || e.lastSeen.After(cutoff) {
			continue
		}
		gone = append(gone, Presence{
			Member: k.member, Session: k.session,
			State: events.PresenceOffline, LastSeen: e.lastSeen,
		})
		delete(r.members, k)
	}
	sort.Slice(gone, func(i, j int) bool { return less(gone[i], gone[j]) })
	return gone
}

func less(a, b Presence) bool {
	if a.Member != b.Member {
		return a.Member < b.Member
	}
	return a.Session < b.Session
}

// snapshot lists present members, narrowed to a session and to watchers
// of one run when either is set.
func (r *roster) snapshot(session domain.SessionID, run domain.RunID) []Presence {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Presence, 0, len(r.members))
	for k, e := range r.members {
		if session != "" && k.session != session {
			continue
		}
		if run != "" {
			if _, ok := e.watching[run]; !ok {
				continue
			}
		}
		p := Presence{
			Member: k.member, Session: k.session,
			State: events.PresenceOnline, LastSeen: e.lastSeen,
		}
		for w := range e.watching {
			p.Watching = append(p.Watching, w)
		}
		if len(p.Watching) > 0 {
			p.State = events.PresenceWatching
			sort.Slice(p.Watching, func(i, j int) bool { return p.Watching[i] < p.Watching[j] })
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}
