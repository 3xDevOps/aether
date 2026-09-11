package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// snapshotTimeout bounds the git work of a single diff snapshot.
const snapshotTimeout = 30 * time.Second
const diffWarnInterval = time.Minute

// diffWatch watches one run checkout for file changes and captures diff
// snapshots on quiescence (§6.4 of the Wave 1 contract): a snapshot fires
// QuietPeriod after the last change, at least MinInterval after the
// previous snapshot, and unconditionally after MaxInterval of sustained
// churn. It goes quiet when the tree is quiet.
type diffWatch struct {
	e        *Engine
	run      domain.RunID
	checkout string
	base     string
	watcher  *fsnotify.Watcher

	// gitIgnoredDirs is the repository-relative set of ignored directories
	// reported by git. visibleFiles and visibleDirs are the tracked or
	// otherwise unignored paths that must remain reachable below an ignored
	// directory. Keeping this state from git, rather than matching only
	// directory names, preserves tracked files and negated rules.
	// watchedDirs is the set of directories currently registered with
	// fsnotify. metadataDirs are deliberately retained across ignore-state
	// reconciliation: Git metadata drives branch and ignore invalidation even
	// though it is outside the worktree's visible file set.
	watchedDirs  map[string]struct{}
	metadataDirs map[string]struct{}

	gitIgnoredDirs map[string]struct{}
	visibleFiles   map[string]struct{}
	visibleDirs    map[string]struct{}

	// ignoreRefreshPending coalesces filesystem events that can change Git's
	// answer. In particular, a new file below an ignored directory may be
	// re-included by a negation that did not exist at startup.
	ignoreRefreshPending bool
	ignoreRefreshAt      time.Time

	lastChange atomic.Int64 // unix nanos of the last fsnotify event; 0 = none

	stopOnce sync.Once
	done     chan struct{}
	finished chan struct{}

	// loop-goroutine state
	dirty            bool
	headDirty        bool
	lastEvent        time.Time
	headEvent        time.Time
	headRetryAt      time.Time
	lastSnap         time.Time
	lastHead         string
	lastFiles        []events.FileDiffStat
	lastTree         string
	lastSnapshotWarn time.Time
	lastPublishWarn  time.Time
	lastTreeWarn     time.Time
}

// StartDiffWatch begins diff-snapshot watching for a run's checkout,
// scoping published events to workspace. It also registers the run in the
// watch registry (run -> workspace/branch), which outlives StopDiffWatch
// and is what allows git.branch events to carry a workspace scope.
// Idempotent while a watch is already active.
func (e *Engine) StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error {
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return err
	}
	head, err := e.git(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		slog.Warn("gitengine: diff snapshot failed", "run", string(run), "error", err)
	}
	// The first interval of a fresh run is measured from the fork-point
	// tree; a run whose store already records a snapshot resumes its chain,
	// so a watch or server restart does not lose an interval boundary.
	lastTree := e.lastSnapshotTree(run)
	if lastTree == "" {
		lastTree, _ = e.git(ctx, checkout, "rev-parse", meta.Base+"^{tree}")
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return fmt.Errorf("gitengine: engine closed")
	}
	e.registry[run] = runInfo{workspace: workspace, branch: meta.Branch}
	if _, active := e.watches[run]; active {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("gitengine: start watcher: %w", err)
	}
	w := &diffWatch{
		e:            e,
		run:          run,
		checkout:     checkout,
		base:         meta.Base,
		watcher:      watcher,
		watchedDirs:  make(map[string]struct{}),
		metadataDirs: make(map[string]struct{}),
		done:         make(chan struct{}),
		finished:     make(chan struct{}),
		// Nothing has been published yet, so the MinInterval floor must not
		// delay the first snapshot: only QuietPeriod gates it.
		lastSnap: time.Now().Add(-e.cfg.MinInterval),
		lastHead: head,
		lastTree: lastTree,
	}
	if err := w.loadIgnoreState(ctx); err != nil {
		// A failure to ask git about excludes must never turn a working
		// checkout blind. Falling back to the full walk costs watches, but
		// preserves change detection and makes the failure visible.
		slog.Warn("gitengine: cannot load git ignore state; watching all directories",
			"run", string(run), "error", err)
	}
	if err := w.addRecursive(checkout); err != nil {
		_ = watcher.Close()
		return err
	}
	refDir := filepath.Dir(filepath.Join(checkout, ".git", "refs", "heads", meta.Branch))
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("gitengine: create ref watch directory %s: %w", refDir, err)
	}
	for _, dir := range []string{
		filepath.Join(checkout, ".git"),
		filepath.Join(checkout, ".git", "info"),
		refDir,
	} {
		if err := w.addWatch(dir, true); err != nil {
			_ = watcher.Close()
			return fmt.Errorf("gitengine: watch %s: %w", dir, err)
		}
	}

	e.mu.Lock()
	if e.closed || e.watches[run] != nil {
		e.mu.Unlock()
		_ = watcher.Close()
		return nil
	}
	e.watches[run] = w
	e.mu.Unlock()

	go w.loop()
	return nil
}

// StopDiffWatch stops the run's diff watcher. The registry entry survives
// so later branch publications keep their workspace scope. Idempotent.
func (e *Engine) StopDiffWatch(run domain.RunID) {
	e.mu.Lock()
	w := e.watches[run]
	delete(e.watches, run)
	e.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

// LastFileChange reports the wall-clock time of the last file-change event
// observed in the run's checkout; false when the run has no active watch or
// no change has been seen yet.
func (e *Engine) LastFileChange(run domain.RunID) (time.Time, bool) {
	e.mu.Lock()
	w := e.watches[run]
	e.mu.Unlock()
	if w == nil {
		return time.Time{}, false
	}
	nanos := w.lastChange.Load()
	if nanos == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, nanos), true
}

func (w *diffWatch) stop() {
	w.stopOnce.Do(func() { close(w.done) })
	<-w.finished
}

// underGit reports whether path is the .git directory or one of its children.
func (w *diffWatch) underGit(path string) bool {
	rel, err := filepath.Rel(filepath.Join(w.checkout, ".git"), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ignored reports whether path (absolute) falls outside the watched tree,
// belongs to Aether's own metadata, or is inside a git-ignored directory
// that has no tracked or otherwise unignored descendant. Git supplies the
// ignore state; the visible-path sets keep tracked files and negations
// reachable even when their parent directory itself is ignored.
func (w *diffWatch) ignored(path string) bool {
	rel, err := filepath.Rel(w.checkout, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	if rel == "." {
		return false
	}
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == ".git" || strings.HasPrefix(part, ".aether-") {
			return true
		}
	}
	return w.gitIgnoredPath(rel)
}

// gitIgnoredPath applies the current Git ignore snapshot to a
// checkout-relative path. A directory with a visible descendant is never
// pruned: a tracked file remains watchable even when its directory matches
// an ignore rule, and a path re-included by a negated rule appears in the
// visible set as an untracked file. The snapshot is refreshed after
// coalesced changes to ignore files, the index, or a new path below a
// pruned directory.
func (w *diffWatch) gitIgnoredPath(path string) bool {
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(w.checkout, path)
		if err != nil {
			return true
		}
		path = rel
	}
	if len(w.gitIgnoredDirs) == 0 {
		return false
	}
	rel := filepath.ToSlash(filepath.Clean(path))
	if rel == "." || rel == "" {
		return false
	}
	for cur := rel; cur != "." && cur != ""; {
		if _, ignored := w.gitIgnoredDirs[cur]; ignored {
			if _, visible := w.visibleFiles[rel]; visible {
				return false
			}
			if _, visible := w.visibleDirs[rel]; visible {
				return false
			}
			return true
		}
		slash := strings.LastIndexByte(cur, '/')
		if slash < 0 {
			break
		}
		cur = cur[:slash]
	}
	return false
}

// ignoreStateEvent identifies events whose contents can change Git's ignore
// answer. The index matters too: `git add -f` makes an existing ignored file
// visible without changing the working tree.
func (w *diffWatch) ignoreStateEvent(path string) bool {
	rel, err := filepath.Rel(w.checkout, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == ".git/index" || strings.HasPrefix(rel, ".git/index.") {
		return true
	}
	if rel == ".git/info" || strings.HasPrefix(rel, ".git/info/exclude") {
		return true
	}
	return filepath.Base(rel) == ".gitignore"
}

// requestIgnoreRefresh coalesces invalidation events into one Git query
// after the current quiet period. It deliberately does not reset an already
// pending deadline: a burst of generated-file events stays one refresh.
func (w *diffWatch) requestIgnoreRefresh(now time.Time) {
	if w.ignoreRefreshPending {
		return
	}
	w.ignoreRefreshPending = true
	w.ignoreRefreshAt = now.Add(w.e.cfg.QuietPeriod)
}

// reconcileIgnoreState refreshes Git's prune decision and then reconciles the
// actual fsnotify set against one walk using the new state. On query failure,
// stale prune state is cleared before restoring a full walk: missing events
// are safer than silently leaving a newly visible subtree unwatched.
func (w *diffWatch) reconcileIgnoreState(ctx context.Context) {
	if err := w.loadIgnoreState(ctx); err != nil {
		w.gitIgnoredDirs = nil
		w.visibleFiles = nil
		w.visibleDirs = nil
		slog.Warn("gitengine: cannot refresh git ignore state; watching all directories",
			"run", string(w.run), "error", err)
	} else {
		slog.Debug("gitengine: refreshed git ignore state", "run", string(w.run))
	}
	if err := w.reconcileWatches(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("gitengine: diff watch cannot reconcile subtree; its changes may not produce snapshots",
			"run", string(w.run), "error", err)
	}
}

// loadIgnoreState asks git for both sides of the prune decision. The
// ignored-directory listing lets the walk skip a generated tree without
// visiting every child; the visible listing keeps all tracked files and
// files re-included by negation reachable below an ignored parent.
func (w *diffWatch) loadIgnoreState(ctx context.Context) error {
	visible, err := w.e.git(ctx, w.checkout,
		"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--")
	if err != nil {
		return fmt.Errorf("gitengine: list visible paths: %w", err)
	}
	ignored, err := w.e.git(ctx, w.checkout,
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory", "--")
	if err != nil {
		return fmt.Errorf("gitengine: list ignored directories: %w", err)
	}

	visibleFiles := make(map[string]struct{})
	visibleDirs := make(map[string]struct{})
	for item := range strings.SplitSeq(visible, "\x00") {
		item = strings.TrimSuffix(item, "/")
		if item == "" {
			continue
		}
		item = filepath.ToSlash(filepath.Clean(item))
		visibleFiles[item] = struct{}{}
		for dir := item; ; {
			slash := strings.LastIndexByte(dir, '/')
			if slash < 0 {
				break
			}
			dir = dir[:slash]
			if dir == "" {
				break
			}
			visibleDirs[dir] = struct{}{}
		}
	}

	ignoredDirs := make(map[string]struct{})
	for item := range strings.SplitSeq(ignored, "\x00") {
		if !strings.HasSuffix(item, "/") {
			continue
		}
		item = strings.TrimSuffix(item, "/")
		if item == "" {
			continue
		}
		item = filepath.ToSlash(filepath.Clean(item))
		ignoredDirs[item] = struct{}{}
	}
	w.visibleFiles = visibleFiles
	w.visibleDirs = visibleDirs
	w.gitIgnoredDirs = ignoredDirs
	return nil
}

// addWatch registers one directory exactly once and records successful
// registrations so reconciliation can remove obsolete descendants.
func (w *diffWatch) addWatch(path string, metadata bool) error {
	if _, ok := w.watchedDirs[path]; ok {
		if metadata {
			w.metadataDirs[path] = struct{}{}
		}
		return nil
	}
	if err := w.watcher.Add(path); err != nil {
		return err
	}
	w.watchedDirs[path] = struct{}{}
	if metadata {
		w.metadataDirs[path] = struct{}{}
	}
	return nil
}

func (w *diffWatch) removeWatch(path string) {
	if err := w.watcher.Remove(path); err != nil &&
		!errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
		slog.Warn("gitengine: diff watch cannot remove directory watch",
			"run", string(w.run), "dir", path, "error", err)
	}
	delete(w.watchedDirs, path)
}

// reachableDirs returns the directories that should be watched for the
// current ignore snapshot. Ignored directories remain sentinels, while
// visible and tracked/reincluded ancestry is traversed normally.
func (w *diffWatch) reachableDirs() (map[string]struct{}, error) {
	reachable := make(map[string]struct{}, len(w.watchedDirs))
	for path := range w.metadataDirs {
		reachable[path] = struct{}{}
	}
	err := filepath.WalkDir(w.checkout, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == w.checkout {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		reachable[path] = struct{}{}
		if path != w.checkout && w.ignored(path) {
			return filepath.SkipDir
		}
		return nil
	})
	return reachable, err
}

// reconcileWatches removes directories no longer reachable under the current
// Git ignore state before adding newly reachable ones. Metadata watches are
// included in the desired set so .git/info and branch refs survive pruning.
func (w *diffWatch) reconcileWatches() error {
	reachable, err := w.reachableDirs()
	if err != nil {
		return err
	}
	for path := range w.watchedDirs {
		if _, keep := reachable[path]; keep {
			continue
		}
		w.removeWatch(path)
	}
	for path := range reachable {
		if _, watched := w.watchedDirs[path]; watched {
			continue
		}
		if err := w.addWatch(path, false); err != nil {
			if path == w.checkout {
				return fmt.Errorf("gitengine: watch %s: %w", path, err)
			}
			if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
				slog.Warn("gitengine: diff watch cannot observe subtree; its changes will not produce snapshots",
					"run", string(w.run), "dir", path, "error", err)
			}
		}
	}
	return nil
}

// addRecursive watches root and every directory below it, skipping ignored
// subtrees. Directories vanishing mid-walk are tolerated. A failed watch on
// a subdirectory (typically inotify watch exhaustion) degrades that subtree
// to blindness, so it is logged loudly rather than swallowed.
func (w *diffWatch) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && w.ignored(path) {
			// Keep a watch on the ignored directory itself. A future Create or
			// Rename may be re-included by a negated rule; watching only the
			// root gives reconciliation a chance to discover it without
			// paying for every ignored descendant.
			if err := w.addWatch(path, false); err != nil &&
				!errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
				slog.Warn("gitengine: diff watch cannot observe ignored directory",
					"run", string(w.run), "dir", path, "error", err)
			}
			return filepath.SkipDir
		}
		if err := w.addWatch(path, false); err != nil {
			if path == root {
				return fmt.Errorf("gitengine: watch %s: %w", path, err)
			}
			if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
				slog.Warn("gitengine: diff watch cannot observe subtree; its changes will not produce snapshots",
					"run", string(w.run), "dir", path, "error", err)
			}
		}
		return nil
	})
}

func (w *diffWatch) loop() {
	defer close(w.finished)
	defer func() { _ = w.watcher.Close() }()

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		select {
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			now := time.Now()
			if ev.Op.Has(fsnotify.Remove) || ev.Op.Has(fsnotify.Rename) {
				// Descendant watches follow their old inodes across a rename.
				// Remove them before the same paths can name replacement dirs.
				prefix := ev.Name + string(filepath.Separator)
				for path := range w.watchedDirs {
					if path == ev.Name || strings.HasPrefix(path, prefix) {
						w.removeWatch(path)
						w.requestIgnoreRefresh(now)
					}
				}
			}
			if w.underGit(ev.Name) {
				w.headDirty = true
				w.headEvent = now
				w.headRetryAt = time.Time{}
				if w.ignoreStateEvent(ev.Name) {
					w.requestIgnoreRefresh(now)
				}
				w.arm(timer, now)
				continue
			}
			newDir := false
			if ev.Op.Has(fsnotify.Create) || ev.Op.Has(fsnotify.Rename) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					newDir = true
				}
			}
			if w.ignoreStateEvent(ev.Name) {
				w.requestIgnoreRefresh(now)
			}
			// A new directory was absent from the previous Git listing, so
			// its old visibility is unknowable. Add only its root sentinel,
			// refresh Git's state, and recurse after classification; otherwise
			// a newly-created ignored tree would be watched in full forever.
			if newDir {
				w.requestIgnoreRefresh(now)
				if err := w.addWatch(ev.Name, false); err != nil &&
					!errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
					slog.Warn("gitengine: diff watch cannot observe new directory",
						"run", string(w.run), "dir", ev.Name, "error", err)
				}
				w.arm(timer, now)
				continue
			}
			// An ignored directory is watched as a sentinel. A Create or
			// Rename below it may be made visible by a negated rule, so
			// reconcile once for the burst rather than querying Git here.
			if ev.Op.Has(fsnotify.Create) || ev.Op.Has(fsnotify.Rename) {
				if w.ignored(ev.Name) {
					w.requestIgnoreRefresh(now)
				}
			}
			if w.ignored(ev.Name) {
				if w.ignoreRefreshPending {
					w.arm(timer, now)
				}
				continue
			}
			w.lastChange.Store(now.UnixNano())
			w.lastEvent = now
			w.dirty = true
			w.arm(timer, now)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("gitengine: diff watcher error; refreshing checkout",
				"run", string(w.run), "error", err)
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				for path := range w.watchedDirs {
					w.removeWatch(path)
				}
			}
			now := time.Now()
			w.requestIgnoreRefresh(now)
			w.arm(timer, now)
		case <-timer.C:
			now := time.Now()
			if w.ignoreRefreshPending && !w.ignoreRefreshAt.After(now) {
				w.ignoreRefreshPending = false
				w.ignoreRefreshAt = time.Time{}
				ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
				w.reconcileIgnoreState(ctx)
				cancel()
				// The ignore/index transition can reveal a path whose
				// directory was previously pruned. Treat the reconciliation
				// as a change; tree equality still suppresses empty events.
				w.lastChange.Store(now.UnixNano())
				w.lastEvent = now
				w.dirty = true
			}
			if w.headDirty && !now.Before(w.headEvent.Add(w.e.cfg.QuietPeriod)) && !now.Before(w.headRetryAt) {
				ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
				if w.checkHead(ctx) {
					w.headDirty = false
					w.headRetryAt = time.Time{}
				} else {
					retry := w.e.cfg.QuietPeriod
					if retry < w.e.cfg.MinInterval {
						retry = w.e.cfg.MinInterval
					}
					w.headRetryAt = time.Now().Add(retry)
				}
				cancel()
			}
			if w.dirty {
				quiet := now.Sub(w.lastEvent) >= w.e.cfg.QuietPeriod
				rested := now.Sub(w.lastSnap) >= w.e.cfg.MinInterval
				overdue := now.Sub(w.lastSnap) >= w.e.cfg.MaxInterval
				if (quiet && rested) || overdue {
					w.dirty = false
					w.snapshot()
					w.lastSnap = time.Now()
				}
			}
			w.arm(timer, time.Now())
		case <-w.done:
			return
		}
	}
}

// arm resets the timer to the earliest instant a snapshot could fire:
// max(lastEvent+QuietPeriod, lastSnap+MinInterval), capped at
// lastSnap+MaxInterval (the sustained-churn bound). A HEAD-only event is
// gated by headEvent and does not affect the tree-change deadline.
func (w *diffWatch) arm(timer *time.Timer, now time.Time) {
	var deadline time.Time
	if w.dirty {
		deadline = w.lastEvent.Add(w.e.cfg.QuietPeriod)
		if floor := w.lastSnap.Add(w.e.cfg.MinInterval); deadline.Before(floor) {
			deadline = floor
		}
		if churn := w.lastSnap.Add(w.e.cfg.MaxInterval); deadline.After(churn) {
			deadline = churn
		}
	}
	if w.headDirty {
		headDeadline := w.headEvent.Add(w.e.cfg.QuietPeriod)
		if w.headRetryAt.After(headDeadline) {
			headDeadline = w.headRetryAt
		}
		if deadline.IsZero() || headDeadline.Before(deadline) {
			deadline = headDeadline
		}
	}
	if w.ignoreRefreshPending && (deadline.IsZero() || w.ignoreRefreshAt.Before(deadline)) {
		deadline = w.ignoreRefreshAt
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	if !deadline.IsZero() {
		timer.Reset(max(0, deadline.Sub(now)))
	}
}

// snapshot records the checkout's content as a git tree, captures its diff
// stats against the recorded base, and publishes run.diff when either moved
// since the last snapshot.
func (w *diffWatch) snapshot() {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()

	files, err := w.e.diffStats(ctx, w.checkout, w.base)
	if err != nil {
		w.warnSnapshot(err)
		return
	}
	tree, treeErr := w.e.writeSnapshotTree(ctx, w.run, w.checkout)
	if treeErr != nil {
		w.warnTree(treeErr)
	}
	// The tree decides, with the stat set as the fallback for a store that
	// cannot be written. The tree is the stricter gate - an edit that keeps
	// the line counts identical moves the tree but not the stats - and it is
	// also the only one that holds across a restart, where lastTree is
	// restored from the store but lastFiles starts empty: ORing the two
	// would publish an interval whose ends are the same tree.
	changed := tree != w.lastTree
	if treeErr != nil {
		changed = !slices.Equal(files, w.lastFiles)
	}
	if changed {
		w.lastFiles = files
		payload := events.RunDiffPayload{Files: files}
		if treeErr == nil {
			payload.Tree = tree
			payload.ParentTree = w.lastTree
		}
		if w.e.cfg.Bus != nil {
			// The registry entry outlives the watch, so the workspace scope
			// is read from it rather than duplicated onto the watch.
			w.e.mu.Lock()
			workspace := w.e.registry[w.run].workspace
			w.e.mu.Unlock()
			_, _ = w.e.cfg.Bus.Publish(ctx, events.Event{
				WorkspaceID: workspace,
				RunID:       w.run,
				Payload:     payload,
			})
		}
		if treeErr == nil {
			w.lastTree = tree
			if err := w.e.setLastSnapshotTree(w.run, tree); err != nil {
				slog.Warn("gitengine: snapshot tree not recorded; a restart will diff from the fork point",
					"run", string(w.run), "error", err)
			}
		}
	}
	w.checkHead(ctx)
}

// checkHead publishes a moved checkout HEAD. It returns true when checking
// HEAD succeeded, including when it did not move.
func (w *diffWatch) checkHead(ctx context.Context) bool {
	head, err := w.e.git(ctx, w.checkout, "rev-parse", "HEAD")
	if err != nil {
		w.warnSnapshot(err)
		return false
	}
	if head == w.lastHead {
		return true
	}
	if _, err := w.e.PublishRunBranch(ctx, w.run); err != nil {
		w.warnPublish(err)
		return false
	}
	w.lastHead = head
	return true
}

func (w *diffWatch) warnSnapshot(err error) {
	now := time.Now()
	if !w.lastSnapshotWarn.IsZero() && now.Sub(w.lastSnapshotWarn) < diffWarnInterval {
		return
	}
	w.lastSnapshotWarn = now
	slog.Warn("gitengine: diff snapshot failed", "run", string(w.run), "error", err)
}

func (w *diffWatch) warnPublish(err error) {
	now := time.Now()
	if !w.lastPublishWarn.IsZero() && now.Sub(w.lastPublishWarn) < diffWarnInterval {
		return
	}
	w.lastPublishWarn = now
	slog.Warn("gitengine: publish run branch failed", "run", string(w.run), "error", err)
}

// warnTree reports a snapshot whose tree could not be written. The stat set
// still publishes, so the timeline survives; what is lost is the interval
// diff behind that row.
func (w *diffWatch) warnTree(err error) {
	now := time.Now()
	if !w.lastTreeWarn.IsZero() && now.Sub(w.lastTreeWarn) < diffWarnInterval {
		return
	}
	w.lastTreeWarn = now
	slog.Warn("gitengine: diff snapshot tree could not be written; this interval has no per-interval diff",
		"run", string(w.run), "error", err)
}

// diffStats builds the snapshot stat set: numstat against base for tracked
// work (committed and uncommitted) plus untracked files at their line
// counts. Both listings use -z (NUL-separated, unquoted) so non-ASCII
// paths survive verbatim, and --no-renames so a rename reports its real
// old and new paths rather than a munged "old => new". Never mutates the
// index. Sorted by path.
func (e *Engine) diffStats(ctx context.Context, checkout, base string) ([]events.FileDiffStat, error) {
	numstat, err := e.git(ctx, checkout, "diff", "--numstat", "--no-renames", "-z", base)
	if err != nil {
		return nil, err
	}
	files := make([]events.FileDiffStat, 0, 8)
	for record := range strings.SplitSeq(numstat, "\x00") {
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0]) // "-" (binary) parses as 0
		del, _ := strconv.Atoi(parts[1])
		files = append(files, events.FileDiffStat{Path: parts[2], Additions: add, Deletions: del})
	}
	untracked, err := e.git(ctx, checkout, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for path := range strings.SplitSeq(untracked, "\x00") {
		if path == "" {
			continue
		}
		lines, err := countLines(ctx, filepath.Join(checkout, path))
		if err != nil {
			continue
		}
		files = append(files, events.FileDiffStat{Path: path, Additions: lines})
	}
	slices.SortFunc(files, func(a, b events.FileDiffStat) int {
		return strings.Compare(a.Path, b.Path)
	})
	return files, nil
}

// countBytesCap bounds how much of a single untracked file a snapshot will
// read; beyond it the line count is truncated. Keeps multi-GB build
// artifacts from stalling the watch loop.
const countBytesCap = 8 << 20

// countLines counts the lines in an untracked path (a trailing partial line
// counts). A symlink counts as one line (git's view of its content) and is
// never followed - the target may be a FIFO or device that would block the
// watch loop forever. Only regular files are read, capped at countBytesCap.
func countLines(ctx context.Context, path string) (int, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return 1, nil
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("gitengine: not a regular file: %s", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("gitengine: not a regular file: %s", path)
	}
	var (
		buf    [32 * 1024]byte
		count  int
		total  int
		endsNL = true
		empty  = true
	)
	for total < countBytesCap {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := f.Read(buf[:])
		if n > 0 {
			empty = false
			total += n
			for _, b := range buf[:n] {
				if b == '\n' {
					count++
				}
			}
			endsNL = buf[n-1] == '\n'
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if !empty && !endsNL {
		count++
	}
	return count, nil
}
