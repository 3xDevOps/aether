package harness

import (
	"maps"
	"testing"
)

func TestMergeEnvCombinesConfigVariables(t *testing.T) {
	env := map[string]string{
		"OPENCODE_CONFIG_CONTENT": `{"model":"m", // the member's
"permission":{"bash":"ask"}}`,
		"KEEP": "1",
	}
	if err := MergeEnv(env, map[string]string{
		"OPENCODE_CONFIG_CONTENT": `{"permission":"allow","plugin":["file:///run/aether/p.js"]}`,
		"ADDED":                   "2",
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPENCODE_CONFIG_CONTENT": `{"model":"m","permission":"allow","plugin":["file:///run/aether/p.js"]}`,
		"KEEP":                    "1",
		"ADDED":                   "2",
	}
	if !maps.Equal(env, want) {
		t.Fatalf("merged env = %v, want %v", env, want)
	}

	broken := map[string]string{"OPENCODE_CONFIG_CONTENT": "[1]"}
	if err := MergeEnv(broken, map[string]string{"OPENCODE_CONFIG_CONTENT": `{"permission":"allow"}`}); err == nil {
		t.Fatal("merged a config variable that is not an object")
	}
}
