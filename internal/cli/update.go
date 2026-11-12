package cli

import (
	"slices"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// MatchServer is the release a client on running installs to catch up
// with a linked server on serverVersion: the server's release, when it is
// newer, so the client never moves ahead of it. Empty means no match. The
// server's version is checked as a tag because it lands in a download URL.
func MatchServer(running, serverVersion string) string {
	core := version.ReleaseCore(serverVersion)
	if selfupdate.ValidTag(core) && selfupdate.Behind(running, core) {
		return core
	}
	return ""
}

// ServerFirst reports that installing target on a client would move it
// past a linked server on serverVersion that this member, an admin, can
// have update itself: the server then goes first, and only to a release
// every client can install.
func ServerFirst(serverVersion, target string, admin, capable bool) bool {
	return admin && capable && selfupdate.Behind(serverVersion, target)
}

// ServerAdmin reports that the member in info administers the server itself:
// a super admin, or an admin of a server that predates workspace-scoped
// access and so reports its administrator as admin.
func ServerAdmin(info protocol.ServerInfoResult) bool {
	role := domain.Role(info.Member.Role)
	return role == domain.RoleSuperAdmin || (role == domain.RoleAdmin && !slices.Contains(info.Features, protocol.FeatureWorkspaceAccess))
}
