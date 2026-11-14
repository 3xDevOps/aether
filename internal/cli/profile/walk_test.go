package profile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
)

func carriedBytes(files []LocalFile) int64 {
	var total int64
	for _, f := range files {
		total += int64(len(f.Content))
	}
	return total
}

// shrinkBudget overrides the per-file and per-snapshot caps for a test
// that has to overrun them, so a fixture proving the budget accounting
// does not need megabytes of real content to do it - the scanner's cost
// grows with content size, and a test writing tens of megabytes to disk
// is what made this package slow under -race. It restores the real caps
// on cleanup; a test that calls it must not run with t.Parallel, since
// the caps are package state.
func shrinkBudget(t *testing.T, perFile, total int64) {
	t.Helper()
	prevFile, prevTotal := maxFileBytes, maxTotalBytes
	maxFileBytes, maxTotalBytes = perFile, total
	t.Cleanup(func() { maxFileBytes, maxTotalBytes = prevFile, prevTotal })
}

// TestDiscoverExcludesOversizedFileUnread names the fix for the blocker: a
// file over the server's per-file cap is excluded from its stat alone. The
// proof that it is never read is that its content would fail the scanner -
// the fixture plants a secret in it, and it is still reported as too
// large rather than as a finding.
func TestDiscoverExcludesOversizedFileUnread(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// A file over the cap whose content would trip the scanner outright.
	big := append([]byte(nil), fixture...)
	big = append(big, make([]byte, profilesvc.MaxFileBytes)...)
	mustWrite(t, filepath.Join(root, "notes", "huge.jsonl"), string(big))

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("DiscoverFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "settings.json" {
		t.Errorf("discovered %d files, want only settings.json", len(files))
	}
	if len(skipped) != 1 || skipped[0].Path != "notes/huge.jsonl" || skipped[0].Reason != ExcludeTooLarge {
		t.Fatalf("skipped = %+v, want notes/huge.jsonl as %s", skipped, ExcludeTooLarge)
	}
	if !strings.Contains(skipped[0].Detail, "over the") {
		t.Errorf("detail does not name the limit: %q", skipped[0].Detail)
	}
}

// TestDiscoverStopsAtTotalBudget proves the walk stops carrying files
// once the per-snapshot cap is reached, so a push never carries more than
// the server accepts.
func TestDiscoverStopsAtTotalBudget(t *testing.T) {
	shrinkBudget(t, 4096, 81920)
	root := setupClaudeRoot(t)
	// Just under the per-file cap each, so the total cap is what bites.
	body := strings.Repeat("x", int(maxFileBytes)-1)
	for i := range 24 {
		mustWrite(t, filepath.Join(root, "skills", fmt.Sprintf("s%02d.md", i)), body)
	}

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if total := carriedBytes(files); total > maxTotalBytes {
		t.Fatalf("push carries %d bytes, over the %d-byte snapshot cap", total, maxTotalBytes)
	}
	over := 0
	for _, e := range skipped {
		if e.Reason == ExcludeOverBudget {
			over++
		}
	}
	if over == 0 {
		t.Fatalf("nothing reported as over budget; skipped = %+v", skipped)
	}
	if len(files)+over != 24 {
		t.Errorf("files %d + over-budget %d != 24", len(files), over)
	}
}

// TestDiscoverHonorsContextCancellation covers the other half of the
// blocker: a caller that has gone away must be able to stop a walk of the
// user's home directory rather than leave it running.
func TestDiscoverHonorsContextCancellation(t *testing.T) {
	root := setupClaudeRoot(t)
	for i := range 50 {
		mustWrite(t, filepath.Join(root, "skills", fmt.Sprintf("s%02d.md", i)), "body")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, _, err := DiscoverFiles(ctx, "claude", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverFiles error = %v, want context.Canceled", err)
	}
}

// TestDiscoverSpendsBudgetByPriority covers the starvation the reviewer
// measured on a real ~/.claude: WalkDir visits lexically, so plugins/ and
// other/ sorted first and spent the whole 20 MiB, leaving zero skills and
// zero commands - exactly the configuration this feature exists to carry.
// The fixture reproduces that ordering: "plugins" and "zz-other" bracket
// "skills" and "commands" alphabetically, and together they overrun the
// budget.
func TestDiscoverSpendsBudgetByPriority(t *testing.T) {
	shrinkBudget(t, 4096, 81920)
	root := setupClaudeRoot(t)
	// Just under the per-file cap each, so the snapshot cap is what bites.
	body := strings.Repeat("x", int(maxFileBytes)-1)
	// 15 files of plugin cache, sorting before "skills".
	for i := range 15 {
		mustWrite(t, filepath.Join(root, "plugins", fmt.Sprintf("p%02d.json", i)), body)
	}
	// 15 files of loose content, sorting after "skills" but before
	// nothing that matters - "other" is last by priority either way.
	for i := range 15 {
		mustWrite(t, filepath.Join(root, "aaa-notes", fmt.Sprintf("n%02d.txt", i)), body)
	}
	mustWrite(t, filepath.Join(root, "skills", "pdf", "SKILL.md"), "# pdf skill\n")
	mustWrite(t, filepath.Join(root, "commands", "review.md"), "# review\n")
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# standing instructions\n")

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := names(files)
	for _, want := range []string{"CLAUDE.md", "skills/pdf/SKILL.md", "commands/review.md"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s did not survive the budget; carried = %v", want, slices.Sorted(maps.Keys(got)))
		}
	}
	if total := carriedBytes(files); total > maxTotalBytes {
		t.Errorf("push carries %d bytes, over the cap", total)
	}
	// The bulk categories are what gets cut, not the configuration.
	over := 0
	for _, e := range skipped {
		if e.Reason == ExcludeOverBudget {
			over++
			if strings.HasPrefix(e.Path, "skills/") || strings.HasPrefix(e.Path, "commands/") {
				t.Errorf("%s was dropped for budget ahead of bulk files", e.Path)
			}
		}
	}
	if over == 0 {
		t.Fatal("the fixture did not overrun the budget; the test proves nothing")
	}
}

// TestDiscoverChargesOnlyCarriedFilesToBudget pins that a file the
// scanner flagged does not spend budget it was dropped from. A flagged
// file read at a higher priority used to charge its bytes anyway, so a
// clean file further down was reported over-budget while the snapshot
// still had room for every byte of it.
func TestDiscoverChargesOnlyCarriedFilesToBudget(t *testing.T) {
	shrinkBudget(t, 4096, 81920)
	root := setupClaudeRoot(t)
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Just under the per-file cap each, so the snapshot cap is what bites.
	size := maxFileBytes - 1
	// Sorts first inside skills/, so it is read before the clean files.
	mustWrite(t, filepath.Join(root, "skills", "aaa-leak.md"),
		string(secret)+strings.Repeat("x", int(size)-len(secret)))
	// Exactly as many clean files as the snapshot cap holds: charging the
	// flagged file too costs the last one its place.
	clean := maxTotalBytes / size
	body := strings.Repeat("x", int(size))
	for i := range clean {
		mustWrite(t, filepath.Join(root, "skills", fmt.Sprintf("s%02d.md", i)), body)
	}

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range skipped {
		if e.Reason == ExcludeOverBudget {
			t.Errorf("%s reported over-budget behind a dropped file: %s", e.Path, e.Detail)
		}
	}
	if len(files) != int(clean) {
		t.Errorf("carried %d files, want %d clean skills", len(files), clean)
	}
	if total := carriedBytes(files); total != clean*size {
		t.Errorf("push carries %d bytes, want %d", total, clean*size)
	}
	if len(skipped) != 1 || skipped[0].Path != "skills/aaa-leak.md" || skipped[0].Reason != ExcludeSecret {
		t.Fatalf("skipped = %+v, want only the flagged file", skipped)
	}
}

// TestDiscoverDefaultIgnoresTransientDirs pins the second half of the
// same fix: claude's run-time directories are dropped by default, and the
// user's own ignore file can take them back.
func TestDiscoverDefaultIgnoresTransientDirs(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# standing instructions\n")
	for _, dir := range []string{"projects", "shell-snapshots", "statsig", "todos"} {
		mustWrite(t, filepath.Join(root, dir, "a.jsonl"), "{}\n")
		mustWrite(t, filepath.Join(root, dir, "b.jsonl"), "{}\n")
	}

	files, _, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("files = %d, want only CLAUDE.md", len(files))
	}

	// The user's own file has the last word, gitignore-style.
	mustWrite(t, filepath.Join(root, IgnoreFileName), "!projects/\n")
	files, _, err = DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("files = %d, want CLAUDE.md plus the two re-included files", len(files))
	}
}

// setupHarnessRoot is setupClaudeRoot for any harness the registry knows.
func setupHarnessRoot(t *testing.T, harnessName string) string {
	t.Helper()
	home := t.TempDir()
	userHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHome = os.UserHomeDir })
	p, ok := harness.Lookup(harnessName)
	if !ok {
		t.Fatalf("%s harness missing", harnessName)
	}
	root := filepath.Join(home, filepath.FromSlash(p.LocalRoot))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestDiscoverCodexScratchTreeIgnored covers the last thing standing
// between codex and an import: codex plants a symlink to its own
// installed binary under tmp/ on every run, which escapes the profile
// root. The scratch tree is a default ignore, so the walk never reaches
// the link at all.
func TestDiscoverCodexScratchTreeIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs SeCreateSymbolicLinkPrivilege: Developer Mode or an elevated shell")
	}
	root := setupHarnessRoot(t, "codex")
	mustWrite(t, filepath.Join(root, "config.toml"), "model = \"o3\"\n")
	mustWrite(t, filepath.Join(root, "skills", "pdf", "SKILL.md"), "# pdf skill\n")
	// The real shape: ~/.codex/tmp/arg0/codex-arg0XXXXXX/apply_patch is a
	// link out to the codex binary wherever it happens to be installed.
	scratch := filepath.Join(root, "tmp", "arg0", "codex-arg0DKkhdr")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "codex")
	mustWrite(t, binary, "#!/bin/sh\n")
	if err := os.Symlink(binary, filepath.Join(scratch, "apply_patch")); err != nil {
		t.Fatal(err)
	}

	files, skipped, err := DiscoverFiles(t.Context(), "codex", nil)
	if err != nil {
		t.Fatalf("codex could not be pushed: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("discovered %d files, want config.toml and the skill", len(files))
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want the link never reached", skipped)
	}
}

// TestDiscoverClaudeRuntimeFilesIgnored pins the rest of claude's
// run-time output: a history log and the daemon's own state are not
// configuration another machine wants.
func TestDiscoverClaudeRuntimeFilesIgnored(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# standing instructions\n")
	mustWrite(t, filepath.Join(root, "history.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(root, "file-history", "a.json"), "{}\n")
	mustWrite(t, filepath.Join(root, "daemon", "roster.json"), "{}\n")

	files, _, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("files = %v, want only CLAUDE.md", slices.Sorted(maps.Keys(names(files))))
	}
}

// TestDiscoverCodexSessionsIgnored covers codex's transcript archive,
// which is the same thing claude keeps in projects/: a per-session log of
// what the agent did, not configuration another machine wants. Left in,
// it spent the snapshot budget and tripped the secret scanner on content
// nobody wrote by hand, blocking the whole import.
func TestDiscoverCodexSessionsIgnored(t *testing.T) {
	root := setupHarnessRoot(t, "codex")
	mustWrite(t, filepath.Join(root, "config.toml"), "model = \"o3\"\n")
	mustWrite(t, filepath.Join(root, "skills", "pdf", "SKILL.md"), "# pdf skill\n")
	// The real shape: ~/.codex/sessions/<year>/<month>/<day>/rollout-*.jsonl.
	day := filepath.Join(root, "sessions", "2026", "02", "17")
	mustWrite(t, filepath.Join(day, "rollout-2026-02-17T19-39-47.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(day, "rollout-2026-02-17T20-11-02.jsonl"), "{}\n")

	files, _, err := DiscoverFiles(t.Context(), "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("files = %d, want config.toml and the skill", len(files))
	}
	// The user's own ignore file still has the last word.
	mustWrite(t, filepath.Join(root, IgnoreFileName), "!sessions/\n")
	files, _, err = DiscoverFiles(t.Context(), "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Errorf("files = %d, want the two transcripts re-included", len(files))
	}
}

// pi writes its transcripts and its extension downloads inside the same
// agent directory that holds everything a member configured, so the
// defaults have to cut two subtrees out of it and leave the rest.
func TestDiscoverPiRuntimeIgnored(t *testing.T) {
	root := setupHarnessRoot(t, "pi")
	mustWrite(t, filepath.Join(root, "agent", "config.yml"), "model: sonnet\n")
	mustWrite(t, filepath.Join(root, "agent", "npm", "package.json"), "{}\n")
	mustWrite(t, filepath.Join(root, "agent", "sessions", "-code", "01.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(root, "agent", "tmp", "extensions", "pkg", "index.js"), "//\n")

	files, skipped, err := DiscoverFiles(t.Context(), "pi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want config.yml and the installed package", slices.Sorted(maps.Keys(names(files))))
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want only the default ignores", skipped)
	}
}

// TestDiscoverOmpRuntimeIgnored covers the harness whose profile root is
// the largest Aether syncs. A real omp home is half a gigabyte, and all
// but a few megabytes of it is the agent's own output: transcripts under
// agent/sessions/, the prompt history it rewrites on every prompt, and a
// per-version native download under natives/. Left in, the budget walk
// spends the whole per-snapshot cap on it and silently drops the
// configuration the member actually wanted on the server.
func TestDiscoverOmpRuntimeIgnored(t *testing.T) {
	root := setupHarnessRoot(t, "omp")
	want := map[string]string{
		"agent/config.yml":                              "model: sonnet\n",
		"agent/extensions/mine.ts":                      "export default () => {}\n",
		"agent/extensions/stats.db":                     "extension settings\n",
		"agent/extensions/stats.db-wal":                 "extension WAL\n",
		"agent/extensions/stats.db-shm":                 "extension SHM\n",
		"agent/npm/node_modules/puppeteer/package.json": "{}\n",
	}
	for rel, body := range want {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), body)
	}
	mustWrite(t, filepath.Join(root, "agent", "sessions", "-code", "01.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(root, "agent", "terminal-sessions", "01.json"), "{}\n")
	mustWrite(t, filepath.Join(root, "agent", "history.db"), "sqlite\n")
	mustWrite(t, filepath.Join(root, "agent", "history.db-wal"), "\n")
	mustWrite(t, filepath.Join(root, "agent", "models.db"), "sqlite\n")
	mustWrite(t, filepath.Join(root, "natives", "18.1.4", "node"), "binary\n")
	mustWrite(t, filepath.Join(root, "collab", "abc.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(root, "logs", "omp.log"), "hello\n")
	for _, name := range []string{"stats.db", "stats.db-wal", "stats.db-shm"} {
		mustWrite(t, filepath.Join(root, name), "runtime\n")
	}
	mustWrite(t, filepath.Join(root, "puppeteer", "browser"), "binary\n")

	checkPaths := func() {
		t.Helper()
		files, skipped, err := DiscoverFiles(t.Context(), "omp", nil)
		if err != nil {
			t.Fatal(err)
		}
		gotPaths := slices.Sorted(maps.Keys(names(files)))
		wantPaths := slices.Sorted(maps.Keys(want))
		if !slices.Equal(gotPaths, wantPaths) {
			t.Fatalf("discovered paths = %v, want %v", gotPaths, wantPaths)
		}
		if len(skipped) != 0 {
			t.Errorf("skipped = %+v, want only the default ignores", skipped)
		}
	}
	checkPaths()

	mustWrite(t, filepath.Join(root, IgnoreFileName), "stats.db\n!/stats.db\n!/puppeteer/\n")
	delete(want, "agent/extensions/stats.db")
	want["stats.db"] = "runtime\n"
	want["puppeteer/browser"] = "binary\n"
	checkPaths()
}

// TestDiscoverCarriesRegularFiles is the other side of the mode check,
// and it runs everywhere: an ordinary file must not be mistaken for one
// of the types the walk refuses.
func TestDiscoverCarriesRegularFiles(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "skills", "pdf", "SKILL.md"), "# pdf skill\n")

	files, _, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want both regular files", len(files))
	}
}
