package localgw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/localops"
)

// envScanTestDockerfile and envScanTestManifest are a minimal pair that
// passes the envdef contract, written by the stub agents below.
const envScanTestDockerfile = `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends jq
`

const envScanTestManifest = `[{"name":"jq","version":"1.7.1","reason":"test","start_line":2,"end_line":2,"check_command":"jq --version"}]`

// writeScanStub writes an executable shell script and returns the argv
// override that runs it with the rendered prompt as its first argument,
// mirroring the localops envscan tests.
func writeScanStub(t *testing.T, body string) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", script, harness.TaskPlaceholder}
}

// scanFrame decodes any frame the envscan socket sends.
type scanFrame struct {
	Type         string `json:"type"`
	Status       string `json:"status"`
	Line         string `json:"line"`
	Dockerfile   string `json:"dockerfile"`
	ManifestJSON string `json:"manifest_json"`
	Manifest     []struct {
		Name string `json:"name"`
	} `json:"manifest"`
	Detail     string `json:"detail"`
	OutputTail string `json:"output_tail"`
}

// readUntilTerminal reads frames until result or error, collecting the
// statuses and output lines seen on the way.
func readUntilTerminal(t *testing.T, conn *websocket.Conn) (terminal scanFrame, statuses, lines []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		frame := readWSJSON[scanFrame](t, conn)
		switch frame.Type {
		case "status":
			statuses = append(statuses, frame.Status)
		case "output":
			lines = append(lines, frame.Line)
		case "result", "error":
			return frame, statuses, lines
		default:
			t.Fatalf("unknown frame type %q", frame.Type)
		}
	}
	t.Fatal("no terminal frame arrived")
	return
}

func TestLocalEnvHarnesses(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	rec := do(g, http.MethodPost, "/local/v1/env.harnesses", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("env.harnesses = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Harnesses []localops.HarnessStatus `json:"harnesses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []localops.HarnessStatus{
		{Name: "claude", Installed: true},
		{Name: "codex", Installed: false},
		{Name: "pi", Installed: false},
		{Name: "amp", Installed: false},
	}
	if len(got.Harnesses) != len(want) {
		t.Fatalf("harnesses = %+v, want %+v", got.Harnesses, want)
	}
	for i := range want {
		if got.Harnesses[i] != want[i] {
			t.Errorf("harness %d = %+v, want %+v", i, got.Harnesses[i], want[i])
		}
	}
}

// TestEnvScanStreamsStubScan covers the happy path end to end: start
// frame, status and output frames in order, and one result frame carrying
// the validated pair.
func TestEnvScanStreamsStubScan(t *testing.T) {
	argv := writeScanStub(t, fmt.Sprintf(`echo inspecting
cat > Dockerfile <<'EOF'
%sEOF
cat > manifest.json <<'EOF'
%s
EOF
`, envScanTestDockerfile, envScanTestManifest))

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	conn := wsDial(t, base, "/ws/envscan", g.Token())

	writeWSJSON(t, conn, map[string]string{"harness": "claude", "mode": localops.ScanModeInventory})
	terminal, statuses, lines := readUntilTerminal(t, conn)
	if terminal.Type != "result" {
		t.Fatalf("terminal frame = %+v, want result", terminal)
	}
	if terminal.Dockerfile != envScanTestDockerfile {
		t.Errorf("dockerfile = %q, want %q", terminal.Dockerfile, envScanTestDockerfile)
	}
	if !strings.Contains(terminal.ManifestJSON, `"jq"`) {
		t.Errorf("manifest_json = %q, want the raw manifest text", terminal.ManifestJSON)
	}
	if len(terminal.Manifest) != 1 || terminal.Manifest[0].Name != "jq" {
		t.Errorf("manifest = %+v, want one jq item", terminal.Manifest)
	}
	wantStatuses := []string{"detecting", "running", "validating"}
	if fmt.Sprint(statuses) != fmt.Sprint(wantStatuses) {
		t.Errorf("statuses = %v, want %v", statuses, wantStatuses)
	}
	found := false
	for _, line := range lines {
		if line == "inspecting" {
			found = true
		}
	}
	if !found {
		t.Errorf("output lines %v missing the stub's echo", lines)
	}
	expectClose(t, conn, websocket.StatusNormalClosure)
}

// TestEnvScanFailureSendsErrorFrame: a crashing agent ends the scan with
// one error frame carrying the detail and the output tail.
func TestEnvScanFailureSendsErrorFrame(t *testing.T) {
	argv := writeScanStub(t, "echo agent broke\nexit 3\n")

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	conn := wsDial(t, base, "/ws/envscan", g.Token())

	writeWSJSON(t, conn, map[string]string{"harness": "claude", "mode": localops.ScanModeInventory})
	terminal, _, _ := readUntilTerminal(t, conn)
	if terminal.Type != "error" {
		t.Fatalf("terminal frame = %+v, want error", terminal)
	}
	if terminal.Detail == "" {
		t.Error("error frame carries no detail")
	}
	if !strings.Contains(terminal.OutputTail, "agent broke") {
		t.Errorf("output tail = %q, want the agent's output", terminal.OutputTail)
	}
	expectClose(t, conn, websocket.StatusNormalClosure)
}

// TestEnvScanEarlyCloseCancels: closing the socket kills the running
// scan's process and frees the one-scan-at-a-time slot.
func TestEnvScanEarlyCloseCancels(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	argv := writeScanStub(t, fmt.Sprintf("touch %q\nsleep 60\n", started))

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	conn := wsDial(t, base, "/ws/envscan", g.Token())

	writeWSJSON(t, conn, map[string]string{"harness": "claude", "mode": localops.ScanModeInventory})
	waitForFile(t, started)
	_ = conn.Close(websocket.StatusNormalClosure, "user canceled")

	// The slot frees only after RunScan returns, which requires the
	// canceled process to die; well under the stub's 60s sleep proves
	// the close killed it.
	deadline := time.Now().Add(8 * time.Second)
	for {
		if g.local.beginScan() {
			g.local.endScan()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("scan slot never freed after the socket closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEnvScanRefusesConcurrentScan: a second scan while one runs is
// refused with a clear error frame and a policy close.
func TestEnvScanRefusesConcurrentScan(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	argv := writeScanStub(t, fmt.Sprintf("touch %q\nsleep 60\n", started))

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	first := wsDial(t, base, "/ws/envscan", g.Token())
	writeWSJSON(t, first, map[string]string{"harness": "claude", "mode": localops.ScanModeInventory})
	waitForFile(t, started)

	second := wsDial(t, base, "/ws/envscan", g.Token())
	writeWSJSON(t, second, map[string]string{"harness": "claude", "mode": localops.ScanModeInventory})
	refusal := readWSJSON[scanFrame](t, second)
	if refusal.Type != "error" || !strings.Contains(refusal.Detail, "already running") {
		t.Fatalf("refusal frame = %+v, want an already-running error", refusal)
	}
	expectClose(t, second, websocket.StatusPolicyViolation)
	_ = first.Close(websocket.StatusNormalClosure, "")
}

// waitForFile polls until the stub's sentinel file exists, proving the
// scan process is running.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sentinel %s never appeared", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
