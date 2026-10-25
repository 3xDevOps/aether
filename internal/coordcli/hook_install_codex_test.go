package coordcli

import (
	"strings"
	"testing"
)

func TestHookSummaryCodexManagedLaunchNeedsNoHooks(t *testing.T) {
	env := map[string]string{"AETHER_MANAGED_HOOKS": "1", "AETHER_RUN_ID": "run-1", "AETHER_HARNESS": "codex"}
	in := hookInstallInputs{home: t.TempDir(), cwd: t.TempDir(), env: func(key string) string { return env[key] }}
	var got strings.Builder
	if err := writeHookSummaryWithInputs(&got, "codex", in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.String(), "Inbox (codex): Aether hands this run its mail through Codex's app-server") {
		t.Fatalf("summary = %q", got.String())
	}
}
