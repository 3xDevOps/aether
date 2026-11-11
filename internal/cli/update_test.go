package cli

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
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
