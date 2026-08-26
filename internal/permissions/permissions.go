// Package permissions is the pure capability policy for Aether: who may
// view, steer, kill, hand off, protect, launch, and administer, given the
// actor's role, run ownership, the run's protected flag, and the
// workspace's steer-others setting. It performs no I/O - callers resolve
// the actor and target from the store and map ErrDenied to the wire's
// CodeDenied.
//
// The policy is the design doc's role table:
//
//	| Role         | Own runs   | Others' runs        | Workspace              |
//	|--------------|------------|---------------------|------------------------|
//	| viewer       | -          | view                | read feed              |
//	| collaborator | everything | view + steer + kill | launch runs            |
//	| admin        | everything | everything          | members, settings, ... |
//
// modified by two restrictions: a workspace with steer_others=admins_only
// limits steering and killing others' runs to owner and admins, and a
// protected run limits steer AND kill to its owner and admins regardless
// of the workspace setting.
//
// Git push over SSH is the Push capability: writing to a workspace's
// repository is a collaborator action, so a viewer is read-only on the
// git transport as well as the control channel. Fetch and clone are View.
package permissions

import (
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Capability is one guarded class of action.
type Capability string

const (
	// View is read-only visibility: run lists and gets, read-only attach,
	// the event feed. Every member has it.
	View Capability = "view"
	// Steer is write access to a run: attach-write, inject, approve,
	// pause, resume, relaunch.
	Steer Capability = "steer"
	// Kill terminates a run: kill and close.
	Kill Capability = "kill"
	// Launch starts new runs; collaborator and admin only.
	Launch Capability = "launch"
	// Push writes to a workspace's git repository (receive-pack);
	// collaborator and admin only. Fetch and clone are View.
	Push Capability = "push"
	// Handoff transfers run ownership; owner or admin only.
	Handoff Capability = "handoff"
	// Protect toggles a run's protected flag; owner or admin only.
	Protect Capability = "protect"
	// WorkspaceAdmin is workspace administration (membership, settings);
	// admin only.
	WorkspaceAdmin Capability = "workspace_admin"
)

// Actor is the member attempting the action.
type Actor struct {
	ID   domain.MemberID
	Role domain.Role
}

// Target describes the acted-on run and its workspace's policy. The zero
// value is correct for capabilities that target no run (Launch,
// WorkspaceAdmin).
type Target struct {
	// Owner is the targeted run's owning member; empty when no run is
	// targeted.
	Owner domain.MemberID
	// Protected restricts Steer and Kill to owner and admins regardless
	// of the workspace setting.
	Protected bool
	// SteerOthers is the workspace's steer-others policy: "" (permissive
	// default) or domain.SteerOthersAdminsOnly.
	SteerOthers string
}

// ErrDenied is the sentinel wrapped by every Check denial; the sshd maps
// it to protocol.CodeDenied.
var ErrDenied = errors.New("permission denied")

func deny(msg string) error {
	return fmt.Errorf("%w: %s", ErrDenied, msg)
}

// Check reports whether actor may exercise cap against target: nil when
// allowed, an ErrDenied-wrapped error naming the failed rule otherwise.
// It is the single policy entrypoint; enforcement sites build the Actor
// and Target and never re-implement rules.
func Check(cap Capability, actor Actor, target Target) error {
	if actor.Role == domain.RoleAdmin {
		return nil
	}
	owner := actor.ID != "" && actor.ID == target.Owner
	switch cap {
	case View:
		return nil
	case WorkspaceAdmin:
		return deny("workspace administration requires the admin role")
	case Launch:
		if actor.Role == domain.RoleCollaborator {
			return nil
		}
		return deny("launching runs requires the collaborator role")
	case Push:
		if actor.Role == domain.RoleCollaborator {
			return nil
		}
		return deny("pushing to a workspace requires the collaborator role")
	case Handoff:
		if owner {
			return nil
		}
		return deny("handoff requires the run's owner or an admin")
	case Protect:
		if owner {
			return nil
		}
		return deny("changing run protection requires the run's owner or an admin")
	case Steer, Kill:
		if actor.Role != domain.RoleCollaborator {
			return deny(string(cap) + " requires the collaborator role")
		}
		if owner {
			return nil
		}
		if target.Protected {
			return deny("run is protected: only its owner or an admin may " + string(cap))
		}
		if target.SteerOthers == domain.SteerOthersAdminsOnly {
			return deny("workspace restricts " + string(cap) + " of others' runs to their owner and admins")
		}
		return nil
	}
	return deny(fmt.Sprintf("unknown capability %q", cap))
}
