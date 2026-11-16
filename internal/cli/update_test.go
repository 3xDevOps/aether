package cli

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
)

func TestServerAdminFollowsTheServersRoleModel(t *testing.T) {
	access := []string{protocol.FeatureWorkspaceAccess}
	for name, tc := range map[string]struct {
		features []string
		role     string
		want     bool
	}{
		"old server admin":        {nil, "admin", true},
		"old server collaborator": {nil, "collaborator", false},
		"old server super admin":  {nil, "super_admin", true},
		"new server admin":        {access, "admin", false},
		"new server super admin":  {access, "super_admin", true},
		"new server collaborator": {access, "collaborator", false},
	} {
		info := protocol.ServerInfoResult{Features: tc.features, Member: protocol.Member{Role: tc.role}}
		if got := ServerAdmin(info); got != tc.want {
			t.Errorf("%s: ServerAdmin = %v, want %v", name, got, tc.want)
		}
	}
}

// A client with a build is warned by the same version comparison the
// server's gate refuses it by, while server.info carries no build.
func TestUpgradeRequiredFollowsTheServersGateWithABuild(t *testing.T) {
	oldVersion, oldBuild := version.Version, version.Build
	version.Version, version.Build = "v0.9.0-beta.2", "412"
	t.Cleanup(func() { version.Version, version.Build = oldVersion, oldBuild })
	info := protocol.ServerInfoResult{ServerVersion: "v0.9.0-nightly.10", MinClientVersion: "v0.9.0-nightly.10"}
	if UpgradeRequired(info) == nil {
		t.Fatal("no update-required warning for a client below the server's min_client_version")
	}
	info.MinClientVersion = "v0.9.0-beta.1"
	if perr := UpgradeRequired(info); perr != nil {
		t.Fatalf("warning %v for a client above min_client_version", perr)
	}
}
