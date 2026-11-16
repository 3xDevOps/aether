package coordcli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Tests name the command themselves, whatever container runs them.
func init() { newCLIPath = "" }

// newNameContainer points the CLI at an executable new path, as in a
// container created since the new name was added.
func newNameContainer(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), coordhooks.NewCLI)
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	newCLIPath = path
	t.Cleanup(func() { newCLIPath = "" })
}

func skillOutput(t *testing.T, cfg Config) string {
	t.Helper()
	outsideEnhancedRun(t)
	task := "Fix /usr/local/bin/aether-internal handling"
	socket := newCLISocket(t, func(protocol.Request) protocol.Response {
		result, _ := json.Marshal(protocol.CoordStatusResult{RunID: "run-self", Task: task, Capabilities: []string{
			protocol.MethodCoordInbox, protocol.MethodCoordReport, protocol.MethodCoordSend, protocol.MethodChildStart, protocol.MethodMemorySearch,
		}})
		return protocol.Response{Result: result}
	})
	var out bytes.Buffer
	cfg.Socket, cfg.Out = socket.path, &out
	if code, err := Run(t.Context(), []string{"skill"}, cfg); err != nil || code != ExitOK {
		t.Fatalf("skill = %d, %v: %s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "Task summary (bounded): "+task+"\n") {
		t.Fatalf("skill changed the task it quotes:\n%s", out.String())
	}
	return strings.Replace(out.String(), task, "", 1)
}

// The skill names the command the container has: the new name only where
// its path is an executable file, and the task it quotes as written.
func TestSkillNamesTheContainersCommand(t *testing.T) {
	old := skillOutput(t, Config{})
	if strings.Contains(old, coordhooks.NewCLI) || !strings.Contains(old, "aether-internal inbox --wait 30") {
		t.Fatalf("a container without the new path was told another name:\n%s", old)
	}
	newNameContainer(t)
	named := skillOutput(t, Config{})
	if strings.Contains(named, coordhooks.OldCLI) || !strings.Contains(named, "cloudfield-internal inbox --wait 30") ||
		!strings.Contains(named, "`cloudfield-internal subsession start`") || !strings.Contains(named, "cloudfield-internal memory search") {
		t.Fatalf("a container with the new path was not told the new name:\n%s", named)
	}
}

// Usage text shows the name the command was run by, whichever the
// container has.
func TestUsageShowsTheNameItWasRunBy(t *testing.T) {
	newNameContainer(t)
	for _, name := range []string{coordhooks.OldCLI, coordhooks.NewCLI} {
		var out bytes.Buffer
		if code, err := Run(t.Context(), []string{"report", "--help"}, Config{Out: &out, Name: name}); err != nil || code != ExitOK {
			t.Fatalf("report --help = %d, %v", code, err)
		}
		other := coordhooks.NewCLI
		if name == other {
			other = coordhooks.OldCLI
		}
		if !strings.HasPrefix(out.String(), "usage: "+name+" report ") || strings.Contains(out.String(), other) {
			t.Fatalf("usage run as %s:\n%s", name, out.String())
		}
	}
}

// A hook's context names the command the container has. The hook runs
// under the old path from its file either way.
func TestHookContextNamesTheContainersCommand(t *testing.T) {
	_, _, socket, _ := hookRun(t, []string{"shared.go"}, true)
	hook := func(event, input string) string {
		t.Helper()
		var out bytes.Buffer
		code, err := Run(t.Context(), []string{"hook", "claude", event}, Config{Socket: socket, In: strings.NewReader(input), Out: &out})
		if err != nil || code != ExitOK {
			t.Fatalf("hook %s = %d, %v", event, code, err)
		}
		return out.String()
	}
	newNameContainer(t)
	start := hook("SessionStart", `{"hook_event_name":"SessionStart","source":"startup"}`)
	for _, want := range []string{"`cloudfield-internal skill`", "/usr/local/bin/cloudfield-internal inbox", "/usr/local/bin/cloudfield-internal status"} {
		if !strings.Contains(start, want) {
			t.Fatalf("SessionStart lacks %q: %s", want, start)
		}
	}
	if strings.Contains(start, coordhooks.OldCLI) {
		t.Fatalf("SessionStart names the old command: %s", start)
	}
	subagent := hook("SubagentStart", `{"hook_event_name":"SubagentStart","agent_id":"a1"}`)
	if !strings.Contains(subagent, "`cloudfield-internal status`") || strings.Contains(subagent, coordhooks.OldCLI) {
		t.Fatalf("SubagentStart: %s", subagent)
	}
	newCLIPath = ""
	if old := hook("SessionStart", `{"hook_event_name":"SessionStart","source":"clear"}`); strings.Contains(old, coordhooks.NewCLI) || !strings.Contains(old, "`aether-internal skill`") {
		t.Fatalf("SessionStart in a container without the new path: %s", old)
	}
}

// Hook and plugin files are copied into member homes, which containers made
// by any server version share, so they keep calling the old path. hook file
// writes each byte for byte as main shipped it, under either name.
func TestHookFilesKeepTheOldPath(t *testing.T) {
	newNameContainer(t)
	want := map[string]string{
		"claude.json":    "9067ceacb4e51295250dbde48de50ba9f09a15ff1342bb313aea5ad856cb92d9",
		"codex.json":     "e7e3426d852ac72a723c509c0813f697ce0f546e6e168102e0c185d041774083",
		"copilot.json":   "5005106a467ce2f7a91c376c60b13d2ec2be6e179742182c90a6b41ebbf8b794",
		"cursor.json":    "efcd9c1f391956678bbd6889ccf5b79e3b08071f991521a6eaacb3f314dd6931",
		"gemini.json":    "f1ec13b3c9c56b5853ced533a6a85e408abb793a6e171ac1c5601fb2b1560df6",
		"omp.ts":         "13aa53ff460e5aaeabd082d14dd92855781fa8522a09556973827e7391f86200",
		"pi.ts":          "ef3a6a803bdc55a17ead2bcb7dc7154cc20844f79793a7c19196740f4f9c1f56",
		"opencode-v1.js": "d15121335e55da4ab89aa50784ccf44a9642a75a7040e1e5af1d369f7287649e",
		"opencode-v2.js": "9fee34ddfc4822be74512a71636bbe1817ea12aa76634ea48fb4f65046cb07ea",
		"generic.sh":     "3d591abcd87d4b7ee1d41a90499f49ed755f4a307bc116e813f1b22c5894c943",
	}
	entries, err := coordhooks.Files.ReadDir(".")
	if err != nil || len(entries) != len(want) {
		t.Fatalf("hook files: %d, %v; want %d", len(entries), err, len(want))
	}
	for name, sum := range want {
		var out bytes.Buffer
		if code, err := Run(t.Context(), []string{"hook", "file", name}, Config{Out: &out, Name: coordhooks.NewCLI}); err != nil || code != ExitOK {
			t.Fatalf("hook file %s = %d, %v", name, code, err)
		}
		got := sha256.Sum256(out.Bytes())
		if hex.EncodeToString(got[:]) != sum || strings.Contains(out.String(), coordhooks.NewCLI) || !strings.Contains(out.String(), "/usr/local/bin/aether-internal") {
			t.Errorf("hook file %s is not the file main shipped:\n%s", name, out.String())
		}
	}
}
