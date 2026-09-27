package coordcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordhooks"
)

func TestOpenCodeInstallationVersionBoundaries(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	plans := make(map[string]hookInstallPlan)
	for _, plan := range hookInstallationPlans(hookInstallInputs{home: home, cwd: project, env: func(string) string { return "" }}) {
		plans[plan.id] = plan
	}
	v1, ok1 := plans["opencode-v1"]
	v2, ok2 := plans["opencode-v2"]
	if !ok1 || !ok2 {
		t.Fatal("version-specific installation diagnostics missing")
	}
	asset, err := coordhooks.Files.ReadFile("opencode-v2.js")
	if err != nil {
		t.Fatal(err)
	}
	// Auto-discovered standalone V2 sources remain supported, but must not
	// be reported as a matching V1 integration.
	hookInstallFixture(t, filepath.Join(project, ".opencode", "plugins", "aether.js"), string(asset))
	for _, tc := range []struct {
		plan  hookInstallPlan
		state string
	}{{v1, "unverified"}, {v2, "configured"}} {
		result, inspectErr := inspectHookInstallation(tc.plan)
		if inspectErr != nil || result.state != tc.state {
			t.Fatalf("standalone V2 source for %s: %+v, %v, want %s", tc.plan.id, result, inspectErr, tc.state)
		}
	}
	v1Asset, err := coordhooks.Files.ReadFile("opencode-v1.js")
	if err != nil {
		t.Fatal(err)
	}
	packageEntry := filepath.Join(project, ".opencode", "plugins", "aether-mailbox", "index.js")
	hookInstallFixture(t, packageEntry, string(v1Asset))
	result, err := inspectHookInstallation(v2)
	if err != nil || result.state != "unverified" {
		t.Fatalf("V1 export reported configured as V2 package: %+v, %v", result, err)
	}
	hookInstallFixture(t, packageEntry, string(asset))
	result, err = inspectHookInstallation(v2)
	if err != nil || result.state != "configured" {
		t.Fatalf("valid V2 package was not detected: %+v, %v", result, err)
	}
}

func TestOpenCodeStaleGlobalSourceBlocksConfiguredClaim(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	var plan hookInstallPlan
	for _, candidate := range hookInstallationPlans(hookInstallInputs{home: home, cwd: project, env: func(string) string { return "" }}) {
		if candidate.id == "opencode-v2" {
			plan = candidate
		}
	}
	if plan.id == "" {
		t.Fatal("V2 installation diagnostics missing")
	}
	asset, err := coordhooks.Files.ReadFile("opencode-v2.js")
	if err != nil {
		t.Fatal(err)
	}
	hookInstallFixture(t, filepath.Join(project, ".opencode", "plugins", "aether-mailbox", "index.js"), string(asset))
	legacyPath := filepath.Join(home, ".config", "opencode", "plugins", "aether.js")
	olderSource := `export default { id: "aether-mailbox", setup() { return {} } };`
	hookInstallFixture(t, legacyPath, olderSource)
	result, err := inspectHookInstallation(plan)
	if err != nil || result.state != "unverified" {
		t.Fatalf("current package hid an older global ID owner: %+v, %v", result, err)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil || string(unchanged) != olderSource {
		t.Fatalf("inspection changed the user's earlier plugin: %q, %v", unchanged, err)
	}
}
