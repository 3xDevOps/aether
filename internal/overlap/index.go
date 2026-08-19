// Package overlap is the conflict radar: an in-memory index of which
// active runs are touching which files, built from the run.diff snapshots
// on the event bus. It is early warning only - nothing here pauses,
// queues, locks, or refuses anything.
package overlap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// RunLookup resolves the currently active runs and the workspace a
// session belongs to; satisfied by store.Store.
type RunLookup interface {
	ListActiveRuns(ctx context.Context) ([]*domain.Run, error)
	GetSession(ctx context.Context, id domain.SessionID) (*domain.Session, error)
}

// SeqSource reports the newest persisted event sequence; satisfied by
// events.EventLog. Its value at Start marks where replayed history ends
// and live events begin. Nil means the bus has no log, so there is no
// history to replay.
type SeqSource interface {
	LastSeq(ctx context.Context) (uint64, error)
}

// Peer is one other active run touching files a run also touches.
type Peer struct {
	RunID    domain.RunID
	MemberID domain.MemberID
	Files    []string
}

// Entry is one run's full overlap set. Every overlapping pair appears
// from both sides, so callers can key entries by RunID alone.
type Entry struct {
	RunID domain.RunID
	With  []Peer
}

// runState is what the index knows about one run: the session to publish
// its overlap changes under, the workspace whose repository its files
// belong to, and the files its last diff snapshot reported. live goes
// false when the run reaches a terminal status; the entry survives until
// its overlap has been announced as cleared. An empty workspace means it
// could not be resolved, and the run is left out of the overlap view
// rather than matched against unrelated repositories.
type runState struct {
	session   domain.SessionID
	workspace domain.WorkspaceID
	files     map[string]struct{}
	live      bool
}

// Index tracks file overlap across active runs. It consumes run.diff
// (each snapshot carries a run's whole file set, so the newest one is the
// whole truth) and run.status (a terminal run stops overlapping).
type Index struct {
	bus  events.Bus
	runs RunLookup
	log  SeqSource

	// rebuildUpto is the log head at Start. Events at or below it are
	// replayed history; only run() reads it, after Start wrote it.
	rebuildUpto uint64

	mu         sync.Mutex
	state      map[domain.RunID]*runState
	workspaces map[domain.SessionID]domain.WorkspaceID
	announced  map[domain.RunID][]events.OverlapPeer
	sub        events.Subscription
	closed     bool

	wg sync.WaitGroup
}

// NewIndex builds an index; call Start to begin consuming events. log is
// the bus's event log, used only to find where its history ends; nil when
// the bus has none.
func NewIndex(bus events.Bus, runs RunLookup, log SeqSource) *Index {
	return &Index{
		bus:        bus,
		runs:       runs,
		log:        log,
		state:      make(map[domain.RunID]*runState),
		workspaces: make(map[domain.SessionID]domain.WorkspaceID),
		announced:  make(map[domain.RunID][]events.OverlapPeer),
	}
}

// Start subscribes to the diff and status streams and begins consuming.
// ctx bounds only the setup; the index runs until Close.
func (i *Index) Start(ctx context.Context) error {
	if i.log != nil {
		last, err := i.log.LastSeq(ctx)
		if err != nil {
			return fmt.Errorf("overlap: read log head: %w", err)
		}
		i.rebuildUpto = last
	}
	sub, err := i.subscribe(ctx, 0)
	if err != nil {
		return err
	}
	i.mu.Lock()
	i.sub = sub
	i.mu.Unlock()
	i.wg.Add(1)
	go i.run()
	return nil
}

// subscribe replays from afterSeq. At Start that is zero - the index
// holds no durable state, so a full replay of run.diff is a full rebuild.
// After dropped events it is the last consumed cursor, so recovery costs
// the gap and not the whole log. A bus without a log has nothing to
// replay and degrades to live events only.
func (i *Index) subscribe(ctx context.Context, afterSeq uint64) (events.Subscription, error) {
	opts := events.SubscribeOptions{
		Filter:   events.Filter{Types: []events.Type{events.TypeRunDiff, events.TypeRunStatus}},
		Replay:   true,
		AfterSeq: afterSeq,
	}
	sub, err := i.bus.Subscribe(ctx, opts)
	if errors.Is(err, events.ErrNoLog) {
		opts.Replay = false
		sub, err = i.bus.Subscribe(ctx, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("overlap: subscribe: %w", err)
	}
	return sub, nil
}

// Close stops consuming. Idempotent, and safe before Start.
func (i *Index) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	sub := i.sub
	i.mu.Unlock()
	if sub != nil {
		_ = sub.Close()
	}
	i.wg.Wait()
	return nil
}

func (i *Index) run() {
	defer i.wg.Done()
	ctx := context.Background()
	var cursor uint64
	for {
		i.mu.Lock()
		sub, closed := i.sub, i.closed
		i.mu.Unlock()
		if sub == nil || closed {
			return
		}
		dropped := sub.Dropped()
		lost := false
		for e := range sub.Events() {
			if d := sub.Dropped(); d > dropped {
				lost = true
				break
			}
			cursor = e.Seq
			if i.apply(ctx, e) {
				// Replayed history rebuilds today's overlap set; its
				// intermediate transitions already happened and must not
				// be announced a second time on every restart.
				i.refresh(ctx, e.Seq > i.rebuildUpto)
			}
		}
		if !lost {
			return
		}
		// The bus discarded buffered events, so some run's file set may be
		// stale. Replay the gap rather than carry a lie forward.
		_ = sub.Close()
		next, err := i.subscribe(ctx, cursor)
		if err != nil {
			slog.Warn("overlap: resubscribe after dropped events failed", "error", err)
			return
		}
		i.mu.Lock()
		if i.closed {
			i.mu.Unlock()
			_ = next.Close()
			return
		}
		i.sub = next
		i.mu.Unlock()
	}
}

// workspaceOf resolves the workspace a session belongs to. A session's
// workspace never changes, so successful answers are cached; a failure is
// left uncached and retried on the run's next diff snapshot.
func (i *Index) workspaceOf(ctx context.Context, id domain.SessionID) (domain.WorkspaceID, error) {
	if id == "" {
		return "", nil
	}
	i.mu.Lock()
	ws, ok := i.workspaces[id]
	i.mu.Unlock()
	if ok {
		return ws, nil
	}
	s, err := i.runs.GetSession(ctx, id)
	if err != nil {
		return "", fmt.Errorf("overlap: resolve workspace of session %s: %w", id, err)
	}
	i.mu.Lock()
	i.workspaces[id] = s.WorkspaceID
	i.mu.Unlock()
	return s.WorkspaceID, nil
}

// apply folds one event into the index, reporting whether it changed the
// state the overlap view is derived from.
func (i *Index) apply(ctx context.Context, e events.Event) bool {
	if e.RunID == "" {
		return false
	}
	switch p := e.Payload.(type) {
	case events.RunDiffPayload:
		files := make(map[string]struct{}, len(p.Files))
		for _, f := range p.Files {
			files[f.Path] = struct{}{}
		}
		ws, err := i.workspaceOf(ctx, e.SessionID)
		if err != nil {
			slog.Warn("overlap: run left out of the overlap view", "run", e.RunID, "error", err)
		}
		i.mu.Lock()
		st := i.state[e.RunID]
		if st == nil {
			st = &runState{}
			i.state[e.RunID] = st
		}
		st.session, st.workspace, st.files, st.live = e.SessionID, ws, files, true
		i.mu.Unlock()
		return true
	case events.RunStatusPayload:
		if !p.To.Terminal() {
			return false
		}
		i.mu.Lock()
		if st := i.state[e.RunID]; st != nil {
			st.files, st.live = nil, false
		}
		i.mu.Unlock()
		return true
	}
	return false
}

// Overlaps returns the current overlap set per run. Runs the store no
// longer reports as active are dropped first: a finished run's flags
// clear even if its terminal status event never reached the index.
func (i *Index) Overlaps(ctx context.Context) ([]Entry, error) {
	active, err := i.runs.ListActiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("overlap: list active runs: %w", err)
	}
	member := make(map[domain.RunID]domain.MemberID, len(active))
	for _, r := range active {
		member[r.ID] = r.MemberID
	}
	i.mu.Lock()
	for id, st := range i.state {
		if _, ok := member[id]; !ok {
			st.files, st.live = nil, false
		}
	}
	i.mu.Unlock()

	view := i.refresh(ctx, true)
	out := make([]Entry, 0, len(view))
	for id, peers := range view {
		with := make([]Peer, 0, len(peers))
		for _, p := range peers {
			with = append(with, Peer{RunID: p.RunID, MemberID: member[p.RunID], Files: p.Files})
		}
		out = append(out, Entry{RunID: id, With: with})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].RunID < out[b].RunID })
	return out, nil
}

// refresh recomputes the overlap view and returns it. When announce is
// set it publishes a run.overlap event for every run whose set changed
// (an empty set means the overlap cleared); otherwise it only records the
// change, so a later live change still publishes the real delta.
func (i *Index) refresh(ctx context.Context, announce bool) map[domain.RunID][]events.OverlapPeer {
	type change struct {
		run     domain.RunID
		session domain.SessionID
		peers   []events.OverlapPeer
	}
	i.mu.Lock()
	view := i.view()
	var changed []change
	for id, st := range i.state {
		peers := view[id]
		if reflect.DeepEqual(peers, i.announced[id]) {
			continue
		}
		if len(peers) == 0 {
			delete(i.announced, id)
		} else {
			i.announced[id] = peers
		}
		changed = append(changed, change{run: id, session: st.session, peers: peers})
	}
	for id, st := range i.state {
		if !st.live && len(i.announced[id]) == 0 {
			delete(i.state, id)
		}
	}
	i.mu.Unlock()

	if !announce {
		return view
	}
	for _, c := range changed {
		if c.session == "" {
			continue
		}
		if _, err := i.bus.Publish(ctx, events.Event{
			SessionID: c.session,
			RunID:     c.run,
			Payload:   events.OverlapPayload{With: c.peers},
		}); err != nil {
			slog.Warn("overlap: publish overlap change failed", "run", c.run, "error", err)
		}
	}
	return view
}

// workspaceFile is one path within one workspace's repository. Two runs
// conflict only when both parts match: the same path in two different
// workspaces is two different files.
type workspaceFile struct {
	workspace domain.WorkspaceID
	path      string
}

// view derives run -> peers from the live file sets. Callers hold i.mu.
func (i *Index) view() map[domain.RunID][]events.OverlapPeer {
	byFile := make(map[workspaceFile][]domain.RunID)
	for id, st := range i.state {
		if !st.live || st.workspace == "" {
			continue
		}
		for f := range st.files {
			key := workspaceFile{workspace: st.workspace, path: f}
			byFile[key] = append(byFile[key], id)
		}
	}
	shared := make(map[domain.RunID]map[domain.RunID][]string)
	for f, ids := range byFile {
		if len(ids) < 2 {
			continue
		}
		for _, a := range ids {
			for _, b := range ids {
				if a == b {
					continue
				}
				if shared[a] == nil {
					shared[a] = make(map[domain.RunID][]string)
				}
				shared[a][b] = append(shared[a][b], f.path)
			}
		}
	}
	out := make(map[domain.RunID][]events.OverlapPeer, len(shared))
	for a, peers := range shared {
		list := make([]events.OverlapPeer, 0, len(peers))
		for b, files := range peers {
			sort.Strings(files)
			list = append(list, events.OverlapPeer{RunID: b, Files: files})
		}
		sort.Slice(list, func(x, y int) bool { return list[x].RunID < list[y].RunID })
		out[a] = list
	}
	return out
}
