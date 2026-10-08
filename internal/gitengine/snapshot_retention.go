package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const snapshotPublishedRef = "refs/aether/published"
const snapshotHistoryRoot = "refs/aether/history/"
const snapshotInflightParentRef = "refs/aether/inflight/parent"
const snapshotInflightTreeRef = "refs/aether/inflight/tree"

func (e *Engine) initSnapshotStore(ctx context.Context, run domain.RunID, checkout, store string) error {
	if err := e.ensureScratchGitDir(ctx, store, checkout); err != nil {
		return err
	}
	if _, err := scratchObjects(store, checkout, run); err != nil {
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
				return fmt.Errorf("gitengine: invalid recorded diff payload for run %s", run)
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
		listing, listErr := e.gitInput(ctx, store, gitEnv(), []byte(input.String()), "cat-file", "--batch-check=%(objectname) %(objecttype)")
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
			if _, updateErr := e.gitInput(ctx, store, gitEnv(), []byte(transaction), "update-ref", "--stdin"); updateErr != nil {
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
	_, err := e.gitInput(ctx, store, gitEnv(), []byte(transaction), "update-ref", "--stdin")
	return err
}

// detachSnapshotStore copies only recorded trees and their dependency closure,
// never the ancestry behind scratch branch-publication refs. Existing local
// objects are left intact. The caller stops the watcher and holds
// repoMaintenanceMu before snapshotLock, matching evidence capture's lock order.
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
	roots, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/")
	if err != nil {
		return err
	}
	if _, packErr := e.gitInput(ctx, store, gitEnv(), []byte(roots), "pack-objects", "--revs", "--non-empty", filepath.Join(store, "objects", "pack", "pack")); packErr != nil {
		return packErr
	}
	if removeErr := os.Remove(filepath.Join(store, "objects", "info", "alternates")); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}
