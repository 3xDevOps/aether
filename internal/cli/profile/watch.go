package profile

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const defaultDebounce = time.Second

// Conn is a control-channel client plus a closer that signals disconnect.
type Conn interface {
	Client() *protocol.Client
	Close() error
	Done() <-chan struct{}
}

// Watcher watches harness LocalRoot directories and pushes after a debounce.
// Disabled (--no-profile-sync) performs no discovery, watching, reconnect
// catch-up, or automatic upload. Manual Push still works independently.
type Watcher struct {
	Disabled bool
	Debounce time.Duration
	// Dial opens a control connection. Tests inject a fake.
	Dial func(ctx context.Context) (Conn, error)
	// Roots maps harness name -> absolute LocalRoot. Empty means discover
	// existing shipped-harness roots at start.
	Roots map[string]string
	// PushOne uploads one harness. Tests inject a counter; production
	// uses Discover+Push via the live client.
	PushOne func(ctx context.Context, c *protocol.Client, harness string) error

	mu      sync.Mutex
	pushes  int
	catchup int
	lastErr error
}

// CatchUp pushes every existing harness root once. No-op when Disabled.
func (w *Watcher) CatchUp(ctx context.Context, c *protocol.Client) error {
	if w.Disabled {
		return nil
	}
	w.mu.Lock()
	w.catchup++
	w.mu.Unlock()
	roots := w.roots()
	push := w.pushFn()
	for name := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := push(ctx, c, name); err != nil {
			w.note(err)
			slog.Warn("profile sync: catch-up push failed", "harness", name, "error", err)
			continue
		}
		w.note(nil)
	}
	return nil
}

// Run watches until ctx is done. When Disabled it returns immediately.
// Each new session catch-up-pushes once (start and SSH reconnect).
func (w *Watcher) Run(ctx context.Context) error {
	if w.Disabled {
		return nil
	}
	if w.Debounce <= 0 {
		w.Debounce = defaultDebounce
	}
	for {
		err := w.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			slog.Warn("profile sync: session ended", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (w *Watcher) session(ctx context.Context) error {
	var client *protocol.Client
	var done <-chan struct{}
	if w.Dial != nil {
		conn, err := w.Dial(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		client = conn.Client()
		done = conn.Done()
	}
	if err := w.CatchUp(ctx, client); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("profile sync: catch-up failed", "error", err)
	}
	return w.watch(ctx, client, done)
}

func (w *Watcher) watch(ctx context.Context, client *protocol.Client, done <-chan struct{}) error {
	roots := w.roots()
	if len(roots) == 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("profile sync: connection lost")
		}
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = fw.Close() }()
	harnessOf := map[string]string{}
	for name, root := range roots {
		if err := addWatchRecursive(fw, root); err != nil {
			slog.Warn("profile sync: watch root", "harness", name, "error", err)
			continue
		}
		harnessOf[root] = name
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				harnessOf[p] = name
			}
			return nil
		})
	}
	pending := map[string]struct{}{}
	timer := time.NewTimer(w.Debounce)
	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	stopTimer()
	flush := func() {
		if len(pending) == 0 {
			return
		}
		names := make([]string, 0, len(pending))
		for n := range pending {
			names = append(names, n)
		}
		pending = map[string]struct{}{}
		push := w.pushFn()
		for _, name := range names {
			if err := push(ctx, client, name); err != nil {
				w.note(err)
				slog.Warn("profile sync: push failed", "harness", name, "error", err)
				continue
			}
			w.note(nil)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("profile sync: connection lost")
		case ev, ok := <-fw.Events:
			if !ok {
				return errors.New("profile watcher: events closed")
			}
			name := harnessFor(harnessOf, ev.Name)
			if name == "" {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = addWatchRecursive(fw, ev.Name)
					harnessOf[ev.Name] = name
				}
			}
			pending[name] = struct{}{}
			stopTimer()
			timer.Reset(w.Debounce)
		case err, ok := <-fw.Errors:
			if !ok {
				return errors.New("profile watcher: errors closed")
			}
			slog.Warn("profile sync: watch error", "error", err)
		case <-timer.C:
			flush()
		}
	}
}

func (w *Watcher) roots() map[string]string {
	if len(w.Roots) > 0 {
		return w.Roots
	}
	return ExistingRoots()
}

func (w *Watcher) pushFn() func(context.Context, *protocol.Client, string) error {
	if w.PushOne != nil {
		return w.PushOne
	}
	return defaultPushOne
}

func (w *Watcher) note(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		w.pushes++
	}
	w.lastErr = err
}

// PushCount is the number of successful automatic uploads (tests).
func (w *Watcher) PushCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pushes
}

// CatchUpCount is how many times CatchUp ran (tests).
func (w *Watcher) CatchUpCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.catchup
}

func defaultPushOne(ctx context.Context, c *protocol.Client, harnessName string) error {
	if c == nil {
		return errors.New("profile sync: no control client")
	}
	files, skipped, err := DiscoverFiles(ctx, harnessName, nil)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// The daemon pushes unattended, so a file the size caps left behind
	// would otherwise be absent from the server with nobody told. This
	// log is the daemon's equivalent of the line `aether profile push`
	// prints and the `skipped` list the dashboard shows.
	for _, s := range skipped {
		slog.Info("profile sync: file not pushed", "harness", harnessName, "path", s.Path, "reason", s.Reason, "detail", s.Detail)
	}
	_, err = Push(c, harnessName, files, nil, "")
	return err
}

// ExistingRoots returns harness name -> LocalRoot for shipped profiles
// whose local directory currently exists.
func ExistingRoots() map[string]string {
	home, err := userHome()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range harness.Profiles() {
		if p.LocalRoot == "" {
			continue
		}
		dir := filepath.Join(home, filepath.FromSlash(p.LocalRoot))
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			out[p.Name] = dir
		}
	}
	return out
}

func addWatchRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return w.Add(p)
		}
		return nil
	})
}

func harnessFor(harnessOf map[string]string, name string) string {
	dir := name
	for dir != "" && dir != string(filepath.Separator) && dir != "." {
		if h, ok := harnessOf[dir]; ok {
			return h
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
