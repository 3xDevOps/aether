// Package permissions is the pure capability policy for Aether: who may
// view, steer, kill, message, hand off, protect, launch, and administer,
// given the actor's role, the workspaces they reach, run ownership, the
// run's protected flag, and the workspace's steer-others setting. It
// performs no I/O - callers resolve the actor and target from the store and
// map ErrDenied to the wire's CodeDenied and ErrNoAccess to CodeNotFound.
//
// A target that names a workspace is first resolved to the actor's role in
// it (Actor.RoleIn): a super admin is admin everywhere, an admin is admin in
// the workspaces they own and a collaborator in those they were granted, and
// a collaborator or viewer keeps their role in the workspaces they were
// granted. No role there is ErrNoAccess, whatever the capability.
//
// That role then meets the role table:
//
//	| Role         | Own runs   | Others' runs        | Workspace              |
//	|--------------|------------|---------------------|------------------------|
//	| viewer       | -          | view                | read feed              |
//	| collaborator | everything | view + steer + kill | launch runs            |
//	| admin        | everything | everything          | members, settings, ... |
//
// modified by two restrictions: a workspace with steer_others=admins_only
// limits messaging, steering and killing others' runs to owner and admins,
// and a protected run limits each of them to its owner and admins
// regardless of the workspace setting.
//
// Message lets one run's owner open a coordination conversation with
// another run in the same workspace. Coordination mail can start a turn in
// an idle run, so it follows the same two restrictions; a viewer has none.
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
	// Message opens a coordination conversation from a run the actor owns
	// to the target run. It grants no other access to the target.
	Message Capability = "message"
	// Launch starts new runs; collaborator and admin only.
	Launch Capability = "launch"
	// Push writes to a workspace's git repository (receive-pack);
	// collaborator and admin only. Fetch and clone are View.
	Push Capability = "push"
	// Handoff transfers run ownership; owner or admin only.
	Handoff Capability = "handoff"
	// Protect toggles a run's protected flag; owner or admin only.
	Protect Capability = "protect"
	// Sudo allows a run's command to run as root in its container, where it
	// acts on the owner's home and credentials; owner or admin only.
	Sudo Capability = "sudo"
	// WorkspaceAdmin is workspace administration (membership, settings);
	// admin in the target workspace only. Without a workspace it is
	// refused to everyone but a super admin.
	WorkspaceAdmin Capability = "workspace_admin"
	// CreateWorkspace creates a workspace the actor then owns; admins and
	// super admins.
	CreateWorkspace Capability = "create_workspace"
	// ServerAdmin is server-wide administration: roles, member removal,
	// updates, server settings; super admins only.
	ServerAdmin Capability = "server_admin"
)

// Actor is the member attempting the action. An Actor built without
// Access reaches no workspace unless it is a super admin.
type Actor struct {
	ID     domain.MemberID
	Role   domain.Role
	Access domain.WorkspaceAccess
}

// ActorOf is the actor for a member read from the store.
func ActorOf(m *domain.Member) Actor {
	return Actor{ID: m.ID, Role: m.Role, Access: m.Access}
}

// RoleIn is the role the actor acts with in workspace ws, false when they
// cannot reach it.
func (a Actor) RoleIn(ws domain.WorkspaceID) (domain.Role, bool) {
	switch {
	case a.Role == domain.RoleSuperAdmin:
		return domain.RoleAdmin, true
	case a.Role == domain.RoleAdmin && a.Access.Owned[ws]:
		return domain.RoleAdmin, true
	case a.Role == domain.RoleAdmin && a.Access.Granted[ws]:
		return domain.RoleCollaborator, true
	case a.Role == domain.RoleAdmin:
		return "", false
	case a.Access.Granted[ws]:
		return a.Role, true
	}
	return "", false
}

// Reaches reports whether the actor may see workspace ws at all.
func (a Actor) Reaches(ws domain.WorkspaceID) bool {
	_, ok := a.RoleIn(ws)
	return ok
}

// Target describes the acted-on run and its workspace's policy. The zero
// value is correct for capabilities that target no run (Launch,
// WorkspaceAdmin).
type Target struct {
	// Workspace is the workspace the action is in. Check resolves the
	// actor's role there and refuses with ErrNoAccess when they have none.
	Workspace domain.WorkspaceID
	// Extra is every other workspace the action reaches into, such as a
	// run's extra repositories. The actor must reach each one, or Check
	// refuses with ErrNoAccess.
	Extra []domain.WorkspaceID
	// Owner is the targeted run's owning member; empty when no run is
	// targeted.
	Owner domain.MemberID
	// Protected restricts Steer, Kill and Message to owner and admins
	// regardless of the workspace setting.
	Protected bool
	// SteerOthers is the workspace's steer-others policy: "" (permissive
	// default) or domain.SteerOthersAdminsOnly.
	SteerOthers string
}

// ErrDenied is the sentinel wrapped by every Check denial; the sshd maps
// it to protocol.CodeDenied.
var ErrDenied = errors.New("permission denied")

// ErrNoAccess is Check's answer for a workspace the actor cannot reach.
// The wire reports it exactly as a workspace that does not exist, so it
// never confirms that one does.
var ErrNoAccess = errors.New("not found")

func deny(msg string) error {
	return fmt.Errorf("%w: %s", ErrDenied, msg)
}

// Check reports whether actor may exercise cap against target: nil when
// allowed, an ErrDenied-wrapped error naming the failed rule otherwise.
// It is the single policy entrypoint; enforcement sites build the Actor
// and Target and never re-implement rules.
func Check(cap Capability, actor Actor, target Target) error {
	if actor.Role == domain.RoleSuperAdmin {
		return nil
	}
	role := actor.Role
	if target.Workspace != "" {
		var ok bool
		if role, ok = actor.RoleIn(target.Workspace); !ok {
			return ErrNoAccess
		}
	}
	for _, ws := range target.Extra {
		if !actor.Reaches(ws) {
			return ErrNoAccess
		}
	}
	switch cap {
	case ServerAdmin:
		return deny("this requires the super_admin role")
	case CreateWorkspace:
		if actor.Role == domain.RoleAdmin {
			return nil
		}
		return deny("creating a workspace requires the admin role")
	case WorkspaceAdmin:
		if role == domain.RoleAdmin && target.Workspace != "" {
			return nil
		}
		return deny("workspace administration requires the workspace's owner or a super admin")
	}
	if role == domain.RoleAdmin {
		return nil
	}
	actor.Role = role
	owner := actor.ID != "" && actor.ID == target.Owner
	switch cap {
	case View:
		return nil
	case Launch:
		if actor.Role == domain.RoleCollaborator {
			return nil
		}
		return deny("launching sessions requires the collaborator role")
	case Push:
		if actor.Role == domain.RoleCollaborator {
			return nil
		}
		return deny("pushing to a workspace requires the collaborator role")
	case Handoff:
		if owner {
			return nil
		}
		return deny("handoff requires the session's owner or an admin")
	case Protect:
		if owner {
			return nil
		}
		return deny("changing session protection requires the session's owner or an admin")
	case Sudo:
		if owner {
			return nil
		}
		return deny("allowing a command to run as root requires the session's owner or an admin")
	case Steer, Kill:
		if actor.Role != domain.RoleCollaborator {
			return deny(string(cap) + " requires the collaborator role")
		}
		if owner {
			return nil
		}
		if target.Protected {
			return deny("session is protected: only its owner or an admin may " + string(cap))
		}
		if target.SteerOthers == domain.SteerOthersAdminsOnly {
			return deny("workspace restricts " + string(cap) + " of others' sessions to their owner and admins")
		}
		return nil
	case Message:
		if actor.Role != domain.RoleCollaborator {
			return deny("messaging another session requires the collaborator role")
		}
		if owner {
			return nil
		}
		if target.Protected {
			return deny("session is protected: only its owner or an admin may message it")
		}
		if target.SteerOthers == domain.SteerOthersAdminsOnly {
			return deny("workspace restricts messaging others' sessions to their owner and admins")
		}
		return nil
	}
	return deny(fmt.Sprintf("unknown capability %q", cap))
}
