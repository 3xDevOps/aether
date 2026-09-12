package localops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeToolsExecuteJavaScriptEntryPointOnWindows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("Node.js is not installed: %v", err)
	}

	// A direct node invocation must not hand this path to cmd.exe, whose
	// metacharacter parsing would change the entry point or its arguments.
	install := filepath.Join(t.TempDir(), "node tools [a&b];", "node_modules", "npm", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapperDir := filepath.Dir(filepath.Dir(filepath.Dir(install)))
	for _, wrapper := range []string{"npm.cmd", "npx.cmd"} {
		if err := os.WriteFile(filepath.Join(wrapperDir, wrapper), []byte("@echo off\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entry := `const args = process.argv.slice(2)
if (args[0] === "fail") {
  console.error("intentional entry failure")
  process.exit(19)
}
process.stdout.write(JSON.stringify(args))
`
	for _, script := range []string{"npm-cli.js", "npx-cli.js"} {
		if err := os.WriteFile(filepath.Join(install, script), []byte(entry), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tools, err := nodeToolsFor(node, filepath.Join(wrapperDir, "npm.cmd"), filepath.Join(wrapperDir, "npx.cmd"), "")
	if err != nil {
		t.Fatalf("nodeToolsFor: %v", err)
	}
	name, args := tools.command(tools.npm, "ok", "value with spaces & semicolon;")
	out, err := exec.CommandContext(context.Background(), name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Node entrypoint failed: %v\n%s", err, out)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Node result %q is not JSON: %v", out, err)
	}
	want := []string{"ok", "value with spaces & semicolon;"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Node result = %q, want %q", got, want)
	}

	name, args = tools.command(tools.npx, "fail")
	out, err = exec.CommandContext(context.Background(), name, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("Node entrypoint unexpectedly succeeded: %s", out)
	}
	if !strings.Contains(string(out), "intentional entry failure") {
		t.Fatalf("Node error output = %q, want the script error", out)
	}
}
