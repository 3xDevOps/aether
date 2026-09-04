package localgw

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/localops"
)

// profileScanTestJSON answers the fixture home below: the one harness
// present there, with the two categories its files fall into.
const profileScanTestJSON = `{"harnesses":[{"harness":"claude","import":true,"categories":["memory","skills"],` +
	`"reason":"skills/review/SKILL.md is a hand-written skill and CLAUDE.md carries standing instructions."}]}`

// profileScanStubOutput is the shell fragment a profile stub runs: it
// finds the scratch directory the prompt names and writes profile.json
// there.
const profileScanStubOutput = `out=$(printf '%%s\n' "$1" | grep -o '/[^[:space:]:]*aether-envscan-[^[:space:]:]*' | head -n 1)
cat > "$out/profile.json" <<'PROFILEEOF'
%s
PROFILEEOF
`

// profileScanFrame decodes the frames a profile scan sends; it is the
// envscan frame plus the recommendation the terminal frame carries.
type profileScanFrame struct {
	Type           string                  `json:"type"`
	Status         string                  `json:"status"`
	Line           string                  `json:"line"`
	Recommendation *profile.Recommendation `json:"recommendation"`
	Detail         string                  `json:"detail"`
	OutputTail     string                  `json:"output_tail"`
}

// readUntilProfileTerminal reads frames until result or error, collecting
// the statuses seen on the way.
func readUntilProfileTerminal(t *testing.T, conn *websocket.Conn) (terminal profileScanFrame, statuses []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		frame := readWSJSON[profileScanFrame](t, conn)
		switch frame.Type {
		case "status":
			statuses = append(statuses, frame.Status)
		case "output":
		case "result", "error":
			return frame, statuses
		default:
			t.Fatalf("unknown frame type %q", frame.Type)
		}
	}
	t.Fatal("no terminal frame arrived")
	return
}

// setProfileHome points profile discovery at a fixture home holding one
// claude configuration, so a scan sees exactly one harness.
func setProfileHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(root, "skills", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "CLAUDE.md"):                    "# standing instructions\nrun the tests.\n",
		filepath.Join(root, "skills", "review", "SKILL.md"): "# review\nread the diff.\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestProfileScanStreamsRecommendation covers the profile branch end to
// end: the socket streams statuses and ends with a result frame carrying
// the agent's import recommendation.
func TestProfileScanStreamsRecommendation(t *testing.T) {
	setProfileHome(t)
	promptLog := filepath.Join(t.TempDir(), "prompt.log")
	argv := writeScanStub(t, fmt.Sprintf(`echo reading inventory
printf '%%s' "$1" > %q
`+profileScanStubOutput, promptLog, profileScanTestJSON))

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	conn := wsDial(t, base, "/ws/envscan", g.Token())

	writeWSJSON(t, conn, map[string]string{"harness": "claude", "mode": localops.ScanModeProfile})
	terminal, statuses := readUntilProfileTerminal(t, conn)
	if terminal.Type != "result" {
		t.Fatalf("terminal frame = %+v, want result (detail: %s)", terminal, terminal.Detail)
	}
	if terminal.Recommendation == nil || len(terminal.Recommendation.Harnesses) != 1 {
		t.Fatalf("recommendation = %+v, want one harness", terminal.Recommendation)
	}
	entry := terminal.Recommendation.Harnesses[0]
	if entry.Harness != "claude" || !entry.Import {
		t.Errorf("entry = %+v, want claude imported", entry)
	}
	if fmt.Sprint(entry.Categories) != fmt.Sprint([]string{"memory", "skills"}) {
		t.Errorf("categories = %v, want [memory skills]", entry.Categories)
	}
	wantStatuses := []string{"detecting", "running", "validating"}
	if fmt.Sprint(statuses) != fmt.Sprint(wantStatuses) {
		t.Errorf("statuses = %v, want %v", statuses, wantStatuses)
	}
	prompt, err := os.ReadFile(promptLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"harness: claude", "skills/review/SKILL.md", "profile.json"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("profile prompt missing %q", want)
		}
	}
	expectClose(t, conn, websocket.StatusNormalClosure)
}

// TestProfileScanEmptyMachine: a machine with no agent configuration is a
// normal answer, not a crash - one error frame saying there is nothing to
// import, and a clean close.
func TestProfileScanEmptyMachine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	g, base := newWSGateway(t, &wsStubBackend{})
	conn := wsDial(t, base, "/ws/envscan", g.Token())

	writeWSJSON(t, conn, map[string]string{"harness": "claude", "mode": localops.ScanModeProfile})
	terminal, _ := readUntilProfileTerminal(t, conn)
	if terminal.Type != "error" {
		t.Fatalf("terminal frame = %+v, want error", terminal)
	}
	if terminal.Detail != "no agent configuration found on this machine; nothing to import" {
		t.Errorf("detail = %q, want the nothing-to-import message", terminal.Detail)
	}
	expectClose(t, conn, websocket.StatusNormalClosure)
}

// TestProfileScanRefusesConcurrentScan: the profile branch claims the
// same single scan slot every other scan does.
func TestProfileScanRefusesConcurrentScan(t *testing.T) {
	setProfileHome(t)
	started := filepath.Join(t.TempDir(), "started")
	argv := writeScanStub(t, fmt.Sprintf("touch %q\nsleep 60\n", started))

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	first := wsDial(t, base, "/ws/envscan", g.Token())
	writeWSJSON(t, first, map[string]string{"harness": "claude", "mode": localops.ScanModeProfile})
	waitForFile(t, started)

	second := wsDial(t, base, "/ws/envscan", g.Token())
	writeWSJSON(t, second, map[string]string{"harness": "claude", "mode": localops.ScanModeProfile})
	refusal := readWSJSON[profileScanFrame](t, second)
	if refusal.Type != "error" || !strings.Contains(refusal.Detail, "already running") {
		t.Fatalf("refusal frame = %+v, want an already-running error", refusal)
	}
	expectClose(t, second, websocket.StatusPolicyViolation)
	_ = first.Close(websocket.StatusNormalClosure, "")
}
