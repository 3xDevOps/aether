package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestWorkspaceImportRefusesBeforeDialing(t *testing.T) {
	testhome.Isolate(t)
	empty := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"workspace", "import"}, workspaceImportUsage},
		{[]string{"workspace", "import", "https://gitlab.com/acme/app.git"}, "is not a GitHub repository; from another Git host, run aether workspace add"},
		{[]string{"workspace", "import", empty + "/missing"}, "no such file or directory"},
		{[]string{"workspace", "import", empty}, "not a git repository"},
	} {
		err := dispatch(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestWorkspacesWithOriginMatchesTheStoredForm(t *testing.T) {
	list := []protocol.Workspace{
		{ID: "ws_app", Name: "app", Origin: "https://github.com/acme/app.git"},
		{ID: "ws_local", Name: "local"},
		{ID: "ws_other", Name: "other", Origin: "https://example.com/acme/app.git"},
	}
	for _, origin := range []string{"git@github.com:acme/app.git", "https://github.com/acme/app", "ssh://git@github.com/acme/app.git"} {
		if got := workspacesWithOrigin(list, origin); len(got) != 1 || got[0].ID != "ws_app" {
			t.Errorf("workspacesWithOrigin(%q) = %+v, want ws_app", origin, got)
		}
	}
	if got := workspacesWithOrigin(list, ""); got != nil {
		t.Errorf("a clone with no origin matched %+v", got)
	}
}
