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
	if role == domain.RoleAdmin {
		return true
	}
	switch cap {
	case View:
		return true
	case SessionAdmin:
		return false
	case Launch, Push:
		return role == domain.RoleCollaborator
	case Handoff, Protect:
		return owner
	case Steer, Kill:
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
	caps := []Capability{View, Steer, Kill, Launch, Push, Handoff, Protect, SessionAdmin}
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
			Actor{actor, domain.RoleViewer}, Target{Owner: other}, true},
		{"viewer cannot steer own run", Steer,
			Actor{actor, domain.RoleViewer}, Target{Owner: actor}, false},
		{"viewer cannot launch", Launch,
			Actor{actor, domain.RoleViewer}, Target{}, false},
		{"collaborator launches", Launch,
			Actor{actor, domain.RoleCollaborator}, Target{}, true},
		{"collaborator kills another's run by default", Kill,
			Actor{actor, domain.RoleCollaborator}, Target{Owner: other}, true},
		{"admins_only blocks collaborator steering another's run", Steer,
			Actor{actor, domain.RoleCollaborator},
			Target{Owner: other, SteerOthers: domain.SteerOthersAdminsOnly}, false},
		{"admins_only keeps own runs steerable", Steer,
			Actor{actor, domain.RoleCollaborator},
			Target{Owner: actor, SteerOthers: domain.SteerOthersAdminsOnly}, true},
		{"protected run blocks non-owner kill even by default", Kill,
			Actor{actor, domain.RoleCollaborator}, Target{Owner: other, Protected: true}, false},
		{"protected run stays open to its owner", Steer,
			Actor{actor, domain.RoleCollaborator}, Target{Owner: actor, Protected: true}, true},
		{"admin bypasses protected and admins_only", Kill,
			Actor{actor, domain.RoleAdmin},
			Target{Owner: other, Protected: true, SteerOthers: domain.SteerOthersAdminsOnly}, true},
		{"handoff by non-owner collaborator denied", Handoff,
			Actor{actor, domain.RoleCollaborator}, Target{Owner: other}, false},
		{"handoff by owner allowed", Handoff,
			Actor{actor, domain.RoleCollaborator}, Target{Owner: actor}, true},
		{"protect by owner allowed", Protect,
			Actor{actor, domain.RoleCollaborator}, Target{Owner: actor}, true},
		{"session admin requires admin", SessionAdmin,
			Actor{actor, domain.RoleCollaborator}, Target{}, false},
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
	err := Check(Capability("fly"), Actor{"m", domain.RoleCollaborator}, Target{})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("unknown capability = %v, want ErrDenied", err)
	}
}
