package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/guest/home"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// promoteTimeout bounds one cache promotion, which runs in the background.
const promoteTimeout = 30 * time.Minute

// checkouts lists a spec's checkout and extra worktrees, in Docker's order.
func checkouts(spec runtime.Spec) []runtime.Mount {
	var out []runtime.Mount
	if spec.WorktreeHostPath != "" {
		out = append(out, runtime.Mount{HostPath: spec.WorktreeHostPath, ContainerPath: spec.WorktreeMountPath})
	}
	return append(out, spec.ExtraWorktrees...)
}

func (e *engine) plan(rec *record) (plan, error) {
	return planMounts(rec.Checkouts, rec.Mounts, rec.NestedDocker, e.dir(rec.Key), rootOnly)
}

// homeConfig serves every file as user's, "uid:gid", the session user, or
// root for an empty user.
func (p plan) homeConfig(user string) *home.Config {
	if len(p.home) == 0 {
		return nil
	}
	uid, gid, _ := strings.Cut(user, ":")
	u, _ := strconv.ParseUint(uid, 10, 32)
	g, _ := strconv.ParseUint(gid, 10, 32)
	return &home.Config{Shares: p.home, UID: uint32(u), GID: uint32(g)}
}

func cacheMount(mounts []runtime.Mount) (runtime.Mount, bool) {
	for _, m := range mounts {
		if m.Kind == runtime.MountCache {
			return m, true
		}
	}
	return runtime.Mount{}, false
}

// cachePool names the member's pool of caches by the host directory a
// container would bind for it.
func cachePool(m runtime.Mount) string {
	sum := sha256.Sum256([]byte(m.HostPath))
	return hex.EncodeToString(sum[:8])
}

// promoteCache makes the cache a stopped VM ran on its pool's newest, so the
// member's next session starts from it. It holds the key's lock only for a
// copy, a clone where the filesystem can, and converts in the background.
// An Isolated session's cache, an overlay in a container, never goes back.
func (e *engine) promoteCache(ctx context.Context, rec *record) {
	m, ok := cacheMount(rec.Mounts)
	if !ok || !rec.CacheRan || m.Overlay != "" {
		return
	}
	snapshot, err := e.cfg.Images.SnapshotCache(ctx, rec.Cache.Path)
	if err != nil {
		slog.Warn("vm runtime: copy the cache of a stopped VM", "vm", rec.Key, "error", err)
		return
	}
	rec.CacheRan = false
	if err := e.store(rec); err != nil {
		slog.Warn("vm runtime: record the cache promotion", "vm", rec.Key, "error", err)
	}
	go func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), promoteTimeout)
		defer cancel()
		if err := e.cfg.Images.PromoteCache(bg, cachePool(m), snapshot); err != nil {
			slog.Warn("vm runtime: promote the cache of a stopped VM", "vm", rec.Key, "error", err)
		}
	}()
}
