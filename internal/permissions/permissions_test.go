package permissions

import (
	"errors"
	"fmt"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// want computes the expected Check outcome straight from the design-doc
// role table, independently of the implementation's structure.
func want(cap Capability, role domain.Role, owner, protected bool, steerOthers string) bool {
	switch cap {
	case WorkspaceAdmin:
		return false
	case CreateWorkspace:
		return role == domain.RoleAdmin
	case ServerAdmin:
		return false
	}
	if role == domain.RoleAdmin {
		return true
	}
	switch cap {
	case View:
		return true
	case Launch, Push:
		return role == domain.RoleCollaborator
	case Handoff, Protect, Sudo:
		return owner
	case Steer, Kill, Message:
		if role != domain.RoleCollaborator {
			return false
		}
		if owner {
			return true
		}
		return !protected && steerOthers != domain.SteerOthersAdminsOnly
	}
	return false
}

// TestCheckFullMatrix exhaustively covers role x capability x ownership x
// protected x steer-others.
func TestCheckFullMatrix(t *testing.T) {
	roles := []domain.Role{domain.RoleViewer, domain.RoleCollaborator, domain.RoleAdmin}
	caps := []Capability{View, Steer, Kill, Message, Launch, Push, Handoff, Protect, Sudo, WorkspaceAdmin, CreateWorkspace, ServerAdmin}
	settings := []string{"", domain.SteerOthersAdminsOnly}
	actor := domain.MemberID("m_actor")
	other := domain.MemberID("m_other")

	for _, role := range roles {
		for _, cap := range caps {
			for _, owns := range []bool{true, false} {
				for _, protected := range []bool{false, true} {
					for _, setting := range settings {
						owner := other
						if owns {
							owner = actor
						}
						name := fmt.Sprintf("%s/%s/owns=%v/protected=%v/steer_others=%q",
							role, cap, owns, protected, setting)
						t.Run(name, func(t *testing.T) {
							err := Check(cap, Actor{ID: actor, Role: role}, Target{
								Owner:       owner,
								Protected:   protected,
								SteerOthers: setting,
							})
							allowed := err == nil
							if allowed != want(cap, role, owns, protected, setting) {
								t.Fatalf("Check = %v, want allowed=%v", err, !allowed)
							}
							if err != nil && !errors.Is(err, ErrDenied) {
								t.Fatalf("denial %v does not wrap ErrDenied", err)
							}
						})
					}
				}
			}
		}
	}
}

// TestCheckSpotRules pins the rules the matrix derives, as named examples
// mirroring the design doc's bullet points.
func TestCheckSpotRules(t *testing.T) {
	actor := domain.MemberID("m_actor")
	other := domain.MemberID("m_other")

	cases := []struct {
		name    string
		cap     Capability
		actor   Actor
		target  Target
		allowed bool
	}{
		{"viewer read-only view of another run", View,
			Actor{ID: actor, Role: domain.RoleViewer}, Target{Owner: other}, true},
		{"viewer cannot steer own run", Steer,
			Actor{ID: actor, Role: domain.RoleViewer}, Target{Owner: actor}, false},
		{"viewer cannot launch", Launch,
			Actor{ID: actor, Role: domain.RoleViewer}, Target{}, false},
		{"collaborator launches", Launch,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{}, true},
		{"collaborator kills another's run by default", Kill,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: other}, true},
		{"admins_only blocks collaborator steering another's run", Steer,
			Actor{ID: actor, Role: domain.RoleCollaborator},
			Target{Owner: other, SteerOthers: domain.SteerOthersAdminsOnly}, false},
		{"admins_only keeps own runs steerable", Steer,
			Actor{ID: actor, Role: domain.RoleCollaborator},
			Target{Owner: actor, SteerOthers: domain.SteerOthersAdminsOnly}, true},
		{"protected run blocks non-owner kill even by default", Kill,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: other, Protected: true}, false},
		{"protected run stays open to its owner", Steer,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: actor, Protected: true}, true},
		{"admin bypasses protected and admins_only", Kill,
			Actor{ID: actor, Role: domain.RoleAdmin},
			Target{Owner: other, Protected: true, SteerOthers: domain.SteerOthersAdminsOnly}, true},
		{"handoff by non-owner collaborator denied", Handoff,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: other}, false},
		{"handoff by owner allowed", Handoff,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: actor}, true},
		{"protect by owner allowed", Protect,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: actor}, true},
		{"viewer cannot message even from its own run", Message,
			Actor{ID: actor, Role: domain.RoleViewer}, Target{Owner: actor}, false},
		{"admins_only blocks messaging another's run", Message,
			Actor{ID: actor, Role: domain.RoleCollaborator},
			Target{Owner: other, SteerOthers: domain.SteerOthersAdminsOnly}, false},
		{"admins_only leaves messaging one's own run", Message,
			Actor{ID: actor, Role: domain.RoleCollaborator},
			Target{Owner: actor, SteerOthers: domain.SteerOthersAdminsOnly}, true},
		{"protected run refuses messages from another member", Message,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: other, Protected: true}, false},
		{"protected run accepts messages from its owner's runs", Message,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{Owner: actor, Protected: true}, true},
		{"admin messages a protected run", Message,
			Actor{ID: actor, Role: domain.RoleAdmin}, Target{Owner: other, Protected: true}, true},
		{"workspace admin requires admin", WorkspaceAdmin,
			Actor{ID: actor, Role: domain.RoleCollaborator}, Target{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.cap, tc.actor, tc.target)
			if (err == nil) != tc.allowed {
				t.Fatalf("Check = %v, want allowed=%v", err, tc.allowed)
			}
		})
	}
}

func TestUnknownCapabilityDenied(t *testing.T) {
	err := Check(Capability("fly"), Actor{ID: "m", Role: domain.RoleCollaborator}, Target{})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("unknown capability = %v, want ErrDenied", err)
	}
}

// TestCheckWorkspaceAccess pins the role each kind of member acts with in a
// workspace, and that an unreachable workspace is ErrNoAccess rather than
// a denial, whatever the capability.
func TestCheckWorkspaceAccess(t *testing.T) {
	const ws, other = domain.WorkspaceID("ws_a"), domain.WorkspaceID("ws_b")
	owns := domain.WorkspaceAccess{Owned: map[domain.WorkspaceID]bool{ws: true}}
	granted := domain.WorkspaceAccess{Granted: map[domain.WorkspaceID]bool{ws: true}}
	cases := []struct {
		name   string
		actor  Actor
		cap    Capability
		target domain.WorkspaceID
		want   error
	}{
		{"super admin administers any workspace", Actor{ID: "s", Role: domain.RoleSuperAdmin}, WorkspaceAdmin, other, nil},
		{"super admin is server admin", Actor{ID: "s", Role: domain.RoleSuperAdmin}, ServerAdmin, "", nil},
		{"admin administers an owned workspace", Actor{ID: "a", Role: domain.RoleAdmin, Access: owns}, WorkspaceAdmin, ws, nil},
		{"admin is not server admin", Actor{ID: "a", Role: domain.RoleAdmin, Access: owns}, ServerAdmin, "", ErrDenied},
		{"admin cannot administer without a workspace", Actor{ID: "a", Role: domain.RoleAdmin, Access: owns}, WorkspaceAdmin, "", ErrDenied},
		{"admin does not reach another admin's workspace", Actor{ID: "a", Role: domain.RoleAdmin, Access: owns}, View, other, ErrNoAccess},
		{"granted admin collaborates", Actor{ID: "a", Role: domain.RoleAdmin, Access: granted}, Launch, ws, nil},
		{"granted admin does not administer", Actor{ID: "a", Role: domain.RoleAdmin, Access: granted}, WorkspaceAdmin, ws, ErrDenied},
		{"granted admin is bound by protection", Actor{ID: "a", Role: domain.RoleAdmin, Access: granted}, Kill, ws, ErrDenied},
		{"admin creates workspaces", Actor{ID: "a", Role: domain.RoleAdmin}, CreateWorkspace, "", nil},
		{"collaborator cannot create workspaces", Actor{ID: "u", Role: domain.RoleCollaborator}, CreateWorkspace, "", ErrDenied},
		{"granted collaborator launches", Actor{ID: "u", Role: domain.RoleCollaborator, Access: granted}, Launch, ws, nil},
		{"granted viewer views", Actor{ID: "v", Role: domain.RoleViewer, Access: granted}, View, ws, nil},
		{"granted viewer cannot push", Actor{ID: "v", Role: domain.RoleViewer, Access: granted}, Push, ws, ErrDenied},
		{"collaborator without a grant cannot view", Actor{ID: "u", Role: domain.RoleCollaborator, Access: granted}, View, other, ErrNoAccess},
		{"ownership without the admin role reaches nothing", Actor{ID: "u", Role: domain.RoleCollaborator, Access: owns}, View, ws, ErrNoAccess},
		{"an actor built without access reaches nothing", Actor{ID: "u", Role: domain.RoleCollaborator}, View, ws, ErrNoAccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := Target{Workspace: tc.target, Owner: "someone else", Protected: true}
			err := Check(tc.cap, tc.actor, target)
			switch {
			case tc.want == nil && err != nil:
				t.Fatalf("Check = %v, want allowed", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("Check = %v, want %v", err, tc.want)
			case errors.Is(err, ErrNoAccess) && errors.Is(err, ErrDenied):
				t.Fatalf("Check = %v: an unreachable workspace must not read as a denial", err)
			}
		})
	}
}
