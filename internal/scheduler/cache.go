package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	cachePoolTarget  uint64 = 4 << 30
	cacheTotalTarget uint64 = 16 << 30
	cacheInactiveAge        = 7 * 24 * time.Hour
)

// CacheRetentionInfo describes ownership, not a promise of physically freed
// bytes: hardlinks can have other owners outside managed cache roots.
type CacheRetentionInfo struct {
	RetainedUntil *time.Time
	Reason        string
	Error         string
	Protected     bool
}

type cacheOwnership struct {
	runs     map[domain.MemberID]bool
	terminal map[domain.MemberID]bool
	legacy   map[domain.MemberID]bool
}

func newCacheOwnership() cacheOwnership {
	return cacheOwnership{make(map[domain.MemberID]bool), make(map[domain.MemberID]bool), make(map[domain.MemberID]bool)}
}

// cacheOwnershipSnapshot is one batch of durable ownership and one short live
// snapshot. It never probes or mutates the runtime. A malformed sidecar without
// a resolvable owner protects every cache rather than guessing who owns it.
func (s *Scheduler) cacheOwnershipSnapshot(ctx context.Context) (cacheOwnership, error) {
	owners := newCacheOwnership()
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return owners, err
	}
	for _, run := range active {
		owners.runs[run.HomeMember()] = true
		owners.legacy[run.HomeMember()] = true
		owners.legacy[run.AccountMember()] = true
	}
	entries, err := os.ReadDir(s.cfg.StateDir)
	if err != nil {
		return owners, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := domain.RunID(strings.TrimSuffix(entry.Name(), ".json"))
		sc, readErr := s.readSidecar(id)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return owners, readErr
		}
		if sc.RunID != string(id) {
			return owners, errors.New("run ownership metadata mismatch")
		}
		run, getErr := s.cfg.Store.GetRun(ctx, id)
		if getErr != nil {
			return owners, getErr
		}
		// Even an empty-container marker may carry unfinished evidence or
		// creation-gap ownership. The existing owner cleanup removes it only
		// after confirmation; cache GC does not second-guess that decision.
		owners.runs[run.HomeMember()] = true
		owners.legacy[run.HomeMember()] = true
		owners.legacy[run.AccountMember()] = true
	}
	terminals, err := os.ReadDir(s.terminalSidecarDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return owners, err
	}
	for _, entry := range terminals {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		member := domain.MemberID(strings.TrimSuffix(entry.Name(), ".json"))
		sc, readErr := s.readTerminalSidecar(member)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return owners, readErr
		}
		if sc.TerminalMember != string(member) {
			return owners, errors.New("terminal ownership metadata mismatch")
		}
		owners.terminal[member], owners.legacy[member] = true, true
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return owners, err
	}
	for _, member := range members {
		terminal, err := s.cfg.Store.GetTerminal(ctx, member.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return owners, err
		}
		if terminal != nil {
			owners.terminal[member.ID], owners.legacy[member.ID] = true, true
		}
	}
	s.addLiveCacheOwners(&owners)
	return owners, nil
}

// addLiveCacheOwners is also called after GC acquires a member's creation lock:
// a launch that published after the batch snapshot must still protect its pool.
func (s *Scheduler) addLiveCacheOwners(owners *cacheOwnership) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		owners.runs[run.memberID], owners.legacy[run.memberID] = true, true
		owners.legacy[run.loginMember] = true
	}
	for member := range s.terminals {
		owners.terminal[member], owners.legacy[member] = true, true
	}
	for key, update := range s.harnessUpdates {
		if update.running != nil {
			member := domain.MemberID(filepath.Base(key.home))
			owners.terminal[member], owners.legacy[member] = true, true
		}
	}
	for member := range s.agentInstalls {
		owners.terminal[member], owners.legacy[member] = true, true
	}
}

func cacheKey(member domain.MemberID, pool string) string { return string(member) + "/" + pool }

func cacheRetention(info memberhome.CacheInfo, owners cacheOwnership, ownershipErr error) CacheRetentionInfo {
	out := CacheRetentionInfo{Error: info.CleanupError}
	protected := owners.runs[info.Member]
	if info.Pool == memberhome.CachePoolTerminal {
		protected = owners.terminal[info.Member]
	}
	switch {
	case ownershipErr != nil || info.MetadataError != "":
		out.Protected, out.Reason, out.Error = true, "Cache ownership uncertain; preserved", "Cache ownership metadata unavailable"
	case protected || len(info.Owners) != 0:
		out.Protected, out.Reason = true, "Active, retained or finalizing owner; cache protected"
	default:
		until := info.LastUsed.Add(cacheInactiveAge)
		out.RetainedUntil = &until
		out.Reason = "Inactive reconstructible cache; eligible at age or byte pressure"
	}
	return out
}

// CacheRetentions reads metadata only. It neither creates cache directories nor
// walks their bytes, and uses one all-owner snapshot for the entire response.
func (s *Scheduler) CacheRetentions(ctx context.Context) (map[string]CacheRetentionInfo, error) {
	out := make(map[string]CacheRetentionInfo)
	if s.cfg.Homes == nil {
		return out, nil
	}
	infos, err := s.cfg.Homes.ListCaches()
	if err != nil {
		return out, err
	}
	owners, ownerErr := s.cacheOwnershipSnapshot(ctx)
	for _, info := range infos {
		out[cacheKey(info.Member, info.Pool)] = cacheRetention(info, owners, ownerErr)
	}
	return out, ownerErr
}

func (s *Scheduler) touchRunCache(member domain.MemberID) {
	s.touchCache(member, memberhome.CachePoolRuns)
}
func (s *Scheduler) touchTerminalCache(member domain.MemberID) {
	s.touchCache(member, memberhome.CachePoolTerminal)
}
func (s *Scheduler) touchCache(member domain.MemberID, pool string) {
	if s.cfg.Homes == nil || member == "" {
		return
	}
	unlock := s.cfg.Homes.LockCaches(member)
	defer unlock()
	if err := s.cfg.Homes.TouchCache(member, pool); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("scheduler: record cache ownership release", "member", member, "pool", pool, "error", err)
	}
}

// sweepCaches is shared by startup, the existing hourly maintenance and one
// pressure retry. It never waits for a busy owner, kills idle work or prunes
// unrelated runtime resources. Lock order is terminal -> cache -> short mu.
func (s *Scheduler) sweepCaches(ctx context.Context, pressure bool) {
	if s.cfg.Homes == nil || ctx.Err() != nil {
		return
	}
	owners, err := s.cacheOwnershipSnapshot(ctx)
	if err != nil {
		slog.Warn("scheduler: cache cleanup ownership unavailable", "error", err)
		return
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return
	}
	current := make(map[domain.MemberID]bool, len(members))
	ids, err := s.cfg.Homes.CacheMembers()
	if err != nil {
		return
	}
	known := make(map[domain.MemberID]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	for _, member := range members {
		current[member.ID] = true
		if !known[member.ID] {
			ids = append(ids, member.ID)
			known[member.ID] = true
		}
	}
	// Establish retry owners for pre-upgrade and removed-member home roots.
	for _, member := range ids {
		unlock, ok := s.cfg.Homes.TryLockCaches(member)
		if !ok {
			continue
		}
		for _, pool := range []string{memberhome.CachePoolRuns, memberhome.CachePoolTerminal} {
			if _, readErr := s.cfg.Homes.ReadCache(member, pool); errors.Is(readErr, os.ErrNotExist) {
				if writeErr := s.cfg.Homes.EnsureCacheMetadata(member, pool); writeErr != nil {
					slog.Warn("scheduler: preserve cache cleanup owner", "member", member, "error", writeErr)
				}
			}
		}
		unlock()
	}
	infos, err := s.cfg.Homes.ListCaches()
	if err != nil {
		return
	}
	for _, info := range infos {
		if info.MetadataError != "" {
			slog.Warn("scheduler: cache cleanup metadata uncertain")
			return
		}
		if len(info.Owners) != 0 {
			owners.legacy[info.Member] = true
		}
	}
	bytes, err := disk.MeasureCachePools(s.cfg.Homes.CacheRoot())
	if err != nil {
		slog.Warn("scheduler: cache cleanup measurement unavailable", "error", err)
		return
	}
	var total uint64
	for _, n := range bytes {
		total += n
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].LastUsed.Before(infos[j].LastUsed) })
	for _, info := range infos {
		if ctx.Err() != nil {
			return
		}
		terminalLock := s.terminalLock(info.Member)
		if !terminalLock.TryLock() {
			continue
		}
		unlock, ok := s.cfg.Homes.TryLockCaches(info.Member)
		if !ok {
			terminalLock.Unlock()
			continue
		}
		func() {
			defer unlock()
			defer terminalLock.Unlock()
			// Re-read metadata under the lock, including a release since sorting.
			latest, readErr := s.cfg.Homes.ReadCache(info.Member, info.Pool)
			if readErr != nil || latest.MetadataError != "" {
				return
			}
			for _, pool := range []string{memberhome.CachePoolRuns, memberhome.CachePoolTerminal} {
				poolInfo, err := s.cfg.Homes.ReadCache(info.Member, pool)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return
				}
				if poolInfo.MetadataError != "" {
					return
				}
				if len(poolInfo.Owners) != 0 {
					owners.legacy[info.Member] = true
				}
			}
			s.addLiveCacheOwners(&owners)
			if err := s.resolveCacheRuntimeOwners(ctx, &latest, &owners); err != nil {
				_ = s.cfg.Homes.SetCacheRuntimeCleanupError(info.Member, info.Pool, "Runtime ownership unavailable; cache protected")
				return
			}
			if len(latest.Owners) == 0 {
				if err := s.cfg.Homes.SetCacheRuntimeCleanupError(info.Member, info.Pool, ""); err != nil {
					return
				}
			}
			retention := cacheRetention(latest, owners, nil)
			if retention.Protected {
				_ = s.cfg.Homes.TouchCache(info.Member, info.Pool)
			} else {
				n := bytes[cacheKey(info.Member, info.Pool)]
				eligible := latest.DataCleanupPending || pressure || n > cachePoolTarget || total > cacheTotalTarget || !s.cfg.Now().Before(latest.LastUsed.Add(cacheInactiveAge)) || !current[info.Member]
				if (latest.DataExists || latest.DataCleanupPending) && eligible {
					if err := s.cfg.Homes.RemoveCache(ctx, info.Member, info.Pool); err != nil {
						slog.Warn("scheduler: remove inactive cache", "member", info.Member, "pool", info.Pool, "error", err)
						return
					}
					// Apparent de-duplicated retained bytes, not freed disk space.
					total -= min(total, n)
				}
			}
			if info.Pool != memberhome.CachePoolTerminal {
				return
			}
			// Legacy caches share HOME, so both pools' owners must be absent.
			if !owners.legacy[info.Member] && len(latest.Owners) == 0 {
				if current[info.Member] {
					if err := s.protectConfiguredLegacyCaches(ctx, info.Member); err != nil {
						_ = s.cfg.Homes.SetCacheLegacyCleanupError(info.Member, "Legacy cache configuration unavailable; cleanup deferred")
						return
					}
				}
				if err := s.cfg.Homes.RemoveLegacyCaches(ctx, info.Member); err != nil {
					_ = s.cfg.Homes.SetCacheLegacyCleanupError(info.Member, "Legacy cache cleanup failed; automatic retry pending")
					return
				}
				if err := s.cfg.Homes.SetCacheLegacyCleanupError(info.Member, ""); err != nil {
					return
				}
				if !current[info.Member] {
					if err := s.cfg.Homes.Remove(ctx, info.Member); err != nil {
						_ = s.cfg.Homes.SetCacheCleanupError(info.Member, info.Pool, "Removed member home cleanup failed; automatic retry pending")
						return
					}
				}
			}
			s.sweepMemberImages(ctx, info.Member, "")
		}()
	}
}

// resolveCacheRuntimeOwners closes deterministic terminal and detached-updater
// creation gaps. An exited updater may be destroyed; a running/unknown one is
// never stopped. The durable key is removed only after confirmed destruction.
func (s *Scheduler) resolveCacheRuntimeOwners(ctx context.Context, info *memberhome.CacheInfo, owners *cacheOwnership) error {
	member := info.Member
	if _, err := s.cfg.Runtime.FindByCreationKey(ctx, terminalCreationKey(member)); err == nil {
		owners.terminal[member], owners.legacy[member] = true, true
	} else if !errors.Is(err, runtime.ErrNotFound) {
		return err
	}
	if info.Pool != memberhome.CachePoolTerminal {
		return nil
	}
	// Pre-upgrade updaters had deterministic keys but no cache metadata.
	// Discover them before touching legacy HOME; never stop a live updater.
	for _, profile := range harness.Profiles() {
		if profile.UpdateScript == "" {
			continue
		}
		key := "harness-update-" + string(member) + "-" + profile.Name
		if _, err := s.cfg.Runtime.FindByCreationKey(ctx, key); err == nil {
			if ownerErr := s.cfg.Homes.AddCacheOwner(member, info.Pool, key); ownerErr != nil {
				return ownerErr
			}
		} else if !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
	}
	latest, err := s.cfg.Homes.ReadCache(member, info.Pool)
	if err != nil {
		return err
	}
	*info = latest
	for _, key := range info.Owners {
		// A published in-memory updater covers its pre-create and teardown gaps.
		// A different terminal owner protects data, not this updater's runtime.
		s.mu.Lock()
		updating := false
		for updateKey, state := range s.harnessUpdates {
			if state.running != nil && domain.MemberID(filepath.Base(updateKey.home)) == member &&
				key == "harness-update-"+string(member)+"-"+updateKey.harness {
				updating = true
				break
			}
		}
		s.mu.Unlock()
		if updating {
			continue
		}
		if !strings.HasPrefix(key, "harness-update-"+string(member)+"-") {
			return errors.New("unknown cache owner key")
		}
		cid, findErr := s.cfg.Runtime.FindByCreationKey(ctx, key)
		if findErr == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			_, waitErr := s.cfg.Runtime.Wait(probeCtx, cid)
			cancel()
			if waitErr != nil && !errors.Is(waitErr, runtime.ErrNotFound) {
				owners.terminal[member], owners.legacy[member] = true, true
				if errors.Is(waitErr, context.DeadlineExceeded) {
					continue
				}
				return waitErr
			}
			if err = s.cfg.Runtime.Destroy(ctx, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return err
			}
		} else if !errors.Is(findErr, runtime.ErrNotFound) {
			return findErr
		}
		if err = s.cfg.Homes.RemoveCacheOwner(member, info.Pool, key); err != nil {
			return err
		}
	}
	latest, err = s.cfg.Homes.ReadCache(member, info.Pool)
	if err != nil {
		return fmt.Errorf("read released cache ownership: %w", err)
	}
	*info = latest
	return nil
}
