package protocol

import (
	"encoding/json"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestEnvEditParamsWireShape pins env.edit's param keys: the dashboard
// builds these by hand, so a renamed field is a silent break.
func TestEnvEditParamsWireShape(t *testing.T) {
	raw, err := json.Marshal(EnvEditParams{
		Workspace: WorkspaceSelector{ID: "ws_1"},
		Harness:   "claude",
		Request:   "add go 1.22",
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"workspace", "harness", "request"} {
		if _, ok := m[k]; !ok {
			t.Errorf("env.edit params missing key %q", k)
		}
	}
}

// TestEnvGetResultWireShape pins env.get's result keys and that the
// optional fields disappear when empty.
func TestEnvGetResultWireShape(t *testing.T) {
	raw, err := json.Marshal(EnvGetResult{
		Version:    2,
		Dockerfile: "FROM ubuntu:24.04\n",
		Manifest:   []domain.ManifestItem{{Name: "go"}},
		Source:     domain.EnvironmentSourceManual,
		Status:     domain.EnvironmentSaved,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "dockerfile", "manifest", "source", "status"} {
		if _, ok := m[k]; !ok {
			t.Errorf("env.get result missing key %q", k)
		}
	}
	for _, k := range []string{"harness", "diff"} {
		if _, ok := m[k]; ok {
			t.Errorf("env.get result carries empty optional key %q", k)
		}
	}
}
