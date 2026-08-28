package localops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/envdef"
	"github.com/3xDevOps/Aether/internal/harness"
)

// validScanDockerfile and validScanManifest are a minimal pair that passes
// the envdef contract, used by the stub agents below.
const validScanDockerfile = `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends jq
`

const validScanManifest = `[{"name":"jq","version":"1.7.1","reason":"test","start_line":2,"end_line":2,"check_command":"jq --version"}]`

// writeStub writes an executable shell script and returns the argv
// override that runs it with the rendered prompt as its first argument.
func writeStub(t *testing.T, body string) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", script, harness.TaskPlaceholder}
}

// scanRecorder collects progress events; RunScan serializes callbacks so
// no locking is needed here.
type scanRecorder struct {
	statuses []string
	lines    []string
}

func (r *scanRecorder) record(event ScanEvent) {
	if event.Status != "" {
		r.statuses = append(r.statuses, event.Status)
	}
	if event.Line != "" {
		r.lines = append(r.lines, event.Line)
	}
}

// assertGone fails unless every recorded scratch directory was removed.
func assertGone(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if dir == "" {
			t.Fatal("no scratch directory was recorded")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("scratch directory %s still exists (stat err: %v)", dir, err)
		}
	}
}

// readScratch reads the scratch paths a stub recorded, one per attempt.
func readScratch(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read scratch log: %v", err)
	}
	return strings.Fields(string(data))
}

// initGitRepo creates a real git repository for repo-mode tests.
func initGitRepo(t *testing.T) string {
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

// resolvePath follows symlinks so a stub's pwd output compares equal to
// the path the test created (temp dirs are symlinked on some systems).
func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

// repoStubOutputs is the shell fragment repo-mode stubs share: it finds
// the scratch directory the prompt names and writes the valid pair there.
const repoStubOutputs = `out=$(printf '%%s\n' "$1" | grep -o '/[^[:space:]:]*aether-envscan-[^[:space:]:]*' | head -n 1)
cat > "$out/Dockerfile" <<'DOCKEREOF'
%sDOCKEREOF
cat > "$out/manifest.json" <<'MANIFESTEOF'
%s
MANIFESTEOF
`

func TestDetectHarnesses(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"claude", "pi"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	got := DetectHarnesses()
	want := []HarnessStatus{
		{Name: "claude", Installed: true},
		{Name: "codex", Installed: false},
		{Name: "pi", Installed: true},
		{Name: "amp", Installed: false},
	}
	if len(got) != len(want) {
		t.Fatalf("DetectHarnesses returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRunScanSuccess(t *testing.T) {
	log := filepath.Join(t.TempDir(), "scratch.log")
	argv := writeStub(t, fmt.Sprintf(`echo inspecting
pwd >> %q
cat > Dockerfile <<'EOF'
%sEOF
cat > manifest.json <<'EOF'
%s
EOF
`, log, validScanDockerfile, validScanManifest))

	var recorder scanRecorder
	result, err := RunScan(context.Background(), ScanOptions{
		Harness: "claude",
		Mode:    ScanModeInventory,
		Argv:    argv,
	}, recorder.record)
	if err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	if result.Dockerfile != validScanDockerfile {
		t.Errorf("Dockerfile = %q, want %q", result.Dockerfile, validScanDockerfile)
	}
	if len(result.Manifest) != 1 || result.Manifest[0].Name != "jq" {
		t.Errorf("Manifest = %+v, want one jq item", result.Manifest)
	}
	if !strings.Contains(result.ManifestJSON, `"jq"`) {
		t.Errorf("ManifestJSON = %q, want the raw manifest text", result.ManifestJSON)
	}

	wantStatuses := []string{ScanStatusRunning, ScanStatusValidating}
	if fmt.Sprint(recorder.statuses) != fmt.Sprint(wantStatuses) {
		t.Errorf("statuses = %v, want %v", recorder.statuses, wantStatuses)
	}
	found := false
	for _, line := range recorder.lines {
		if line == "inspecting" {
			found = true
		}
	}
	if !found {
		t.Errorf("output lines %v missing the stub's echo", recorder.lines)
	}
	assertGone(t, readScratch(t, log)...)
}

func TestRunScanRefinePrompt(t *testing.T) {
	promptLog := filepath.Join(t.TempDir(), "prompt.log")
	argv := writeStub(t, fmt.Sprintf(`printf '%%s' "$1" > %q
cat > Dockerfile <<'EOF'
%sEOF
cat > manifest.json <<'EOF'
%s
EOF
`, promptLog, validScanDockerfile, validScanManifest))

	_, err := RunScan(context.Background(), ScanOptions{
		Harness:              "claude",
		Mode:                 ScanModeRefine,
		PreviousDockerfile:   validScanDockerfile,
		PreviousManifestJSON: validScanManifest,
		Feedback:             "drop jq and add ripgrep",
		Argv:                 argv,
	}, nil)
	if err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	prompt, readErr := os.ReadFile(promptLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{"drop jq and add ripgrep", validScanManifest, "FROM ubuntu:24.04"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("refine prompt missing %q", want)
		}
	}
}

func TestRunScanRetriesOnceOnInvalidOutput(t *testing.T) {
	dir := t.TempDir()
	scratchLog := filepath.Join(dir, "scratch.log")
	promptDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	argv := writeStub(t, fmt.Sprintf(`pwd >> %q
i=0
while [ -e %q/prompt.$i ]; do i=$((i+1)); done
printf '%%s' "$1" > %q/prompt.$i
printf 'FROM ubuntu:24.04\n' > Dockerfile
printf '[]' > manifest.json
`, scratchLog, promptDir, promptDir))

	var recorder scanRecorder
	_, err := RunScan(context.Background(), ScanOptions{
		Harness: "codex",
		Mode:    ScanModeInventory,
		Argv:    argv,
	}, recorder.record)
	if err == nil {
		t.Fatal("RunScan succeeded on an invalid pair")
	}
	var failure *ScanFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a *ScanFailure", err)
	}

	prompts, globErr := filepath.Glob(filepath.Join(promptDir, "prompt.*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(prompts) != 2 {
		t.Fatalf("stub ran %d times, want exactly 2 (one retry)", len(prompts))
	}
	second, readErr := os.ReadFile(filepath.Join(promptDir, "prompt.1"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(second), "manifest has no items") {
		t.Errorf("retry prompt does not carry the validation error:\n%s", second)
	}

	hasRetrying := false
	for _, s := range recorder.statuses {
		if s == ScanStatusRetrying {
			hasRetrying = true
		}
	}
	if !hasRetrying {
		t.Errorf("statuses %v missing %q", recorder.statuses, ScanStatusRetrying)
	}
	assertGone(t, readScratch(t, scratchLog)...)
}

func TestRunScanNonZeroExitFailsWithoutRetry(t *testing.T) {
	dir := t.TempDir()
	countLog := filepath.Join(dir, "count.log")
	scratchLog := filepath.Join(dir, "scratch.log")
	argv := writeStub(t, fmt.Sprintf(`pwd >> %q
echo boom >> %q
echo "agent broke"
exit 3
`, scratchLog, countLog))

	_, err := RunScan(context.Background(), ScanOptions{
		Harness: "claude",
		Mode:    ScanModeInventory,
		Argv:    argv,
	}, nil)
	if err == nil {
		t.Fatal("RunScan succeeded on a crashing agent")
	}
	var failure *ScanFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a *ScanFailure", err)
	}
	if !strings.Contains(failure.OutputTail, "agent broke") {
		t.Errorf("output tail %q missing the agent's output", failure.OutputTail)
	}
	count, readErr := os.ReadFile(countLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if runs := strings.Count(string(count), "boom"); runs != 1 {
		t.Errorf("crashing agent ran %d times, want 1 (no retry)", runs)
	}
	assertGone(t, readScratch(t, scratchLog)...)
}

func TestRunScanTimeout(t *testing.T) {
	scratchLog := filepath.Join(t.TempDir(), "scratch.log")
	argv := writeStub(t, fmt.Sprintf("pwd >> %q\nsleep 10\n", scratchLog))

	start := time.Now()
	_, err := RunScan(context.Background(), ScanOptions{
		Harness: "claude",
		Mode:    ScanModeInventory,
		Argv:    argv,
		Timeout: 200 * time.Millisecond,
	}, nil)
	if err == nil {
		t.Fatal("RunScan did not report the timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not mention the timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("RunScan took %s; the timeout did not kill the process", elapsed)
	}
	assertGone(t, readScratch(t, scratchLog)...)
}

func TestRunScanFakeHarness(t *testing.T) {
	var recorder scanRecorder
	result, err := RunScan(context.Background(), ScanOptions{
		Harness: "fake",
		Mode:    ScanModeInventory,
	}, recorder.record)
	if err != nil {
		t.Fatalf("RunScan(fake): %v", err)
	}
	if !strings.HasPrefix(result.Dockerfile, "FROM "+envdef.BaseImage) {
		t.Errorf("canned Dockerfile does not start from %s:\n%s", envdef.BaseImage, result.Dockerfile)
	}
	if len(result.Manifest) != 1 {
		t.Fatalf("canned manifest has %d items, want 1", len(result.Manifest))
	}
	item := result.Manifest[0]
	if item.Name == "" || item.CheckCommand == "" {
		t.Errorf("canned item %+v is missing name or check command", item)
	}
	if err := envdef.ValidateDockerfile(result.Dockerfile, result.Manifest); err != nil {
		t.Errorf("canned pair fails its own contract: %v", err)
	}
}

func TestRunScanRejectsUnknownHarness(t *testing.T) {
	if _, err := RunScan(context.Background(), ScanOptions{
		Harness: "opencode",
		Mode:    ScanModeInventory,
	}, nil); err == nil {
		t.Fatal("RunScan accepted a harness outside the setup-capable set")
	}
}

func TestRunScanRepoModeRunsInRepoAndWritesToScratch(t *testing.T) {
	repo := initGitRepo(t)
	cwdLog := filepath.Join(t.TempDir(), "cwd.log")
	argv := writeStub(t, fmt.Sprintf("pwd >> %q\n"+repoStubOutputs,
		cwdLog, validScanDockerfile, validScanManifest))

	result, err := RunScan(context.Background(), ScanOptions{
		Harness:  "claude",
		Mode:     ScanModeRepo,
		RepoPath: repo,
		Argv:     argv,
	}, nil)
	if err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	if result.Dockerfile != validScanDockerfile {
		t.Errorf("Dockerfile = %q, want %q", result.Dockerfile, validScanDockerfile)
	}
	cwds := readScratch(t, cwdLog)
	if len(cwds) != 1 {
		t.Fatalf("stub ran %d times, want 1", len(cwds))
	}
	if got, want := resolvePath(t, cwds[0]), resolvePath(t, repo); got != want {
		t.Errorf("harness ran in %s, want the repository %s", got, want)
	}
	// The repository must hold no scan output: the pair went to scratch.
	for _, name := range []string{"Dockerfile", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(repo, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was written into the repository (stat err: %v)", name, err)
		}
	}
}

func TestRunScanRepoPathValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	noGit := filepath.Join(dir, "plain")
	if err := os.MkdirAll(noGit, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")

	cases := map[string]struct{ path, want string }{
		"empty path":    {"", "needs the repository's folder"},
		"missing":       {missing, "does not exist"},
		"not a folder":  {file, "is not a folder"},
		"no .git entry": {noGit, "is not a git repository"},
	}
	for name, tc := range cases {
		_, err := RunScan(context.Background(), ScanOptions{
			Harness:  "fake",
			Mode:     ScanModeRepo,
			RepoPath: tc.path,
		}, nil)
		if err == nil {
			t.Errorf("%s: RunScan accepted repo path %q", name, tc.path)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not say %q", name, err, tc.want)
		}
		if tc.path != "" && !strings.Contains(err.Error(), tc.path) {
			t.Errorf("%s: error %q does not name the path %q", name, err, tc.path)
		}
	}
}

func TestRunScanRepoModifiedGuard(t *testing.T) {
	repo := initGitRepo(t)
	countLog := filepath.Join(t.TempDir(), "count.log")
	argv := writeStub(t, fmt.Sprintf("echo ran >> %q\ntouch tampered.txt\n"+repoStubOutputs,
		countLog, validScanDockerfile, validScanManifest))

	_, err := RunScan(context.Background(), ScanOptions{
		Harness:  "claude",
		Mode:     ScanModeRepo,
		RepoPath: repo,
		Argv:     argv,
	}, nil)
	if err == nil {
		t.Fatal("RunScan accepted a run that modified the repository")
	}
	if !strings.Contains(err.Error(), "modified the repository") {
		t.Errorf("error %q does not state the repository was modified", err)
	}
	count, readErr := os.ReadFile(countLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if runs := strings.Count(string(count), "ran"); runs != 1 {
		t.Errorf("repo-modifying agent ran %d times, want 1 (no retry)", runs)
	}
}

func TestRunScanRefineInheritsRepoPath(t *testing.T) {
	repo := initGitRepo(t)
	dir := t.TempDir()
	cwdLog := filepath.Join(dir, "cwd.log")
	promptLog := filepath.Join(dir, "prompt.log")
	argv := writeStub(t, fmt.Sprintf("pwd >> %q\nprintf '%%s' \"$1\" > %q\n"+repoStubOutputs,
		cwdLog, promptLog, validScanDockerfile, validScanManifest))

	result, err := RunScan(context.Background(), ScanOptions{
		Harness:              "claude",
		Mode:                 ScanModeRefine,
		PreviousDockerfile:   validScanDockerfile,
		PreviousManifestJSON: validScanManifest,
		Feedback:             "add ripgrep",
		RepoPath:             repo,
		Argv:                 argv,
	}, nil)
	if err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	if result.Dockerfile != validScanDockerfile {
		t.Errorf("Dockerfile = %q, want %q", result.Dockerfile, validScanDockerfile)
	}
	cwds := readScratch(t, cwdLog)
	if len(cwds) != 1 {
		t.Fatalf("stub ran %d times, want 1", len(cwds))
	}
	if got, want := resolvePath(t, cwds[0]), resolvePath(t, repo); got != want {
		t.Errorf("refine ran in %s, want the repository %s", got, want)
	}
	prompt, readErr := os.ReadFile(promptLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{"add ripgrep", "aether-envscan-", "never modify, create, or delete"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("repo-anchored refine prompt missing %q", want)
		}
	}
}

func TestRunScanFakeHarnessRepoMode(t *testing.T) {
	repo := initGitRepo(t)
	result, err := RunScan(context.Background(), ScanOptions{
		Harness:  "fake",
		Mode:     ScanModeRepo,
		RepoPath: repo,
	}, nil)
	if err != nil {
		t.Fatalf("RunScan(fake, repo): %v", err)
	}
	if len(result.Manifest) != 1 {
		t.Fatalf("canned repo manifest has %d items, want 1", len(result.Manifest))
	}
	if result.Manifest[0].Name == "jq" {
		t.Errorf("canned repo pair matches the mirror pair; want a distinct one, got %+v", result.Manifest)
	}
	if err := envdef.ValidateDockerfile(result.Dockerfile, result.Manifest); err != nil {
		t.Errorf("canned repo pair fails its own contract: %v", err)
	}
}
