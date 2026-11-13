package gitengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/guest"
)

const (
	vmWatchRetryMin  = time.Second
	vmWatchRetryMax  = 30 * time.Second
	maxDiffPathBytes = 4096
)

// vmWatch follows the diff watch the git service runs in a VM and records
// each snapshot it reports, as a diffWatch records the snapshots it takes.
// Everything the VM sends is checked before it is recorded.
type vmWatch struct {
	e        *Engine
	run      domain.RunID
	meta     runMeta
	ctx      context.Context
	cancel   context.CancelFunc
	finished chan struct{}
	// lastChange is the server's time of the VM's last file change report.
	lastChange atomic.Int64
	// flushing lifts the rate limit for the last snapshot stop asks for.
	flushing atomic.Bool

	// loop-goroutine state
	lastTree   string
	lastBase   string
	historyGap bool
	lastWarn   time.Time
	// tokens and tokensAt limit what the VM gets recorded to what a diff
	// watch reports at most: a snapshot and a moved HEAD per MinInterval.
	tokens   float64
	tokensAt time.Time
}

const vmWatchBurst = 2

func (w *vmWatch) allow() bool {
	now := time.Now()
	w.tokens = min(vmWatchBurst, w.tokens+vmWatchBurst*float64(now.Sub(w.tokensAt))/float64(w.e.cfg.MinInterval))
	w.tokensAt = now
	if w.tokens < 1 && !w.flushing.Load() {
		return false
	}
	w.tokens = max(0, w.tokens-1)
	return true
}

func (e *Engine) startVMWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, meta runMeta) error {
	if e.cfg.VM == nil {
		return fmt.Errorf("gitengine: session %s has its checkout in a VM, and this server runs no VMs", run)
	}
	store, err := e.vmStore(ctx, run, meta)
	if err != nil {
		return err
	}
	lastTree := e.lastSnapshotTree(run)
	if lastTree == "" {
		out, _, treeErr := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "refs/aether/base")
		if treeErr != nil {
			return treeErr
		}
		lastTree = strings.TrimSpace(out)
	}
	if active, regErr := e.registerWatch(workspace, run, meta); regErr != nil || active {
		return regErr
	}
	_, gapErr := os.Stat(filepath.Join(store, "history-gap"))
	watchCtx, cancel := context.WithCancel(context.Background())
	w := &vmWatch{
		e: e, run: run, meta: meta, ctx: watchCtx, cancel: cancel, finished: make(chan struct{}),
		lastTree: lastTree, historyGap: !errors.Is(gapErr, os.ErrNotExist),
		tokens: vmWatchBurst, tokensAt: time.Now(),
	}
	e.mu.Lock()
	if e.closed || e.watches[run] != nil {
		e.mu.Unlock()
		cancel()
		return nil
	}
	e.watches[run] = w
	e.mu.Unlock()
	go w.loop()
	return nil
}

func (w *vmWatch) changedAt() (time.Time, bool) {
	nanos := w.lastChange.Load()
	if nanos == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, nanos), true
}

// stop with final asks the VM for one last snapshot first, which this
// watch records before the VM's flush returns.
func (w *vmWatch) stop(final bool) {
	if final {
		w.flushing.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 2*snapshotTimeout)
		g, err := w.e.cfg.VM.Guest(ctx, vmOf(w.run))
		if err == nil {
			err = g.Flush(ctx, string(w.run))
		}
		cancel()
		if err != nil {
			slog.Warn("gitengine: last diff snapshot of a session VM failed", "run", string(w.run), "error", err)
		}
	}
	w.cancel()
	<-w.finished
}

func (w *vmWatch) loop() {
	defer close(w.finished)
	delay := vmWatchRetryMin
	for {
		heard, err := w.follow()
		if w.ctx.Err() != nil {
			return
		}
		if heard {
			delay = vmWatchRetryMin
		}
		if now := time.Now(); now.Sub(w.lastWarn) >= diffWarnInterval {
			w.lastWarn = now
			slog.Warn("gitengine: diff watch of a session VM interrupted; retrying", "run", string(w.run), "error", err)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, vmWatchRetryMax)
	}
}

// follow reads the VM's watch until it breaks, reporting whether any event
// arrived.
func (w *vmWatch) follow() (bool, error) {
	g, err := w.e.cfg.VM.Guest(w.ctx, vmOf(w.run))
	if err != nil {
		return false, err
	}
	watch, err := g.Watch(w.ctx, string(w.run))
	if err != nil {
		return false, err
	}
	heard := false
	for {
		ev, err := watch.Next()
		if err != nil {
			return heard, err
		}
		heard = true
		var failure error
		switch {
		case ev.Changed:
			w.lastChange.Store(time.Now().UnixNano())
			continue
		case !w.allow():
			failure = errors.New("gitengine: the session VM reports faster than its diff watch snapshots")
		case ev.Diff != nil:
			failure = w.record(ev.Diff)
		default:
			failure = w.e.publishVMHead(w.ctx, w.run, w.meta, ev.Head)
		}
		if failure != nil {
			if now := time.Now(); now.Sub(w.lastWarn) >= diffWarnInterval {
				w.lastWarn = now
				slog.Warn("gitengine: refused what a session VM reported", "run", string(w.run), "error", failure)
			}
		}
		if err := watch.Ack(ev.Seq, failure); err != nil {
			return heard, err
		}
	}
}

// record checks one snapshot the VM reports, copies its objects and records
// it as an interval of the run's history.
func (w *vmWatch) record(data []byte) error {
	ctx, cancel := context.WithTimeout(w.ctx, snapshotTimeout)
	defer cancel()
	payload, err := decodeVMDiff(data)
	if err != nil {
		return err
	}
	if payload.Tree == "" {
		w.historyGap = true
		payload.HistoryGap, payload.ParentTree = true, ""
		return w.e.publishRunDiff(ctx, w.run, payload)
	}
	store, err := w.e.snapshotStorePath(w.run)
	if err != nil {
		return err
	}
	base, err := w.e.checkVMSnapshot(ctx, w.run, w.meta, store, payload, w.lastTree)
	if err != nil {
		return err
	}
	tree, err := w.e.recordWatchSnapshot(ctx, w.run, base, func() (string, error) {
		if retainErr := w.e.retainSnapshot(ctx, store, payload.Tree); retainErr != nil {
			return "", retainErr
		}
		return payload.Tree, nil
	}, func(tree string) error {
		if tree == w.lastTree && base == w.lastBase && !w.historyGap {
			return nil
		}
		next := payload
		next.Base, next.Tree, next.ParentTree, next.HistoryGap = base, tree, w.lastTree, w.historyGap
		return w.e.publishRunDiff(ctx, w.run, next)
	})
	if tree != "" {
		w.lastTree = tree
	}
	if err != nil {
		w.historyGap = true
		return err
	}
	w.lastBase, w.historyGap = base, false
	if tree != payload.Tree {
		return errors.New("gitengine: an earlier interval was recorded instead; send the snapshot again")
	}
	return nil
}

// checkVMSnapshot copies a reported snapshot's objects and returns the base
// its stats count from, once the tree is a tree within the snapshot bound,
// the head a commit, and the base a commit between the fork point and the
// head.
func (e *Engine) checkVMSnapshot(ctx context.Context, run domain.RunID, meta runMeta, store string, p events.RunDiffPayload, lastTree string) (string, error) {
	wants := []string{p.Tree}
	if p.Head != "" {
		wants = append(wants, p.Head)
	}
	if err := e.fetchVMObjects(ctx, run, store, wants, []string{meta.Base, lastTree}, maxVMPackBytes); err != nil {
		return "", err
	}
	if kind := e.objectType(ctx, store, p.Tree); kind != "tree" {
		return "", fmt.Errorf("%w: session %s reported a %s as its snapshot tree", ErrInvalidObjectID, run, kind)
	}
	if err := e.checkTreeBounds(ctx, store, p.Tree, MaxSnapshotInputBytes, ErrSnapshotStorageLimit); err != nil {
		return "", err
	}
	if p.Head != "" && e.objectType(ctx, store, p.Head) != "commit" {
		return "", fmt.Errorf("%w: session %s reported %s as its HEAD commit", ErrInvalidObjectID, run, p.Head)
	}
	if p.Base == "" || p.Base == meta.Base {
		return meta.Base, nil
	}
	if p.Head == "" || !e.isAncestor(ctx, store, meta.Base, p.Base) || !e.isAncestor(ctx, store, p.Base, p.Head) {
		return "", fmt.Errorf("%w: session %s reported base %s, which is not between its fork point and HEAD", ErrInvalidObjectID, run, p.Base)
	}
	return p.Base, nil
}

func (e *Engine) isAncestor(ctx context.Context, store, ancestor, commit string) bool {
	_, _, err := e.gitBareBounded(ctx, store, 0, "merge-base", "--is-ancestor", ancestor, commit)
	return err == nil
}

// checkTreeBounds is checkSnapshotBounds for a tree in a store with no
// checkout beside it.
func (e *Engine) checkTreeBounds(ctx context.Context, store, tree string, limit uint64, limitErr error) error {
	listingLimit := int(limit) + (1 << 20)
	listing, over, err := e.gitBareBounded(ctx, store, listingLimit, "ls-tree", "-r", "-l", "-z", tree, "--")
	if err != nil {
		return err
	}
	if over {
		return fmt.Errorf("%w: snapshot listing exceeds %d bytes", limitErr, listingLimit)
	}
	var total uint64
	for record := range strings.SplitSeq(listing, "\x00") {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(record[:tab])
		if len(fields) != 4 || fields[1] != "blob" {
			continue
		}
		var size uint64
		if _, scanErr := fmt.Sscan(fields[3], &size); scanErr != nil {
			return fmt.Errorf("gitengine: parse snapshot size %q: %w", fields[3], scanErr)
		}
		if size > limit-total {
			return fmt.Errorf("%w: retained Git input exceeds %d bytes", limitErr, limit)
		}
		total += size
	}
	return nil
}

var vmSnapshotErrors = map[string]bool{
	"Snapshot history unavailable; current change statistics remain live.":               true,
	"Snapshot history unavailable: visible input exceeds 128 MiB or free disk headroom.": true,
}

// decodeVMDiff reads the run.diff payload a VM sent, refusing anything a
// diffWatch could not have produced.
func decodeVMDiff(data []byte) (events.RunDiffPayload, error) {
	var p events.RunDiffPayload
	if len(data) > guest.MaxDiffEventBytes {
		return p, fmt.Errorf("gitengine: diff snapshot of %d bytes exceeds %d", len(data), guest.MaxDiffEventBytes)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("gitengine: decode diff snapshot: %w", err)
	}
	for _, id := range []string{p.Base, p.Head, p.Tree} {
		if id != "" && !validObjectID(id) {
			return p, fmt.Errorf("%w: %q", ErrInvalidObjectID, id)
		}
	}
	for _, f := range p.Files {
		if f.Path == "" || len(f.Path) > maxDiffPathBytes || strings.ContainsRune(f.Path, 0) || f.Additions < 0 || f.Deletions < 0 {
			return p, fmt.Errorf("gitengine: diff snapshot has an invalid entry %q", f.Path)
		}
		switch f.Status {
		case "", "added", "deleted", "modified":
		default:
			return p, fmt.Errorf("gitengine: diff snapshot has status %q", f.Status)
		}
	}
	if p.SnapshotError != "" && !vmSnapshotErrors[p.SnapshotError] {
		p.SnapshotError = "Snapshot history unavailable; current change statistics remain live."
	}
	p.HeadSubject = strings.ToValidUTF8(p.HeadSubject, "")
	if len(p.HeadSubject) > maxHeadSubjectBytes {
		cut := maxHeadSubjectBytes
		for cut > 0 && !utf8.RuneStart(p.HeadSubject[cut]) {
			cut--
		}
		p.HeadSubject = p.HeadSubject[:cut]
	}
	p.Repo = ""
	return p, nil
}

// publishVMHead publishes the run branch at head, a commit the VM reports
// its checkout's HEAD moved to: the server copies the commit's objects from
// the VM and fetches the branch from them, as publishRunBranch fetches it
// from a checkout on its own disk.
func (e *Engine) publishVMHead(ctx context.Context, run domain.RunID, meta runMeta, head string) error {
	if !validObjectID(head) {
		return fmt.Errorf("%w: %q", ErrInvalidObjectID, head)
	}
	if err := e.checkBranchName(ctx, meta.Branch); err != nil {
		return err
	}
	repo, err := e.existingRepoPath(meta.Workspace)
	if err != nil {
		return err
	}
	// The branch is staged in the run's store, which the watch and
	// PublishRunBranch share.
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	store, err := e.vmStore(ctx, run, meta)
	if err != nil {
		return err
	}
	ref := "refs/heads/" + meta.Branch
	before, _ := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	if err = e.fetchVMObjects(ctx, run, store, []string{head}, []string{meta.Base, before}, maxVMHistoryBytes); err != nil {
		return err
	}
	if kind := e.objectType(ctx, store, head); kind != "commit" {
		return fmt.Errorf("%w: session %s reported a %s as its HEAD", ErrInvalidObjectID, run, kind)
	}
	if _, err = e.git(ctx, store, "update-ref", ref, head); err != nil {
		return fmt.Errorf("gitengine: stage checkout HEAD %s: %w", head, err)
	}
	if _, err = e.git(ctx, repo, "fetch", "--quiet", store, "+"+ref+":"+ref); err != nil {
		return fmt.Errorf("gitengine: publish session branch %s: %w", run, err)
	}
	after, err := e.git(ctx, repo, "rev-parse", "--verify", ref)
	if err != nil {
		return err
	}
	if after != head {
		return fmt.Errorf("gitengine: published session branch %s resolved to %s, want %s", run, after, head)
	}
	if after != before {
		e.publishBranch(ctx, run, before, after)
	}
	return nil
}

// vmPatch renders a checkout in a VM: recorded ranges from the server's
// history, the live diff from the VM, and the last recorded snapshot while
// the VM is not running.
func (e *Engine) vmPatch(ctx context.Context, run domain.RunID, checkout string, req PatchRequest, maxBytes int) (Patch, error) {
	switch {
	case req.To != "" && req.From == "":
		return Patch{}, fmt.Errorf("%w: a snapshot range needs a from tree", ErrInvalidObjectID)
	case req.To != "":
		return e.rangePatch(ctx, run, checkout, req.From, req.To, maxBytes)
	}
	if req.From != "" && !validObjectID(req.From) {
		return Patch{}, fmt.Errorf("%w: %q", ErrInvalidObjectID, req.From)
	}
	g, err := e.cfg.VM.Guest(ctx, vmOf(run))
	if errors.Is(err, guest.ErrVMNotRunning) {
		return e.recordedPatch(ctx, run, req.From, maxBytes)
	}
	if err != nil {
		return Patch{}, err
	}
	p, err := g.Patch(ctx, string(run), req.From, maxBytes)
	if err != nil {
		return Patch{}, vmErr(err)
	}
	if !validObjectID(p.Base) || p.ForkTree != "" && !validObjectID(p.ForkTree) {
		return Patch{}, fmt.Errorf("gitengine: session %s rendered a diff against %q", run, p.Base)
	}
	return Patch{Base: p.Base, Text: p.Text, Truncated: p.Truncated, ForkTree: p.ForkTree}, nil
}

var vmReasons = map[guest.GitReason]error{
	guest.ReasonSnapshotTreeMissing:  ErrSnapshotTreeMissing,
	guest.ReasonInvalidObjectID:      ErrInvalidObjectID,
	guest.ReasonSnapshotStorageLimit: ErrSnapshotStorageLimit,
	guest.ReasonCheckoutNotFound:     ErrCheckoutNotFound,
}

// vmErr gives a git service failure the engine's own error for its reason.
func vmErr(err error) error {
	var failure *guest.GitError
	if errors.As(err, &failure) {
		if sentinel, ok := vmReasons[failure.Reason]; ok {
			return fmt.Errorf("%w: %s", sentinel, failure.Message)
		}
	}
	return err
}

// GitReason is the reason the git service reports err under, empty for a
// failure the server has no error for.
func GitReason(err error) guest.GitReason {
	for reason, sentinel := range vmReasons {
		if errors.Is(err, sentinel) {
			return reason
		}
	}
	return ""
}
