package server

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
)

// A session VM's disks and saved state belong to its session, the VMs that
// run for a member to that member, even when its ID and the updater's agent
// hold dashes, and the shared image store to the server.
func TestVMStorageOwners(t *testing.T) {
	s, root, admin, workspace := newWorkspaceDeletionServer(t)
	run := &domain.Run{WorkspaceID: workspace.ID, MemberID: admin.ID, Task: "vm", Harness: "fake", Mode: domain.LaunchHeadless, Status: domain.RunRunning}
	if err := s.db.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	u := disk.Usage{}
	for _, kindKey := range [][2]string{
		{"vm", string(run.ID)}, {"vm-state", string(run.ID)}, {"vm-db", string(run.ID)}, {"vm", "terminal-" + string(admin.ID)},
		{"vm", "assistant-install-" + string(admin.ID)}, {"vm", "assistant-" + string(admin.ID)},
		{"vm", "harness-update-" + string(admin.ID) + "-claude-code"}, {"vm", "harness-update-gone-claude"},
		{"vm", "run-gone"}, {"vm-image", ""}, {"vm-cache", "0123456789abcdef"},
	} {
		u.Entries = append(u.Entries, disk.Entry{Kind: kindKey[0], Key: kindKey[1], OwnerKind: "server"})
	}
	attributeStorage(t.Context(), Deps{Store: s.db, Runs: s.sched, DataDir: root}, &u, time.Now())
	want := []struct{ ownerKind, ownerID, reason string }{
		{"run", string(run.ID), "Active session"},
		{"run", string(run.ID), "Saved memory of a sleeping session VM; deleted when it wakes or its VM is deleted"},
		{"run", string(run.ID), "Agent databases of a session VM; deleted with the session's VM, kept across an account move"},
		{"member", string(admin.ID), "Environment VM disks, with what the member installed; deleted when the environment is removed"},
		{"member", string(admin.ID), "VM that installs the assistant's agent; deleted when the install ends"},
		{"member", string(admin.ID), "Assistant VM disks; deleted when the member's assistant is removed"},
		{"member", string(admin.ID), "Agent updater VM disks; deleted when the update ends"},
		{"server", "", "No durable owner found; cleanup eligibility unknown"},
		{"server", "", "No durable owner found; cleanup eligibility unknown"},
		{"server", "", "Disks session VMs boot from, stored once and shared by every VM; cleanup deletes those no image tag or VM still uses"},
		{"server", "", "Package cache new session VMs start from; rebuildable"},
	}
	for i, e := range u.Entries {
		if e.OwnerKind != want[i].ownerKind || e.OwnerID != want[i].ownerID || e.Reason != want[i].reason || e.Error != "" {
			t.Errorf("%s %s: got owner %s %q, reason %q, error %q; want %+v", e.Kind, e.Key, e.OwnerKind, e.OwnerID, e.Reason, e.Error, want[i])
		}
	}
}
