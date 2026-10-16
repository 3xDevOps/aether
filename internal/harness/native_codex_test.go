package harness

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/pelletier/go-toml/v2"
)

func TestCodexNativeLaunchLoadsTheInboxHooksThroughConfigOverrides(t *testing.T) {
	p, _ := Lookup("codex")
	native, err := p.PrepareNativeLaunch("/run/aether", []string{"codex", "a task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(native.Files) != 0 || native.ReplacesStatus {
		t.Fatalf("files = %v, replaces status = %v, want neither", native.Files, native.ReplacesStatus)
	}
	if native.Env[coordtransport.ManagedHooksEnv] != "1" {
		t.Fatalf("env = %v, want %s=1", native.Env, coordtransport.ManagedHooksEnv)
	}
	asset, err := coordhooks.Files.ReadFile("codex.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err = json.Unmarshal(asset, &want); err != nil {
		t.Fatal(err)
	}
	if len(native.Args) != 2*len(want.Hooks) {
		t.Fatalf("args = %q, want one -c per event in codex.json", native.Args)
	}
	for i := 0; i < len(native.Args); i += 2 {
		if native.Args[i] != "-c" {
			t.Fatalf("args[%d] = %q, want -c", i, native.Args[i])
		}
		var parsed struct {
			Hooks map[string]any
		}
		if err = toml.Unmarshal([]byte(native.Args[i+1]), &parsed); err != nil {
			t.Fatalf("parse %q: %v", native.Args[i+1], err)
		}
		if len(parsed.Hooks) != 1 {
			t.Fatalf("%q sets %d events, want 1", native.Args[i+1], len(parsed.Hooks))
		}
		for event, got := range parsed.Hooks {
			if !reflect.DeepEqual(got, tomlShape(want.Hooks[event])) {
				t.Fatalf("%s = %v, want %v", event, got, want.Hooks[event])
			}
		}
	}
}

// tomlShape converts JSON numbers to the int64 go-toml decodes.
func tomlShape(v any) any {
	switch v := v.(type) {
	case float64:
		return int64(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = tomlShape(v[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = tomlShape(e)
		}
		return out
	}
	return v
}
