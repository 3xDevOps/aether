package localgw

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

// scanRepoStubOutputs is the shell fragment a repo-mode stub runs: it
// finds the scratch directory the prompt names and writes the valid pair
// there, mirroring the localops repo-mode tests.
const scanRepoStubOutputs = `out=$(printf '%%s\n' "$1" | grep -o '/[^[:space:]:]*aether-envscan-[^[:space:]:]*' | head -n 1)
cat > "$out/Dockerfile" <<'DOCKEREOF'
%sDOCKEREOF
cat > "$out/manifest.json" <<'MANIFESTEOF'
%s
MANIFESTEOF
`

// initScanGitRepo creates a real git repository for repo-mode tests.
func initScanGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
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

// TestLocalEnvHarnessesRepoSuggestion: the verb suggests the one
// repository folder the saved link config knows, so the wizard can
// prefill the from-repo folder input; several distinct folders or none
// mean no safe guess and the key is omitted.
func TestLocalEnvHarnessesRepoSuggestion(t *testing.T) {
	cases := map[string]struct {
		cfg  cli.Config
		want string
	}{
		"linked repo": {
			cfg:  cli.Config{Repo: "/src/repo"},
			want: "/src/repo",
		},
		"same repo across profiles": {
			cfg: cli.Config{Repo: "/src/repo", Links: []cli.NamedLink{
				{Name: "prod", Repo: "/src/repo"},
				{Name: "staging"},
			}},
			want: "/src/repo",
		},
		"profile-only repo": {
			cfg: cli.Config{Links: []cli.NamedLink{
				{Name: "prod", Repo: "/src/repo"},
			}},
			want: "/src/repo",
		},
		"no repo known": {
			cfg:  cli.Config{},
			want: "",
		},
		"distinct repos": {
			cfg: cli.Config{Repo: "/src/one", Links: []cli.NamedLink{
				{Name: "prod", Repo: "/src/two"},
			}},
			want: "",
		},
	}
	for name, tc := range cases {
		g := newVerbGateway(t, &verbStubBackend{}, tc.cfg)
		rec := do(g, http.MethodPost, "/local/v1/env.harnesses", "{}", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: env.harnesses = %d: %s", name, rec.Code, rec.Body)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		raw, present := got["repo_path"]
		if tc.want == "" {
			if present {
				t.Errorf("%s: repo_path = %s, want the key omitted", name, raw)
			}
			continue
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			t.Fatalf("%s: repo_path = %s: %v", name, raw, err)
		}
		if path != tc.want {
			t.Errorf("%s: repo_path = %q, want %q", name, path, tc.want)
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

// TestEnvScanRepoModeStreamsStubScan: a repo-mode start frame carries the
// repository folder, the stub runs inside it while writing to the scratch
// directory, and the socket streams to one result frame.
func TestEnvScanRepoModeStreamsStubScan(t *testing.T) {
	repo := initScanGitRepo(t)
	cwdLog := filepath.Join(t.TempDir(), "cwd.log")
	argv := writeScanStub(t, fmt.Sprintf("pwd >> %q\n"+scanRepoStubOutputs,
		cwdLog, envScanTestDockerfile, envScanTestManifest))

	g, base := newWSGateway(t, &wsStubBackend{})
	g.local.setScanArgv(argv)
	conn := wsDial(t, base, "/ws/envscan", g.Token())

	writeWSJSON(t, conn, map[string]string{
		"harness":   "claude",
		"mode":      localops.ScanModeRepo,
		"repo_path": repo,
	})
	terminal, statuses, _ := readUntilTerminal(t, conn)
	if terminal.Type != "result" {
		t.Fatalf("terminal frame = %+v, want result", terminal)
	}
	if terminal.Dockerfile != envScanTestDockerfile {
		t.Errorf("dockerfile = %q, want %q", terminal.Dockerfile, envScanTestDockerfile)
	}
	wantStatuses := []string{"detecting", "running", "validating"}
	if fmt.Sprint(statuses) != fmt.Sprint(wantStatuses) {
		t.Errorf("statuses = %v, want %v", statuses, wantStatuses)
	}
	cwd, err := os.ReadFile(cwdLog)
	if err != nil {
		t.Fatalf("read cwd log: %v", err)
	}
	got, gotErr := filepath.EvalSymlinks(strings.TrimSpace(string(cwd)))
	want, wantErr := filepath.EvalSymlinks(repo)
	if gotErr != nil || wantErr != nil {
		t.Fatalf("resolve paths: %v, %v", gotErr, wantErr)
	}
	if got != want {
		t.Errorf("harness ran in %s, want the repository %s", got, want)
	}
	// The repository must hold no scan output: the pair went to scratch.
	for _, name := range []string{"Dockerfile", "manifest.json"} {
		if _, statErr := os.Stat(filepath.Join(repo, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s was written into the repository (stat err: %v)", name, statErr)
		}
	}
	expectClose(t, conn, websocket.StatusNormalClosure)
}

// TestEnvScanRepoPathErrorFrame: a repo-mode start frame with a missing
// or invalid repository folder answers one error frame naming the
// problem and closes cleanly instead of dropping the socket.
func TestEnvScanRepoPathErrorFrame(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cases := map[string]struct{ path, want string }{
		"missing repo_path": {"", "needs the repository's folder"},
		"folder not there":  {missing, "does not exist"},
	}
	for name, tc := range cases {
		g, base := newWSGateway(t, &wsStubBackend{})
		g.local.setScanArgv(writeScanStub(t, "exit 0\n"))
		conn := wsDial(t, base, "/ws/envscan", g.Token())

		writeWSJSON(t, conn, map[string]string{
			"harness":   "claude",
			"mode":      localops.ScanModeRepo,
			"repo_path": tc.path,
		})
		terminal, _, _ := readUntilTerminal(t, conn)
		if terminal.Type != "error" {
			t.Fatalf("%s: terminal frame = %+v, want error", name, terminal)
		}
		if !strings.Contains(terminal.Detail, tc.want) {
			t.Errorf("%s: detail = %q, want it to say %q", name, terminal.Detail, tc.want)
		}
		expectClose(t, conn, websocket.StatusNormalClosure)
	}
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
