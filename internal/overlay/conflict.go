package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mutagen-io/mutagen/pkg/synchronization"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core"
)

// ConflictSuffix is appended to a conflicted path to name its preserved
// losing-side twin. It always sits last in the name, so the single
// "*"+ConflictSuffix sync ignore covers every twin, numbered ones
// included.
const ConflictSuffix = ".aether-conflict"

// maxConflictTwins bounds the numbered twins kept for one path. Hitting
// it means nobody is resolving conflicts; erroring out beats spinning.
const maxConflictTwins = 1000

// ErrConflict is the sentinel wrapped by the *Conflict error Run returns
// when synchronization paused on a conflict.
var ErrConflict = errors.New("overlay: sync conflict")

// ErrHalted is returned by Run when mutagen halts the session on a
// safety check (root emptied, deleted, or type-changed).
var ErrHalted = errors.New("overlay: session halted by safety check")

// Conflict describes a paused overlay: the conflicted paths (relative to
// the sync roots, slash-separated) and the mutagen session that was
// paused. The run worktree side wins; the local edits are preserved as
// <path>.aether-conflict twins next to the originals.
type Conflict struct {
	SessionID string
	Files     []string
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("sync conflict on %s: local versions preserved as *%s, sync paused; resolve and rerun `aether sync` to resume",
		strings.Join(c.Files, ", "), ConflictSuffix)
}

func (c *Conflict) Unwrap() error { return ErrConflict }

// Run drives the overlay until the context is canceled (clean shutdown,
// returns nil), a conflict pauses the session (returns *Conflict after
// preserving twins), or mutagen halts on a safety check (returns
// ErrHalted). Transient connection errors are mutagen's to retry; they
// never end the loop.
func (s *Session) Run(ctx context.Context) error {
	for {
		index, states, err := s.manager.List(ctx, s.selection(), s.stateIndex)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("overlay: session state: %w", err)
		}
		s.stateIndex = index
		if len(states) == 0 {
			return errors.New("overlay: session disappeared")
		}
		state := states[0]
		switch state.Status {
		case synchronization.Status_HaltedOnRootEmptied,
			synchronization.Status_HaltedOnRootDeletion,
			synchronization.Status_HaltedOnRootTypeChange:
			return fmt.Errorf("%w: %s", ErrHalted, state.Status.Description())
		}
		if len(state.Conflicts) > 0 {
			return s.pauseOnConflict(ctx, state.Conflicts)
		}
	}
}

// Flush forces one synchronization cycle and waits for it to complete.
// It is how tests (and the CLI, before teardown) make "edit propagated"
// deterministic instead of racing the watcher.
func (s *Session) Flush(ctx context.Context) error {
	return s.manager.Flush(ctx, s.selection(), "", false)
}

// Pause halts synchronization; Resume restarts it. Both are exposed for
// tests that need a propagation barrier; the CLI never resumes a paused
// session in-process (resume = rerun the command).
func (s *Session) Pause(ctx context.Context) error {
	return s.manager.Pause(ctx, s.selection(), "")
}

// Resume restarts a paused session.
func (s *Session) Resume(ctx context.Context) error {
	return s.manager.Resume(ctx, s.selection(), "")
}

// Paused reports whether the mutagen session is currently paused.
func (s *Session) Paused(ctx context.Context) bool {
	_, states, err := s.manager.List(ctx, s.selection(), 0)
	if err != nil || len(states) == 0 {
		return false
	}
	return states[0].Session.Paused
}

// pauseOnConflict is the conflict policy: pause the session first (no
// silent continue, and no propagation races while twins are written),
// then preserve the losing side and report.
//
// The run worktree wins and the local edit loses, because the worktree
// is the shared canon (the agent's work, visible to every member) while
// the local overlay is one member's guest copy - and because the local
// side is the only one the client can mutate without pushing bytes
// through the very sync engine that just paused. Each conflicted local
// file is copied to <path>.aether-conflict (or the next free
// <path>.N.aether-conflict when earlier twins are still around) and the
// original is removed, so a rerun sees one-sided content and converges
// on the worktree version while the local edit survives in the twin.
func (s *Session) pauseOnConflict(ctx context.Context, conflicts []*core.Conflict) error {
	if err := s.manager.Pause(ctx, s.selection(), ""); err != nil {
		return fmt.Errorf("overlay: pause on conflict: %w", err)
	}
	files := conflictPaths(conflicts)
	for _, rel := range files {
		if err := preserveLocal(s.localDir, rel); err != nil {
			return fmt.Errorf("overlay: preserve %s: %w", rel, err)
		}
	}
	return &Conflict{SessionID: s.sessionID, Files: files}
}

// conflictPaths flattens conflicts to a sorted, deduplicated list of
// relative paths. A change path names the conflicted entry; an empty
// path (whole-root conflict) falls back to the conflict root.
func conflictPaths(conflicts []*core.Conflict) []string {
	seen := make(map[string]struct{})
	for _, c := range conflicts {
		for _, change := range append(c.AlphaChanges, c.BetaChanges...) {
			p := change.Path
			if p == "" {
				p = c.Root
			}
			if p == "" {
				continue
			}
			seen[p] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for p := range seen {
		files = append(files, p)
	}
	sort.Strings(files)
	return files
}

// preserveLocal copies the local file at rel to a fresh conflict twin
// and removes the original. Existing twins are never overwritten, so a
// second conflict on the same path cannot destroy the version the first
// one preserved. Only regular files are moved aside; a missing local
// entry (the conflict was a local deletion) or a directory is left as
// is - the twin would be meaningless and removing a whole tree is not a
// call the overlay makes on its own.
func preserveLocal(root, rel string) error {
	src := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := copyToFreshTwin(src, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Remove(src)
}

// twinName returns the n-th twin name for src: n == 1 is the plain
// <path>.aether-conflict, later ones interpose the count as
// <path>.2.aether-conflict. The suffix stays last so the single
// "*"+ConflictSuffix ignore keeps every twin out of the overlay.
func twinName(src string, n int) string {
	if n == 1 {
		return src + ConflictSuffix
	}
	return fmt.Sprintf("%s.%d%s", src, n, ConflictSuffix)
}

// copyToFreshTwin copies src to the lowest-numbered twin name that is
// not taken yet. The copy creates its destination exclusively, so a twin
// written by an earlier conflict (or a racing session) is skipped rather
// than truncated.
func copyToFreshTwin(src string, perm os.FileMode) error {
	for n := 1; n <= maxConflictTwins; n++ {
		err := copyFile(src, twinName(src, n), perm)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return err
	}
	return fmt.Errorf("%d conflict twins already preserved, resolve them first", maxConflictTwins)
}

// copyFile writes src to a newly created dst. It fails with os.ErrExist
// if dst is already there and leaves no partial file behind on error.
func copyFile(src, dst string, perm os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	defer func() { _ = in.Close() }()
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
