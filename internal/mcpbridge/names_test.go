package mcpbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// Tests name the command themselves, whatever container runs them.
func init() { newCLIPath = "" }

// The bridge's instructions and tool descriptions name the command the
// container has: the new name only where its path is an executable file.
func TestBridgeNamesTheContainersCommand(t *testing.T) {
	t.Setenv(coordtransport.RoleCardEnv, "")
	status, err := json.Marshal(protocol.CoordStatusResult{RunID: "run-self", Capabilities: []string{protocol.MethodCoordReport, protocol.MethodCoordSend}})
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeCoord(t, func(protocol.Request) protocol.Response { return protocol.Response{Result: status} })
	exe := filepath.Join(t.TempDir(), coordhooks.NewCLI)
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { newCLIPath = "" })
	for _, tc := range []struct{ path, want, other string }{
		{"", coordhooks.OldCLI, coordhooks.NewCLI},
		{exe, coordhooks.NewCLI, coordhooks.OldCLI},
	} {
		newCLIPath = tc.path
		cs := session(t, fake.path, nil)
		text := cs.InitializeResult().Instructions
		tools, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range tools.Tools {
			text += "\n" + tool.Description
		}
		if !strings.Contains(text, "`"+tc.want+" skill`") || strings.Contains(text, tc.other) {
			t.Errorf("with the new path at %q the bridge does not name only %s:\n%s", tc.path, tc.want, text)
		}
	}
}
