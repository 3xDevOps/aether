package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const snapshotPublishedRef = "refs/aether/published"
const snapshotHistoryRoot = "refs/aether/history/"
const snapshotInflightParentRef = "refs/aether/inflight/parent"
const snapshotInflightTreeRef = "refs/aether/inflight/tree"
const snapshotInflightUpstreamRef = "refs/aether/inflight/upstream"

func (e *Engine) initSnapshotStore(ctx context.Context, run domain.RunID, checkout, store string) error {
	if err := e.ensureScratchGitDir(ctx, store, checkout); err != nil {
		return err
	}
	if _, err := e.scratchObjects(store, checkout, run); err != nil {
		return err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return err
	}
	base, err := e.gitCheckout(ctx, run, checkout, "rev-parse", meta.Base+"^{tree}")
	if err != nil {
		return err
	}
	if _, _, err = e.gitBareBounded(ctx, store, 0, "update-ref", "refs/aether/base", strings.TrimSpace(base)); err != nil {
		return err
	}
	// Legacy stores had only a last-tip file. Import that boundary without
	// pruning uncatalogued historical trees.
	data, err := os.ReadFile(filepath.Join(store, lastTreeFile))
	if err == nil {
		tree := strings.TrimSpace(string(data))
		if validObjectID(tree) {
			if _, _, err = e.gitBareBounded(ctx, store, 0, "update-ref", snapshotPublishedRef, tree); err != nil {
				return err
			}
		}
		return os.Remove(filepath.Join(store, lastTreeFile))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Older stores did not catalog every captured root, and a tree reused from
// checkout history may not even have a local object. Recover exactly the
// recorded endpoints from the existing event log, in bounded pages.
func (e *Engine) retainRecordedSnapshotTrees(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, store string) error {
	if e.cfg.EventLog == nil {
		return nil
	}
	upto, err := e.cfg.EventLog.LastSeq(ctx)
	if err != nil {
		return err
	}
	filter := events.Filter{Workspace: workspace, Run: run, Types: []events.Type{events.TypeRunDiff}}
	for after := uint64(0); after < upto; {
		page, pageErr := e.cfg.EventLog.Read(ctx, filter, after, upto, 256)
		if pageErr != nil {
			return pageErr
		}
		if len(page) == 0 {
			break
		}
		ids := make(map[string]struct{})
		for _, event := range page {
			payload, ok := event.Payload.(events.RunDiffPayload)
			if !ok {
				return fmt.Errorf("gitengine: invalid recorded diff payload for session %s", run)
			}
			for _, id := range []string{payload.ParentTree, payload.Tree} {
				if validObjectID(id) {
					ids[id] = struct{}{}
				}
			}
		}
		after = page[len(page)-1].Seq
		if len(ids) == 0 {
			continue
		}
		var input strings.Builder
		for id := range ids {
			input.WriteString(id + "\n")
		}
		listing, listErr := e.gitInput(ctx, store, e.gitEnv(), []byte(input.String()), "cat-file", "--batch-check=%(objectname) %(objecttype)")
		if listErr != nil {
			return listErr
		}
		var refs strings.Builder
		for line := range strings.SplitSeq(listing, "\n") {
			fields := strings.Fields(line)
			// Already-pruned trees remain unavailable; never fabricate a
			// replacement interval or prevent retaining the surviving ones.
			if len(fields) == 2 && fields[1] == "tree" {
				refs.WriteString("update " + snapshotHistoryRoot + fields[0] + " " + fields[0] + "\n")
			}
		}
		if refs.Len() > 0 {
			transaction := "start\n" + refs.String() + "prepare\ncommit\n"
			if _, updateErr := e.gitInput(ctx, store, e.gitEnv(), []byte(transaction), "update-ref", "--stdin"); updateErr != nil {
				return updateErr
			}
		}
	}
	return nil
}

// retainSnapshot requires snapshotLock through staging and any object copy.
// Content-addressed refs retain every captured tree without duplicate refs for
// unchanged captures or reverts. Older timestamp-named refs remain valid.
func (e *Engine) retainSnapshot(ctx context.Context, store, tree string) error {
	transaction := "start\nupdate refs/aether/latest " + tree +
		"\nupdate " + snapshotHistoryRoot + tree + " " + tree + "\nprepare\ncommit\n"
	_, err := e.gitInput(ctx, store, e.gitEnv(), []byte(transaction), "update-ref", "--stdin")
	return err
}

// detachSnapshotStore moves the store off the checkout's objects before the
// checkout goes. The caller stops the watcher and holds repoMaintenanceMu
// before snapshotLock, matching evidence capture's lock order.
func (e *Engine) detachSnapshotStore(ctx context.Context, run domain.RunID, checkout string) error {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(store, "HEAD")); errors.Is(statErr, os.ErrNotExist) {
		return nil
	} else if statErr != nil {
		return statErr
	}
	if _, statErr := os.Stat(checkout); errors.Is(statErr, os.ErrNotExist) {
		return nil
	} else if statErr != nil {
		return statErr
	}
	if initErr := e.initSnapshotStore(ctx, run, checkout, store); initErr != nil {
		return initErr
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return err
	}
	if retainErr := e.retainRecordedSnapshotTrees(ctx, meta.Workspace, run, store); retainErr != nil {
		return retainErr
	}
	repo, err := e.repoPath(meta.Workspace)
	if err != nil {
		return err
	}
	return e.shareHistory(ctx, run, store, repo)
}

// historyPinRoot holds, in a workspace repository, one ref per snapshot store
// that borrows its objects, naming the fork-point tree the store relies on.
// git gc keeps what refs reach, so the tree outlives every branch that held it.
const historyPinRoot = "refs/aether/sessions/"

// shareHistory repacks the store of key to exactly what keep and its
// refs/aether refs reach beyond the fork-point tree, which it then borrows
// from repo under a pin. With repo empty, or a repo that lacks part of the
// tree, the store keeps all of it and borrows nothing. Older packs, and loose
// objects a pack holds, go, so a caller that can gives every tree the event
// log recorded a ref first. The caller holds repoMaintenanceMu, so no
// evidence prune that started before the pin can drop the tree, and then the
// snapshot lock of key.
func (e *Engine) shareHistory(ctx context.Context, key domain.RunID, store, repo string, keep ...string) (err error) {
	base, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "refs/aether/base")
	if err != nil {
		return err
	}
	base = strings.TrimSpace(base)
	if repo != "" && !e.holdsTree(ctx, repo, base) {
		repo = ""
	}
	roots, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/")
	if err != nil {
		return err
	}
	for _, id := range keep {
		roots += id + "\n"
	}
	if repo != "" {
		pin := historyPinRoot + string(key)
		if _, err = e.git(ctx, repo, "update-ref", pin, base); err != nil {
			return err
		}
		defer func() {
			if borrowed, _ := e.borrowedRepo(store); err != nil && borrowed != repo {
				_, unpinErr := e.git(context.WithoutCancel(ctx), repo, "update-ref", "-d", pin)
				err = errors.Join(err, unpinErr)
			}
		}()
		roots += "^" + base + "\n"
	}
	packs := filepath.Join(store, "objects", "pack")
	old, err := os.ReadDir(packs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// One thread: this runs in the background, on hosts short of memory too.
	name, err := e.gitInput(ctx, store, e.gitEnv(), []byte(roots), "pack-objects", "--revs", "--non-empty", "--threads=1", filepath.Join(packs, "pack"))
	if err != nil {
		return err
	}
	if err = e.switchAlternates(ctx, store, repo, key); err != nil {
		return err
	}
	// The new pack is in place before the old ones go, as in git repack -d.
	for _, entry := range old {
		if name == "" || !strings.HasPrefix(entry.Name(), "pack-"+name+".") {
			if err = os.Remove(filepath.Join(packs, entry.Name())); err != nil {
				return err
			}
		}
	}
	_, _, err = e.gitBareBounded(ctx, store, 0, "prune-packed")
	return err
}

// holdsTree reports whether repo has every object of tree.
func (e *Engine) holdsTree(ctx context.Context, repo, tree string) bool {
	_, _, err := e.gitBareBounded(ctx, repo, 0, "rev-list", "--objects", "--quiet", tree)
	return err == nil
}

// switchAlternates makes the store borrow repo's objects, or nothing when
// repo is "". The path is relative, so a moved data directory keeps it, and
// runs between real paths because git resolves it from the store's real
// objects directory. One that git does not follow leaves the store as it was.
func (e *Engine) switchAlternates(ctx context.Context, store, repo string, key domain.RunID) error {
	e.alternatesMu.Lock()
	defer e.alternatesMu.Unlock()
	alternates := filepath.Join(store, "objects", "info", "alternates")
	if repo == "" {
		if err := os.Remove(alternates); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	from, err := filepath.EvalSymlinks(filepath.Join(store, "objects"))
	if err != nil {
		return err
	}
	// Only the repositories directory is resolved, so a store still names a
	// repository that is itself a symlink by its place in it.
	repos, err := filepath.EvalSymlinks(filepath.Dir(repo))
	if err != nil {
		return err
	}
	to := filepath.Join(repos, filepath.Base(repo), "objects")
	rel, err := filepath.Rel(from, to)
	if err != nil {
		return err
	}
	previous, err := os.ReadFile(alternates)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err = borrowObjects(store, rel, key); err != nil {
		return err
	}
	reads, err := e.readsAlternate(ctx, store, to)
	if reads {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("git does not read %s through %s", to, rel)
	}
	var restoreErr error
	if previous == nil {
		restoreErr = os.Remove(alternates)
	} else {
		_, restoreErr = borrowObjects(store, strings.TrimSpace(string(previous)), key)
	}
	return errors.Join(fmt.Errorf("gitengine: the history of session %s cannot borrow from %s: %w", key, repo, err), restoreErr)
}

// readsAlternate reports whether git, in store, reads the objects directory
// objects as an alternate.
func (e *Engine) readsAlternate(ctx context.Context, store, objects string) (bool, error) {
	want, err := os.Stat(objects)
	if err != nil {
		return false, err
	}
	out, _, err := e.gitBareBounded(ctx, store, -1, "count-objects", "-v")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		path, ok := strings.CutPrefix(line, "alternate: ")
		if !ok {
			continue
		}
		if strings.HasPrefix(path, `"`) {
			if path, err = strconv.Unquote(path); err != nil {
				return false, fmt.Errorf("gitengine: alternate %s: %w", line, err)
			}
		}
		if got, statErr := os.Stat(path); statErr == nil && os.SameFile(got, want) {
			return true, nil
		}
	}
	return false, nil
}

// borrowedRepo is the workspace repository whose objects the store at dir
// borrows, or "" when it borrows none.
func (e *Engine) borrowedRepo(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "objects", "info", "alternates"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	objects := strings.TrimSpace(string(data))
	if !filepath.IsAbs(objects) {
		resolved, resolveErr := filepath.EvalSymlinks(filepath.Join(dir, "objects"))
		if resolveErr != nil {
			return "", resolveErr
		}
		objects = filepath.Join(resolved, objects)
	}
	repo := filepath.Dir(objects)
	parent, err := os.Stat(filepath.Dir(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	repos, err := os.Stat(e.cfg.ReposDir)
	if err != nil {
		return "", err
	}
	if !os.SameFile(parent, repos) {
		return "", nil
	}
	return filepath.Join(e.cfg.ReposDir, filepath.Base(repo)), nil
}

// reserveShare reserves, against the receive floor, a bound on what
// shareHistory writes: the full size of the objects the store's refs reach
// beyond the tree borrowed, the whole history with borrowed "". An object
// stored as a delta against one the pack leaves out is written whole.
func (e *Engine) reserveShare(ctx context.Context, store, borrowed string) (func(), error) {
	roots, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/")
	if err != nil {
		return nil, err
	}
	if borrowed != "" {
		roots += "^" + borrowed + "\n"
	}
	objects, err := e.gitInput(ctx, store, e.gitEnv(), []byte(roots), "rev-list", "--objects", "--no-object-names", "--stdin")
	if err != nil {
		return nil, err
	}
	sizes, err := e.gitInput(ctx, store, e.gitEnv(), []byte(objects), "cat-file", "--batch-check=%(objectsize)")
	if err != nil {
		return nil, err
	}
	var size int64
	for field := range strings.FieldsSeq(sizes) {
		n, parseErr := strconv.ParseInt(field, 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("gitengine: size of a history object: %w", parseErr)
		}
		size += n
	}
	grant, err := e.reserveVMReceive(store, size)
	if err != nil {
		return nil, err
	}
	if grant < size {
		e.releaseVMReceive(grant)
		return nil, fmt.Errorf("%w: the server has too little free disk", ErrSnapshotStorageLimit)
	}
	return func() { e.releaseVMReceive(grant) }, nil
}

// unpinHistory drops the pin of key in the workspace repository its store
// borrows from, if it borrows from one.
func (e *Engine) unpinHistory(ctx context.Context, key domain.RunID, store string) error {
	repo, err := e.borrowedRepo(store)
	if err != nil || repo == "" {
		return err
	}
	if _, err = os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_, err = e.git(ctx, repo, "update-ref", "-d", historyPinRoot+string(key))
	return err
}

// unshareHistory gives every store that borrows repo's objects its own copy
// of what it borrows, before repo is removed. The stores say who borrows,
// live ones that hold no pin too.
func (e *Engine) unshareHistory(ctx context.Context, repo string) error {
	entries, err := os.ReadDir(e.cfg.CheckoutsDir)
	if err != nil {
		return fmt.Errorf("gitengine: list diff history: %w", err)
	}
	for _, entry := range entries {
		key, ok := strings.CutSuffix(entry.Name(), snapshotStoreSuffix)
		if !ok || !entry.IsDir() {
			continue
		}
		if err = e.unshareStore(ctx, domain.RunID(key), repo); err != nil {
			return fmt.Errorf("gitengine: copy the fork point into the history of session %s: %w", key, err)
		}
	}
	return nil
}

func (e *Engine) unshareStore(ctx context.Context, key domain.RunID, repo string) error {
	store, err := e.snapshotStorePath(key)
	if err != nil {
		return err
	}
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	if borrowed, borrowErr := e.borrowedRepo(store); borrowErr != nil || borrowed != filepath.Clean(repo) {
		return borrowErr
	}
	release, err := e.reserveShare(ctx, store, "")
	if err != nil {
		return err
	}
	defer release()
	return e.shareHistory(ctx, key, store, "")
}

// ShareDetachedHistory shares the fork point of each removed checkout's store
// that still holds its own copy, as servers before shareHistory left them,
// with the repository of the workspace workspaceOf names for it, "" for none.
// It works one store at a time and skips a store whose repack could take the
// disk below the receive floor.
func (e *Engine) ShareDetachedHistory(ctx context.Context, workspaceOf func(context.Context, domain.RunID) (domain.WorkspaceID, error)) error {
	stores, err := os.ReadDir(e.cfg.CheckoutsDir)
	if err != nil {
		return fmt.Errorf("gitengine: list diff history: %w", err)
	}
	var errs []error
	for _, entry := range stores {
		key, ok := strings.CutSuffix(entry.Name(), snapshotStoreSuffix)
		if !ok || !entry.IsDir() {
			continue
		}
		if shareErr := e.shareDetachedStore(ctx, domain.RunID(key), workspaceOf); shareErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errs = append(errs, fmt.Errorf("gitengine: share the history of session %s: %w", key, shareErr))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) shareDetachedStore(ctx context.Context, key domain.RunID, workspaceOf func(context.Context, domain.RunID) (domain.WorkspaceID, error)) error {
	store, err := e.snapshotStorePath(key)
	if err != nil {
		return err
	}
	checkout, err := e.checkoutPath(key)
	if err != nil {
		return err
	}
	ws, err := workspaceOf(ctx, key)
	if err != nil || ws == "" {
		return err
	}
	repo, err := e.repoPath(ws)
	if err != nil {
		return err
	}
	e.repoMaintenanceMu.Lock()
	defer e.repoMaintenanceMu.Unlock()
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	// A checkout still there detaches its own store; a store that borrows
	// already shares.
	for _, path := range []string{checkout, filepath.Join(store, "objects", "info", "alternates")} {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return statErr
		}
	}
	// A first snapshot refused before staging leaves a store with no HEAD.
	if _, err = os.Stat(filepath.Join(store, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	base, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "refs/aether/base")
	if err != nil {
		return err
	}
	base = strings.TrimSpace(base)
	if !e.holdsTree(ctx, repo, base) {
		return nil
	}
	release, err := e.reserveShare(ctx, store, base)
	if err != nil {
		return err
	}
	defer release()
	return e.shareHistory(ctx, key, store, repo)
}
