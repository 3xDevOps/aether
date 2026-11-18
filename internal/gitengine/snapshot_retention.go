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
// from repo under a pin. With repo empty, or a repo that lacks the tree, the
// store keeps all of it and borrows nothing. Older packs, and loose objects a
// pack holds, go, so a caller that can gives every tree the event log
// recorded a ref first. The caller holds repoMaintenanceMu, so no evidence
// prune that started before the pin can drop the tree, and then the snapshot
// lock of key.
func (e *Engine) shareHistory(ctx context.Context, key domain.RunID, store, repo string, keep ...string) error {
	base, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "refs/aether/base")
	if err != nil {
		return err
	}
	base = strings.TrimSpace(base)
	if repo != "" && !e.hasObject(ctx, repo, base) {
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
		if _, err = e.git(ctx, repo, "update-ref", historyPinRoot+string(key), base); err != nil {
			return err
		}
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
	if err = e.switchAlternates(store, repo, key); err != nil {
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

// switchAlternates makes the store borrow repo's objects, by a path relative
// to its own so a moved data directory keeps it, or nothing when repo is "".
func (e *Engine) switchAlternates(store, repo string, key domain.RunID) error {
	e.alternatesMu.Lock()
	defer e.alternatesMu.Unlock()
	if repo == "" {
		if err := os.Remove(filepath.Join(store, "objects", "info", "alternates")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	rel, err := filepath.Rel(filepath.Join(store, "objects"), filepath.Join(repo, "objects"))
	if err != nil {
		return err
	}
	_, err = borrowObjects(store, rel, key)
	return err
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
		objects = filepath.Join(dir, "objects", objects)
	}
	repo := filepath.Dir(objects)
	if filepath.Dir(repo) != filepath.Clean(e.cfg.ReposDir) {
		return "", nil
	}
	return repo, nil
}

// reserveShare reserves, against the receive floor, the disk the objects the
// store's refs reach take now, which bounds what shareHistory writes.
func (e *Engine) reserveShare(ctx context.Context, store string) (func(), error) {
	roots, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/")
	if err != nil {
		return nil, err
	}
	objects, err := e.gitInput(ctx, store, e.gitEnv(), []byte(roots), "rev-list", "--objects", "--no-object-names", "--stdin")
	if err != nil {
		return nil, err
	}
	sizes, err := e.gitInput(ctx, store, e.gitEnv(), []byte(objects+"\n"), "cat-file", "--batch-check=%(objectsize:disk)")
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
	release, err := e.reserveShare(ctx, store)
	if err != nil {
		return err
	}
	defer release()
	return e.shareHistory(ctx, key, store, "")
}

// ShareDetachedHistory shares the fork point of each removed checkout's store
// that still holds its own copy, as servers before shareHistory left them,
// with a workspace repository that holds it. It works one store at a time and
// skips a store whose repack could take the disk below the receive floor.
func (e *Engine) ShareDetachedHistory(ctx context.Context) error {
	stores, err := os.ReadDir(e.cfg.CheckoutsDir)
	if err != nil {
		return fmt.Errorf("gitengine: list diff history: %w", err)
	}
	entries, err := os.ReadDir(e.cfg.ReposDir)
	if err != nil {
		return fmt.Errorf("gitengine: list workspace repositories: %w", err)
	}
	var repos []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".git") {
			repos = append(repos, filepath.Join(e.cfg.ReposDir, entry.Name()))
		}
	}
	var errs []error
	for _, entry := range stores {
		key, ok := strings.CutSuffix(entry.Name(), snapshotStoreSuffix)
		if !ok || !entry.IsDir() {
			continue
		}
		if shareErr := e.shareDetachedStore(ctx, domain.RunID(key), repos); shareErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errs = append(errs, fmt.Errorf("gitengine: share the history of session %s: %w", key, shareErr))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) shareDetachedStore(ctx context.Context, key domain.RunID, repos []string) error {
	store, err := e.snapshotStorePath(key)
	if err != nil {
		return err
	}
	checkout, err := e.checkoutPath(key)
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
	for _, repo := range repos {
		if !e.hasObject(ctx, repo, strings.TrimSpace(base)) {
			continue
		}
		release, reserveErr := e.reserveShare(ctx, store)
		if reserveErr != nil {
			return reserveErr
		}
		defer release()
		return e.shareHistory(ctx, key, store, repo)
	}
	return nil
}
