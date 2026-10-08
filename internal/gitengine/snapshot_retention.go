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
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const snapshotPublishedRef = "refs/aether/published"
const snapshotHistoryRoot = "refs/aether/history/"
const snapshotInflightParentRef = "refs/aether/inflight/parent"
const snapshotInflightTreeRef = "refs/aether/inflight/tree"

// These are retained-history targets, not quotas on a concurrently edited
// checkout. Base, latest and the published interval boundary remain protected.
type snapshotPolicy struct {
	bytes int64
	trees int
	age   time.Duration
	now   func() time.Time
}

var defaultSnapshotPolicy = snapshotPolicy{512 << 20, 1024, 7 * 24 * time.Hour, time.Now}

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
	// Legacy stores had only a last-tip file. Import that boundary before
	// removing the marker; uncatalogued historical trees expire at scoped GC.
	data, err := os.ReadFile(filepath.Join(store, lastTreeFile))
	if err == nil {
		tree := strings.TrimSpace(string(data))
		if validObjectID(tree) {
			if _, _, err = e.gitBareBounded(ctx, store, 0, "update-ref", snapshotPublishedRef, tree); err != nil {
				return err
			}
		}
		if _, _, err = e.gitBareBounded(ctx, store, 0, "repack", "-a", "-d", "-l"); err != nil {
			return err
		}
		if _, _, err = e.gitBareBounded(ctx, store, 0, "prune", "--expire=now"); err != nil {
			return err
		}
		return os.Remove(filepath.Join(store, lastTreeFile))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// retainSnapshot requires snapshotLock through staging, pruning and any object
// copy. Only this sidecar's refs/objects are changed; alternates are read-only.
func (e *Engine) retainSnapshot(ctx context.Context, store, tree string, policy snapshotPolicy) error {
	latest, _, err := e.gitBareBounded(ctx, store, 256, "for-each-ref", "--format=%(objectname)", "refs/aether/latest")
	if err != nil {
		return err
	}
	listing, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--sort=refname", "--format=%(refname) %(objectname)", snapshotHistoryRoot)
	if err != nil {
		return err
	}
	refs := strings.FieldsFunc(listing, func(r rune) bool { return r == '\n' })
	// Only consecutive identical captures are unchanged. A revert is a new
	// occurrence: replace its old catalog entry so age/count use its latest
	// changed occurrence, without accumulating duplicate refs for one tree.
	transaction := "start\nupdate refs/aether/latest " + tree + "\n"
	if strings.TrimSpace(latest) != tree {
		ref := fmt.Sprintf("%s%019d-%s", snapshotHistoryRoot, policy.now().UnixNano(), tree)
		kept := refs[:0]
		for _, old := range refs {
			fields := strings.Fields(old)
			if fields[1] == tree {
				if fields[0] != ref {
					transaction += "delete " + fields[0] + "\n"
				}
				continue
			}
			kept = append(kept, old)
		}
		transaction += "update " + ref + " " + tree + "\n"
		refs = append(kept, ref+" "+tree)
	}
	transaction += "prepare\ncommit\n"
	if _, err = e.gitInput(ctx, store, gitEnv(), []byte(transaction), "update-ref", "--stdin"); err != nil {
		return err
	}
	cutoff := policy.now().Add(-policy.age).UnixNano()
	removed := false
	for len(refs) > 1 {
		fields := strings.Fields(refs[0])
		stamp := strings.TrimPrefix(fields[0], snapshotHistoryRoot)
		nanos, parseErr := strconv.ParseInt(strings.SplitN(stamp, "-", 2)[0], 10, 64)
		if parseErr != nil {
			return fmt.Errorf("gitengine: invalid snapshot retention ref")
		}
		if len(refs) <= policy.trees && nanos >= cutoff {
			break
		}
		if _, _, err = e.gitBareBounded(ctx, store, 0, "update-ref", "-d", fields[0]); err != nil {
			return err
		}
		refs = refs[1:]
		removed = true
	}
	bytes, err := snapshotObjectBytes(store)
	if err != nil {
		return err
	}
	// GC also drops uncatalogued legacy and failed-staging objects. Local
	// repacking never copies or prunes the checkout's alternate object store.
	if removed || bytes > policy.bytes {
		for {
			if _, _, err = e.gitBareBounded(ctx, store, 0, "repack", "-a", "-d", "-l"); err != nil {
				return err
			}
			if _, _, err = e.gitBareBounded(ctx, store, 0, "prune", "--expire=now"); err != nil {
				return err
			}
			bytes, err = snapshotObjectBytes(store)
			if err != nil || bytes <= policy.bytes || len(refs) <= 1 {
				return err
			}
			// Evict oldest half under byte pressure, then measure actual packs.
			count := max(1, len(refs)/2)
			for _, ref := range refs[:count] {
				if _, _, err = e.gitBareBounded(ctx, store, 0, "update-ref", "-d", strings.Fields(ref)[0]); err != nil {
					return err
				}
			}
			refs = refs[count:]
		}
	}
	return nil
}

func snapshotObjectBytes(store string) (int64, error) {
	var bytes int64
	err := filepath.WalkDir(filepath.Join(store, "objects"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
		}
		return nil
	})
	return bytes, err
}
