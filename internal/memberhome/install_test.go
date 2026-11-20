package memberhome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProgramUnitIsTheFileOrPackageALauncherRunsFrom(t *testing.T) {
	home := t.TempDir()
	file := func(rel string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		if err := os.Symlink(target, filepath.Join(home, rel)); err != nil {
			t.Fatal(err)
		}
	}
	file(".local/bin/omp")
	file(".local/share/claude/versions/2.1.329")
	link("/root/.local/share/claude/versions/2.1.329", ".local/bin/claude")
	file(".local/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js")
	link("../lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js", ".local/bin/claude-agent-acp")
	file(".local/lib/node_modules/opencode-ai/bin/opencode")
	link("../lib/node_modules/opencode-ai/bin/opencode", ".local/bin/opencode")
	link("/usr/bin/true", ".local/bin/elsewhere")

	for exe, want := range map[string]string{
		"omp":              ".local/bin/omp",
		"claude":           ".local/share/claude/versions/2.1.329",
		"claude-agent-acp": ".local/lib/node_modules/@agentclientprotocol/claude-agent-acp",
		"opencode":         ".local/lib/node_modules/opencode-ai",
		"elsewhere":        "",
		"missing":          "",
	} {
		if got, err := ProgramUnit(home, exe); err != nil || got != want {
			t.Errorf("ProgramUnit(%s) = %q, %v; want %q", exe, got, err, want)
		}
	}
}
