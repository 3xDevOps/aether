package coordcli

import (
	"strings"
	"testing"
)

func TestHookSummaryCodexManagedLaunchReportsTrust(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	env := map[string]string{"AETHER_MANAGED_HOOKS": "1", "AETHER_RUN_ID": "run-1", "AETHER_HARNESS": "codex"}
	in := hookInstallInputs{home: t.TempDir(), cwd: t.TempDir(), env: func(key string) string { return env[key] }}

	var before strings.Builder
	if err := writeHookSummaryWithInputs(&before, "codex", in); err != nil {
		t.Fatal(err)
	}
	if got := before.String(); !strings.Contains(got, "loaded by this run's launch, but Codex runs them only after you trust them once") || !strings.Contains(got, "/hooks then t") {
		t.Fatalf("summary before any hook call = %q", got)
	}

	writeHookNotices(hookNoticePath("run-1", "codex"), hookNotices{})
	var after strings.Builder
	if err := writeHookSummaryWithInputs(&after, "codex", in); err != nil {
		t.Fatal(err)
	}
	if got := after.String(); !strings.HasPrefix(got, "Inbox hooks (codex): loaded by this run's launch and running.") || strings.Contains(got, "trust") {
		t.Fatalf("summary after a hook call = %q", got)
	}
}
