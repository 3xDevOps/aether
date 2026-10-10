package server

import (
	"cmp"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

const storageEntryLimit = 50

type storageRuntime interface {
	StorageUsage(context.Context, string) disk.DockerUsage
}

type containerSizer interface {
	ContainerSizes(context.Context) (map[string]uint64, error)
}

const (
	containerSizeTTL     = 5 * time.Minute
	containerSizeTimeout = 2 * time.Minute
)

// containerSizes keeps the last completed measurement by creation key. The
// daemon walks every writable layer, which can outlast a request, so read
// never waits. Owners are resolved per reading: a handoff must show at once.
type containerSizes struct {
	mu      sync.Mutex
	sizes   map[string]uint64
	at      *time.Time
	err     string
	started time.Time
	running bool
}

func (c *containerSizes) read(rt containerSizer) (map[string]uint64, *time.Time, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running && time.Since(c.started) >= containerSizeTTL {
		c.running, c.started = true, time.Now()
		go c.measure(rt)
	}
	return c.sizes, c.at, c.err
}

// measure keeps the previous sizes when the daemon fails.
func (c *containerSizes) measure(rt containerSizer) {
	ctx, cancel := context.WithTimeout(context.Background(), containerSizeTimeout)
	defer cancel()
	sizes, err := rt.ContainerSizes(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false
	if err != nil {
		c.err = err.Error()
		return
	}
	now := time.Now().UTC()
	c.sizes, c.at, c.err = sizes, &now, ""
}

func storageEnricher(d Deps) func(*disk.Usage) {
	var containers containerSizes
	return func(u *disk.Usage) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if len(u.Entries) > storageEntryLimit {
			u.Entries = u.Entries[:storageEntryLimit:storageEntryLimit]
			u.Truncated = true
		}
		attributeStorage(ctx, d, u, time.Now().UTC())
		if rt, ok := d.Runtime.(containerSizer); ok {
			sizes, at, failure := containers.read(rt)
			owned, lookup := attributeContainers(ctx, d.Store, sizes)
			u.Containers, u.ContainersAt = owned, at
			u.ContainersError = strings.Join(nonemptyStorageErrors(failure, lookup), "; ")
		}
		if rt, ok := d.Runtime.(storageRuntime); ok {
			usage := rt.StorageUsage(ctx, d.DataDir)
			u.Docker = &usage
		} else {
			u.Docker = &disk.DockerUsage{Error: "Docker storage reporting is unavailable for this runtime."}
		}
	}
}

// attributeContainers keeps run and environment containers, largest first.
// Short-lived updater and verification containers have no durable owner.
func attributeContainers(ctx context.Context, db store.Store, sizes map[string]uint64) ([]disk.Container, string) {
	var containers []disk.Container
	var failure string
	for key, size := range sizes {
		if member, ok := strings.CutPrefix(key, "terminal:"); ok {
			containers = append(containers, disk.Container{OwnerKind: "member", OwnerID: member, MemberID: member, Bytes: size})
			continue
		}
		run, err := db.GetRun(ctx, domain.RunID(key))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			failure = "Container owner lookup failed"
			continue
		}
		containers = append(containers, disk.Container{OwnerKind: "run", OwnerID: string(run.ID), MemberID: string(run.MemberID), Bytes: size})
	}
	slices.SortFunc(containers, func(a, b disk.Container) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.OwnerID, b.OwnerID))
	})
	return containers, failure
}

func attributeStorage(ctx context.Context, d Deps, u *disk.Usage, now time.Time) {
	active, activeErr := d.Store.ListActiveRuns(ctx)
	inUse := make(map[string]bool, len(active))
	for _, run := range active {
		if run.Worktree != "" {
			inUse[filepath.Clean(run.Worktree)] = true
		}
	}
	var caches map[string]scheduler.CacheRetentionInfo
	var cacheErr error
	if d.Runs != nil {
		caches, cacheErr = d.Runs.CacheRetentions(ctx)
	}
	if cacheErr != nil {
		u.Warnings = append(u.Warnings, "Cache retention lookup is incomplete; unknown pools remain protected")
	}
	for i := range u.Entries {
		e := &u.Entries[i]
		switch e.Kind {
		case "database":
			e.Reason = "Persistent database and event history; no automatic expiry"
		case "other":
			e.Reason = "Other server state, profiles and caches; no general deletion policy"
		case "cache":
			e.OwnerKind = "member"
			e.OwnerID, e.Pool, _ = strings.Cut(e.Key, "/")
			e.Reason = "Cache ownership is unknown; pool protected"
			if retention, ok := caches[e.Key]; ok {
				e.RetainedUntil, e.Reason = retention.RetainedUntil, retention.Reason
				e.Error = strings.Join(nonemptyStorageErrors(e.Error, retention.Error), "; ")
			} else if cacheErr != nil || d.Runs == nil {
				e.Error = strings.Join(nonemptyStorageErrors(e.Error, "Cache retention lookup failed"), "; ")
			}
		case "home":
			member, err := d.Store.GetMember(ctx, domain.MemberID(e.Key))
			if err != nil {
				unknownStorageOwner(e, err)
				continue
			}
			e.OwnerKind, e.OwnerID = "member", string(member.ID)
			e.Reason = "Persistent member environment and credentials; no automatic expiry"
		case "repo":
			workspace, err := d.Store.GetWorkspace(ctx, domain.WorkspaceID(e.Key))
			if err != nil {
				unknownStorageOwner(e, err)
				continue
			}
			e.OwnerKind, e.OwnerID = "workspace", string(workspace.ID)
			e.Reason = "Persistent repository, run branches and shared clone objects; no automatic expiry"
		case "checkout", "snapshot", "transcript", "coord":
			run, err := d.Store.GetRun(ctx, domain.RunID(e.Key))
			if err != nil {
				unknownStorageOwner(e, err)
				continue
			}
			e.OwnerKind, e.OwnerID = "run", string(run.ID)
			if e.Kind == "snapshot" || e.Kind == "transcript" {
				e.Reason = "Run history retained until explicit deletion"
				continue
			}
			if e.Kind == "checkout" {
				expected := filepath.Join(d.DataDir, "checkouts", string(run.ID))
				if run.Worktree == "" || filepath.Clean(run.Worktree) != filepath.Clean(expected) {
					e.Reason = "Run does not own this checkout in durable state; cleanup eligibility unknown"
					continue
				}
				if activeErr != nil {
					e.Reason = "Active checkout ownership could not be checked"
					e.Error = "Run ownership lookup failed"
					continue
				}
				if inUse[filepath.Clean(run.Worktree)] {
					e.Reason = "Checkout is in use by an active run"
					continue
				}
			}
			if d.Runs == nil {
				e.Reason = "Lifecycle ownership is unavailable; cleanup eligibility unknown"
				continue
			}
			until, reason, err := d.Runs.StorageRetention(ctx, run.ID)
			if err != nil {
				e.Reason, e.Error = "Lifecycle ownership could not be checked", "Run retention lookup failed"
				continue
			}
			e.RetainedUntil = until
			if reason != "" {
				e.Reason = reason
				continue
			}
			if e.Kind == "coord" {
				e.Reason = "Run coordination assets await owned lifecycle cleanup"
				continue
			}
			ttl := d.Config.CheckoutTTL
			if ttl == 0 {
				ttl = 72 * time.Hour
			}
			if ttl < 0 {
				e.Reason = "Automatic checkout expiry is disabled"
			} else if run.FinishedAt == nil || !run.Status.Terminal() {
				e.Reason = "No durable terminal completion time; checkout protected"
			} else {
				until := run.FinishedAt.Add(ttl)
				e.RetainedUntil = &until
				if now.Before(until) {
					e.Reason = "Retained until checkout TTL expires; cleanup also requires evidence preservation"
				} else {
					e.Reason = "Checkout TTL expired; awaiting lifecycle and evidence cleanup checks"
				}
			}
		case "evidence":
			e.Reason = "Evidence owner is unknown; cleanup eligibility unknown"
		}
	}
	attributeEvidence(ctx, d.Store, u, now)
}

func nonemptyStorageErrors(values ...string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func unknownStorageOwner(e *disk.Entry, err error) {
	e.Reason = "No durable owner found; cleanup eligibility unknown"
	if !errors.Is(err, store.ErrNotFound) {
		e.Error = "Storage owner lookup failed"
	}
}

func attributeEvidence(ctx context.Context, db store.Store, u *disk.Usage, now time.Time) {
	wanted := make(map[string]*disk.Entry)
	for i := range u.Entries {
		if e := &u.Entries[i]; e.Kind == "evidence" {
			wanted[e.Key] = e
		}
	}
	if len(wanted) == 0 {
		return
	}
	fail := func() {
		for _, e := range wanted {
			e.Error = "Evidence ownership lookup failed or incomplete"
		}
	}
	packets, ok := db.(store.EvidencePacketStore)
	if !ok {
		fail()
		return
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		fail()
		return
	}
	for _, workspace := range workspaces {
		cursor := ""
		for {
			page, err := packets.ListEvidencePackets(ctx, workspace.ID, "", cursor, store.MaxCollaborationPageSize)
			if err != nil {
				fail()
				return
			}
			for _, packet := range page.Items {
				key := evidence.StorageKey(packet)
				if e := wanted[key]; e != nil {
					setEvidenceOwner(e, packet.RunID, packet.WorkspaceID, packet.ExpiresAt, now)
					if packet.Availability == store.EvidenceExpired {
						e.Reason = "Evidence expired; residual artifacts await cleanup"
					}
					delete(wanted, key)
				}
			}
			if len(wanted) == 0 || page.NextBefore == "" {
				break
			}
			cursor = page.NextBefore
		}
		if len(wanted) == 0 {
			return
		}
	}
	if staging, ok := db.(store.EvidenceStagingStore); ok {
		rows, err := staging.ListEvidenceStaging(ctx, now, store.MaxCollaborationPageSize)
		if err != nil {
			fail()
			return
		}
		for _, row := range rows {
			if e := wanted[row.ID]; e != nil {
				setEvidenceOwner(e, row.RunID, row.WorkspaceID, &row.ExpiresAt, now)
				e.Reason = "Evidence capture staging; " + e.Reason
				delete(wanted, row.ID)
			}
		}
		if len(rows) == store.MaxCollaborationPageSize {
			fail()
		}
	}
}

func setEvidenceOwner(e *disk.Entry, run domain.RunID, workspace domain.WorkspaceID, until *time.Time, now time.Time) {
	e.OwnerKind, e.OwnerID = "workspace", string(workspace)
	if run != "" {
		e.OwnerKind, e.OwnerID = "run", string(run)
	}
	e.RetainedUntil = until
	switch {
	case until == nil:
		e.Reason = "Evidence retained without automatic expiry"
	case now.Before(*until):
		e.Reason = "Evidence retained until its durable expiry"
	default:
		e.Reason = "Evidence expiry passed; awaiting owned cleanup"
	}
}
