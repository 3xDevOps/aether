package harness

import (
	"maps"
	"testing"
)

func TestMergeEnvCombinesConfigVariables(t *testing.T) {
	env := map[string]string{
		"OPENCODE_CONFIG_CONTENT": `{"model":"m", // the member's
"permission":{"bash":"ask"},"instructions":["STYLE.md"],"plugin":["mine"]}`,
		"COPILOT_CUSTOM_INSTRUCTIONS_DIRS": "/home/me/notes",
		"KEEP":                             "1",
	}
	if err := MergeEnv(env, map[string]string{
		"OPENCODE_CONFIG_CONTENT":          `{"permission":"allow","plugin":["file:///run/aether/p.js"],"instructions":["/run/aether/instructions.md"]}`,
		"COPILOT_CUSTOM_INSTRUCTIONS_DIRS": "/run/aether",
		"ADDED":                            "2",
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPENCODE_CONFIG_CONTENT":          `{"instructions":["STYLE.md","/run/aether/instructions.md"],"model":"m","permission":"allow","plugin":["mine","file:///run/aether/p.js"]}`,
		"COPILOT_CUSTOM_INSTRUCTIONS_DIRS": "/home/me/notes,/run/aether",
		"KEEP":                             "1",
		"ADDED":                            "2",
	}
	if !maps.Equal(env, want) {
		t.Fatalf("merged env = %v, want %v", env, want)
	}

	broken := map[string]string{"OPENCODE_CONFIG_CONTENT": "[1]"}
	if err := MergeEnv(broken, map[string]string{"OPENCODE_CONFIG_CONTENT": `{"permission":"allow"}`}); err == nil {
		t.Fatal("merged a config variable that is not an object")
	}
}
