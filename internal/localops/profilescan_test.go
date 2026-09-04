package localops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli/profile"
)

// validProfileJSON answers profileTestPreviews within the contract: one
// entry per harness shown, categories drawn from that harness's own
// inventory, one-line reasons.
const validProfileJSON = `{"harnesses":[
  {"harness":"claude","import":true,"categories":["memory","skills"],
   "reason":"skills/ holds two hand-written skills and CLAUDE.md carries standing instructions."},
  {"harness":"codex","import":false,"categories":[],
   "reason":"config.toml is the only file and it holds defaults."}
]}`

// invalidProfileJSON names a category no inventory has, which is the kind
// of contract violation that earns the one retry.
const invalidProfileJSON = `{"harnesses":[{"harness":"claude","import":true,"categories":["nope"],"reason":"guessing."}]}`

// profileStubOutput is the shell fragment profile stubs share: it finds
// the scratch directory the prompt names and writes profile.json there.
const profileStubOutput = `out=$(printf '%%s\n' "$1" | grep -o '/[^[:space:]:]*aether-envscan-[^[:space:]:]*' | head -n 1)
cat > "$out/profile.json" <<'PROFILEEOF'
%s
PROFILEEOF
`

// profileTestPreviews is the inventory the profile tests show the agent:
// two present harnesses, and one excluded credential file that must never
// reach the prompt.
func profileTestPreviews() []profile.Preview {
	return []profile.Preview{
		{
			Harness: "claude",
			Root:    "/home/dev/.claude",
			Present: true,
			Files:   3,
			Bytes:   120,
			Categories: []profile.Category{
				{Name: profile.CategoryMemory, Files: 1, Bytes: 40, Paths: []string{"CLAUDE.md"}},
				{Name: profile.CategorySkills, Files: 2, Bytes: 80, Paths: []string{"skills/review/SKILL.md", "skills/ship/SKILL.md"}},
			},
			Excluded: []profile.Exclusion{{Path: "secret-token.json", Reason: profile.ExcludeCredential}},
		},
		{
			Harness: "codex",
			Root:    "/home/dev/.codex",
			Present: true,
			Files:   1,
			Bytes:   10,
			Categories: []profile.Category{
				{Name: profile.CategorySettings, Files: 1, Bytes: 10, Paths: []string{"config.toml"}},
			},
		},
	}
}

func TestRunProfileScanSuccess(t *testing.T) {
	dir := t.TempDir()
	promptLog := filepath.Join(dir, "prompt.log")
	scratchLog := filepath.Join(dir, "scratch.log")
	argv := writeStub(t, fmt.Sprintf(`echo reading inventory
pwd >> %q
printf '%%s' "$1" > %q
`+profileStubOutput, scratchLog, promptLog, validProfileJSON))

	var recorder scanRecorder
	result, err := RunProfileScan(context.Background(), ProfileScanOptions{
		Harness:   "claude",
		Inventory: profileTestPreviews(),
		Argv:      argv,
	}, recorder.record)
	if err != nil {
		t.Fatalf("RunProfileScan: %v", err)
	}
	if len(result.Recommendation.Harnesses) != 2 {
		t.Fatalf("recommendation has %d harnesses, want 2: %+v", len(result.Recommendation.Harnesses), result.Recommendation)
	}
	first := result.Recommendation.Harnesses[0]
	if first.Harness != "claude" || !first.Import {
		t.Errorf("first entry = %+v, want claude imported", first)
	}
	if !strings.Contains(result.RecommendationJSON, `"skills"`) {
		t.Errorf("RecommendationJSON = %q, want the raw file text", result.RecommendationJSON)
	}

	wantStatuses := []string{ScanStatusRunning, ScanStatusValidating}
	if fmt.Sprint(recorder.statuses) != fmt.Sprint(wantStatuses) {
		t.Errorf("statuses = %v, want %v", recorder.statuses, wantStatuses)
	}

	prompt, readErr := os.ReadFile(promptLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{
		"harness: claude", "/home/dev/.claude", "category skills: 2 files, 80 bytes",
		"skills/review/SKILL.md", "harness: codex", "profile.json",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("profile prompt missing %q", want)
		}
	}
	// The excluded entry names a credential file; it must never reach the
	// agent, and neither must a repository the run was not given.
	if strings.Contains(string(prompt), "secret-token.json") {
		t.Errorf("profile prompt leaked an excluded file:\n%s", prompt)
	}
	if !strings.Contains(string(prompt), "No repository was given") {
		t.Errorf("profile prompt without a repository does not say so:\n%s", prompt)
	}
	assertGone(t, readScratch(t, scratchLog)...)
}

func TestRunProfileScanRetriesOnceOnInvalidOutput(t *testing.T) {
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
echo "agent guessed"
`+profileStubOutput, scratchLog, promptDir, promptDir, invalidProfileJSON))

	var recorder scanRecorder
	_, err := RunProfileScan(context.Background(), ProfileScanOptions{
		Harness:   "codex",
		Inventory: profileTestPreviews(),
		Argv:      argv,
	}, recorder.record)
	if err == nil {
		t.Fatal("RunProfileScan succeeded on an invalid recommendation")
	}
	var failure *ScanFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a *ScanFailure", err)
	}
	if !strings.Contains(failure.OutputTail, "agent guessed") {
		t.Errorf("output tail %q missing the agent's output", failure.OutputTail)
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
	for _, want := range []string{"is not one this machine has", "write profile.json again"} {
		if !strings.Contains(string(second), want) {
			t.Errorf("retry prompt missing %q:\n%s", want, second)
		}
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

// TestRunProfileScanRepoAnchored: the agent runs inside the repository,
// the prompt names it, and the recommendation still lands in scratch.
func TestRunProfileScanRepoAnchored(t *testing.T) {
	repo := initGitRepo(t)
	dir := t.TempDir()
	cwdLog := filepath.Join(dir, "cwd.log")
	promptLog := filepath.Join(dir, "prompt.log")
	argv := writeStub(t, fmt.Sprintf(`pwd >> %q
printf '%%s' "$1" > %q
`+profileStubOutput, cwdLog, promptLog, validProfileJSON))

	result, err := RunProfileScan(context.Background(), ProfileScanOptions{
		Harness:   "claude",
		RepoPath:  repo,
		Inventory: profileTestPreviews(),
		Argv:      argv,
	}, nil)
	if err != nil {
		t.Fatalf("RunProfileScan: %v", err)
	}
	if len(result.Recommendation.Harnesses) != 2 {
		t.Fatalf("recommendation has %d harnesses, want 2", len(result.Recommendation.Harnesses))
	}
	cwds := readScratch(t, cwdLog)
	if len(cwds) != 1 {
		t.Fatalf("stub ran %d times, want 1", len(cwds))
	}
	if got, want := resolvePath(t, cwds[0]), resolvePath(t, repo); got != want {
		t.Errorf("harness ran in %s, want the repository %s", got, want)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "profile.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("profile.json was written into the repository (stat err: %v)", statErr)
	}
	prompt, readErr := os.ReadFile(promptLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(prompt), repo) {
		t.Errorf("repo-anchored profile prompt does not name the repository %s", repo)
	}
	if strings.Contains(string(prompt), "the current directory") {
		t.Errorf("repo-anchored profile prompt points at the current directory:\n%s", prompt)
	}
}

func TestRunProfileScanRepoModifiedGuard(t *testing.T) {
	repo := initGitRepo(t)
	countLog := filepath.Join(t.TempDir(), "count.log")
	argv := writeStub(t, fmt.Sprintf(`echo ran >> %q
touch tampered.txt
`+profileStubOutput, countLog, validProfileJSON))

	result, err := RunProfileScan(context.Background(), ProfileScanOptions{
		Harness:   "claude",
		RepoPath:  repo,
		Inventory: profileTestPreviews(),
		Argv:      argv,
	}, nil)
	if err == nil {
		t.Fatal("RunProfileScan accepted a run that modified the repository")
	}
	if result != nil {
		t.Errorf("result = %+v, want the output discarded", result)
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

func TestRunProfileScanCancel(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	argv := writeStub(t, fmt.Sprintf("touch %q\nsleep 60\n", started))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunProfileScan(ctx, ProfileScanOptions{
			Harness:   "claude",
			Inventory: profileTestPreviews(),
			Argv:      argv,
		}, nil)
		done <- err
	}()

	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the stub never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunProfileScan succeeded after cancellation")
		}
		if !strings.Contains(err.Error(), "canceled") {
			t.Errorf("error %q does not report the cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not kill the scan process")
	}
}

func TestRunProfileScanFakeHarness(t *testing.T) {
	previews := profileTestPreviews()
	var recorder scanRecorder
	result, err := RunProfileScan(context.Background(), ProfileScanOptions{
		Harness:   "fake",
		Inventory: previews,
	}, recorder.record)
	if err != nil {
		t.Fatalf("RunProfileScan(fake): %v", err)
	}
	if len(result.Recommendation.Harnesses) != len(previews) {
		t.Fatalf("canned recommendation has %d harnesses, want %d", len(result.Recommendation.Harnesses), len(previews))
	}
	// The canned answer must satisfy the same contract a real one does.
	if _, err := profile.ParseRecommendation([]byte(result.RecommendationJSON), previews); err != nil {
		t.Errorf("canned recommendation fails its own contract: %v", err)
	}
	wantStatuses := []string{ScanStatusRunning, ScanStatusValidating}
	if fmt.Sprint(recorder.statuses) != fmt.Sprint(wantStatuses) {
		t.Errorf("statuses = %v, want %v", recorder.statuses, wantStatuses)
	}
}

func TestRunProfileScanRequiresInventory(t *testing.T) {
	if _, err := RunProfileScan(context.Background(), ProfileScanOptions{Harness: "fake"}, nil); err == nil {
		t.Fatal("RunProfileScan accepted an empty inventory")
	}
}

func TestRunProfileScanRepoPathValidation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := RunProfileScan(context.Background(), ProfileScanOptions{
		Harness:   "fake",
		RepoPath:  missing,
		Inventory: profileTestPreviews(),
	}, nil)
	if err == nil {
		t.Fatalf("RunProfileScan accepted the missing folder %s", missing)
	}
	if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the missing folder", err)
	}
}
