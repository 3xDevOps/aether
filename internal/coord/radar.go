package coord

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// peerState is one run's authorization to message another. lastSeen is the
// last instant the radar reported the two as overlapping, so the grace
// window that follows is anchored to when the overlap really ended rather
// than to when this process happened to notice.
type peerState struct {
	files    []string
	lastSeen time.Time
	live     bool
}

// expiry is the instant the peer stops being messageable.
func (p *peerState) expiry(grace time.Duration) time.Time { return p.lastSeen.Add(grace) }

// authorizedPeer is one entry of a run's messageable set.
type authorizedPeer struct {
	run    domain.RunID
	files  []string
	state  string
	expiry time.Time
}

// radar is the authorization source: the radar's live overlap view plus
// the grace window each cleared overlap leaves behind.
//
// The live view is re-read from the index on every check, so the active
// set is never stale. The grace window runs from the last instant the two
// runs were known to overlap. A clearing witnessed in a live event anchors
// it at the event, because the index publishes the change the moment it
// happens. A clearing found only by re-reading the index anchors at
// lastSeen instead: overlap events can be dropped by a full subscriber
// buffer, and a window opened on late discovery would run the full grace
// period from then - hours after the overlap actually ended. Anchored to
// lastSeen, a late discovery yields a window that already expired.
type radar struct {
	peers Peers
	grace time.Duration
	now   func() time.Time

	mu        sync.Mutex
	state     map[domain.RunID]map[domain.RunID]*peerState
	refreshed time.Time
	// cleared remembers, per run, the live peers a re-read discovered had
	// just left the index. A coord call can re-read the index after a
	// clearing but before its published event is consumed; without the
	// note, that re-read downgrades or expires the peer against its stale
	// anchor and the event's witnessed-clear anchor is lost. The next
	// observed event consumes the run's notes - confirming a clearing
	// anchors the grace window there, exactly as if the event had been
	// processed first.
	cleared map[domain.RunID]map[domain.RunID]bool
}

// radarRefreshInterval is how long one read of the live overlap view is
// reused. Reading it costs a query plus a full index rebuild under the
// radar's own lock - the lock the diff watcher and run.overlaps need - and
// coord.status is a free call an agent can loop on every connection it
// holds. Coalescing a burst into one recompute keeps that from reaching
// the radar. An announced change invalidates the cache immediately, so the
// window only ever spans a period in which nothing was reported to have
// changed.
const radarRefreshInterval = time.Second

func newRadar(peers Peers, grace time.Duration, now func() time.Time) *radar {
	return &radar{
		peers:   peers,
		grace:   grace,
		now:     now,
		state:   make(map[domain.RunID]map[domain.RunID]*peerState),
		cleared: make(map[domain.RunID]map[domain.RunID]bool),
	}
}

// observe folds one run's current peer set into the state, starting the
// grace window of every peer that just left it. An announcement is news,
// so it also drops the memoized view: the next check re-reads.
func (r *radar) observe(run domain.RunID, current map[domain.RunID][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apply(run, current, r.now(), true)
	r.refreshed = time.Time{}
}

// apply records run's current peers at instant now. observed marks a
// live report from the index rather than a re-read: a live peer it
// removes had its overlap end at now, so the grace window anchors there.
// A re-read keeps the stale lastSeen anchor, since the overlap may have
// ended any time after it. Callers hold r.mu.
func (r *radar) apply(run domain.RunID, current map[domain.RunID][]string, now time.Time, observed bool) {
	peers := r.state[run]
	if peers == nil {
		peers = make(map[domain.RunID]*peerState, len(current))
		r.state[run] = peers
	}
	for id, files := range current {
		peers[id] = &peerState{files: files, lastSeen: now, live: true}
	}
	for id, st := range peers {
		if _, live := current[id]; live {
			continue
		}
		if observed && st.live {
			st.lastSeen = now
		}
		if !observed && st.live {
			r.markCleared(run, id)
		}
		st.live = false
		if !st.expiry(r.grace).After(now) {
			delete(peers, id)
		}
	}
	if observed {
		// A clearing a re-read found first is witnessed here after all:
		// re-anchor its grace window at the event, resurrecting the entry
		// if the re-read already expired it against the stale anchor.
		for id := range r.cleared[run] {
			if _, live := current[id]; live {
				continue
			}
			st := peers[id]
			if st == nil {
				st = &peerState{}
				peers[id] = st
			}
			st.lastSeen, st.live = now, false
		}
		delete(r.cleared, run)
	}
	if len(peers) == 0 {
		delete(r.state, run)
	}
}

// markCleared notes that a re-read saw one of run's live peers leave the
// index, for the next observed event to consume. Callers hold r.mu.
func (r *radar) markCleared(run, peer domain.RunID) {
	m := r.cleared[run]
	if m == nil {
		m = make(map[domain.RunID]bool, 1)
		r.cleared[run] = m
	}
	m[peer] = true
}

// refresh re-reads the radar's live view and folds it in, so a run whose
// overlaps changed while nothing was listening still gets an accurate
// answer, and expired grace windows are dropped. A read taken within the
// last radarRefreshInterval is reused rather than repeated.
func (r *radar) refresh(ctx context.Context) error {
	if r.memoized() {
		return nil
	}
	entries, err := r.peers.Overlaps(ctx)
	if err != nil {
		return fmt.Errorf("coord: read the conflict radar: %w", err)
	}
	live := make(map[domain.RunID]map[domain.RunID][]string, len(entries))
	for _, e := range entries {
		peers := make(map[domain.RunID][]string, len(e.With))
		for _, p := range e.With {
			peers[p.RunID] = p.Files
		}
		live[e.RunID] = peers
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for run := range r.state {
		if _, ok := live[run]; !ok {
			live[run] = nil
		}
	}
	for run, peers := range live {
		r.apply(run, peers, now, false)
	}
	r.refreshed = now
	return nil
}

// memoized reports whether the live view was read recently enough to reuse.
func (r *radar) memoized() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.refreshed.IsZero() && r.now().Sub(r.refreshed) < radarRefreshInterval
}

// authorizedSet returns run's messageable peers - active overlaps first,
// then unexpired grace windows - sorted by run ID.
func (r *radar) authorizedSet(ctx context.Context, run domain.RunID) ([]authorizedPeer, error) {
	if err := r.refresh(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	peers := r.state[run]
	out := make([]authorizedPeer, 0, len(peers))
	for id, st := range peers {
		p := authorizedPeer{run: id, files: st.files, state: protocol.CoordPeerActive}
		if !st.live {
			p.state, p.expiry, p.files = protocol.CoordPeerGrace, st.expiry(r.grace), nil
		}
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].run < out[b].run })
	return out, nil
}

// authorized reports whether from may message to, and under which state.
// An empty state means the send is denied.
func (r *radar) authorized(ctx context.Context, from, to domain.RunID) (authorizedPeer, error) {
	set, err := r.authorizedSet(ctx, from)
	if err != nil {
		return authorizedPeer{}, err
	}
	for _, p := range set {
		if p.run == to {
			return p, nil
		}
	}
	return authorizedPeer{}, nil
}

// forget drops a run's authorization state; called when its coordination
// directory goes away.
func (r *radar) forget(run domain.RunID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, run)
	delete(r.cleared, run)
}
