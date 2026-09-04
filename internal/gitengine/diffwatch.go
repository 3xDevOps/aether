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
	lastSnapshotWarn time.Time
	lastPublishWarn  time.Time
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
		e:        e,
		run:      run,
		checkout: checkout,
		base:     meta.Base,
		watcher:  watcher,
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		// Nothing has been published yet, so the MinInterval floor must not
		// delay the first snapshot: only QuietPeriod gates it.
		lastSnap: time.Now().Add(-e.cfg.MinInterval),
		lastHead: head,
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
		refDir,
	} {
		if err := watcher.Add(dir); err != nil {
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

// ignored reports whether path (absolute) falls outside the watched tree or
// has an ignored component. .git paths are handled separately by underGit.
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
	return false
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
			return filepath.SkipDir
		}
		if err := w.watcher.Add(path); err != nil {
			if path == root {
				return fmt.Errorf("gitengine: watch %s: %w", path, err)
			}
			if !errors.Is(err, fs.ErrNotExist) {
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
			if w.underGit(ev.Name) {
				w.headDirty = true
				w.headEvent = now
				w.headRetryAt = time.Time{}
				w.arm(timer, now)
				continue
			}
			if w.ignored(ev.Name) {
				continue
			}
			w.lastChange.Store(now.UnixNano())
			w.lastEvent = now
			w.dirty = true
			if ev.Op.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if err := w.addRecursive(ev.Name); err != nil && !errors.Is(err, fs.ErrNotExist) {
						slog.Warn("gitengine: diff watch cannot observe new subtree; its changes will not produce snapshots",
							"run", string(w.run), "dir", ev.Name, "error", err)
					}
				}
			}
			w.arm(timer, now)
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		case <-timer.C:
			now := time.Now()
			if w.headDirty {
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
				if w.headDirty || w.dirty {
					w.arm(timer, time.Now())
				}
				continue
			}
			if !w.dirty {
				continue
			}
			quiet := now.Sub(w.lastEvent) >= w.e.cfg.QuietPeriod
			rested := now.Sub(w.lastSnap) >= w.e.cfg.MinInterval
			overdue := now.Sub(w.lastSnap) >= w.e.cfg.MaxInterval
			if (quiet && rested) || overdue {
				w.dirty = false
				w.snapshot()
				w.lastSnap = time.Now()
			} else {
				w.arm(timer, now)
			}
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
	deadline := w.lastEvent.Add(w.e.cfg.QuietPeriod)
	if w.headDirty {
		deadline = w.headEvent.Add(w.e.cfg.QuietPeriod)
		if w.headRetryAt.After(deadline) {
			deadline = w.headRetryAt
		}
	} else {
		if floor := w.lastSnap.Add(w.e.cfg.MinInterval); deadline.Before(floor) {
			deadline = floor
		}
		if churn := w.lastSnap.Add(w.e.cfg.MaxInterval); deadline.After(churn) {
			deadline = churn
		}
	}
	d := deadline.Sub(now)
	if d < 0 {
		d = 0
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

// snapshot captures the checkout's diff against its recorded base and
// publishes run.diff when the stat set changed since the last snapshot.
func (w *diffWatch) snapshot() {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()

	files, err := w.e.diffStats(ctx, w.checkout, w.base)
	if err != nil {
		w.warnSnapshot(err)
		return
	}
	if !slices.Equal(files, w.lastFiles) {
		w.lastFiles = files
		if w.e.cfg.Bus != nil {
			// The registry entry outlives the watch, so the workspace scope
			// is read from it rather than duplicated onto the watch.
			w.e.mu.Lock()
			workspace := w.e.registry[w.run].workspace
			w.e.mu.Unlock()
			_, _ = w.e.cfg.Bus.Publish(ctx, events.Event{
				WorkspaceID: workspace,
				RunID:       w.run,
				Payload:     events.RunDiffPayload{Files: files},
			})
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
