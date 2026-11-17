package cli

import (
	"slices"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/version"
)

// MatchServer is the release a client on running installs to catch up
// with a linked server: the server's release, when the server's gate
// orders it above the client (selfupdate.Ahead), so a client the gate
// refuses always has the server's release to move to and the client never
// moves ahead of it. Empty means no match. The server's version is
// checked as a tag because it lands in a download URL.
func MatchServer(running, server selfupdate.Release) string {
	server.Version = version.ReleaseCore(server.Version)
	if selfupdate.ValidTag(server.Version) && selfupdate.Ahead(server, running) {
		return server.Version
	}
	return ""
}

// ServerFirst reports that installing target on a client would move it
// past a linked server that this member, an admin, can have update
// itself: the server then goes first, and only to a release every client
// can install.
func ServerFirst(server, target selfupdate.Release, admin, capable bool) bool {
	return admin && capable && selfupdate.Ahead(target, server)
}

// ServerRelease is the release a server reports in server.info.
func ServerRelease(info protocol.ServerInfoResult) selfupdate.Release {
	return selfupdate.Release{Version: info.ServerVersion, Build: info.Build, Hotfix: info.Hotfix}
}

// ServerAdmin reports that the member in info administers the server itself:
// a super admin, or an admin of a server that predates workspace-scoped
// access and so reports its administrator as admin.
func ServerAdmin(info protocol.ServerInfoResult) bool {
	role := domain.Role(info.Member.Role)
	return role == domain.RoleSuperAdmin || (role == domain.RoleAdmin && !slices.Contains(info.Features, protocol.FeatureWorkspaceAccess))
}
