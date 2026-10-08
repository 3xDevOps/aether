package server

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/store"
)

const storageEntryLimit = 50

type storageRuntime interface {
	StorageUsage(context.Context, string) disk.DockerUsage
}

func storageEnricher(d Deps) func(*disk.Usage) {
	return func(u *disk.Usage) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if len(u.Entries) > storageEntryLimit {
			u.Entries = u.Entries[:storageEntryLimit:storageEntryLimit]
			u.Truncated = true
		}
		attributeStorage(ctx, d, u, time.Now().UTC())
		if rt, ok := d.Runtime.(storageRuntime); ok {
			usage := rt.StorageUsage(ctx, d.DataDir)
			u.Docker = &usage
		} else {
			u.Docker = &disk.DockerUsage{Error: "Docker storage reporting is unavailable for this runtime."}
		}
	}
}

func attributeStorage(ctx context.Context, d Deps, u *disk.Usage, now time.Time) {
	active, activeErr := d.Store.ListActiveRuns(ctx)
	inUse := make(map[string]bool, len(active))
	for _, run := range active {
		if run.Worktree != "" {
			inUse[filepath.Clean(run.Worktree)] = true
		}
	}
	for i := range u.Entries {
		e := &u.Entries[i]
		switch e.Kind {
		case "database":
			e.Reason = "Persistent database and event history; no automatic expiry"
		case "other":
			e.Reason = "Other server state, profiles and caches; no general deletion policy"
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
			if e.Kind == "transcript" && run.Worktree == "" {
				e.Reason = "No owned checkout; transcript automatic cleanup eligibility unknown"
				continue
			}
			if e.Kind == "checkout" || e.Kind == "snapshot" || e.Kind == "transcript" {
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
				e.Reason = "Automatic checkout and transcript expiry is disabled"
			} else if run.FinishedAt == nil || !run.Status.Terminal() {
				e.Reason = "No durable terminal completion time; checkout and transcript protected"
			} else {
				until := run.FinishedAt.Add(ttl)
				e.RetainedUntil = &until
				if now.Before(until) {
					e.Reason = "Retained until checkout TTL expires; transcript cleanup also requires evidence preservation"
				} else {
					e.Reason = "Checkout TTL expired; checkout and transcript await lifecycle and evidence cleanup checks"
				}
			}
		case "evidence":
			e.Reason = "Evidence owner is unknown; cleanup eligibility unknown"
		}
	}
	attributeEvidence(ctx, d.Store, u, now)
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
