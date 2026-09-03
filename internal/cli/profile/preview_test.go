package profile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
)

// writeAll writes every profile-relative path in files under root and
// returns their total size, which is what a preview must report.
func writeAll(t *testing.T, root string, files map[string]string) int64 {
	t.Helper()
	var total int64
	for rel, body := range files {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), body)
		total += int64(len(body))
	}
	return total
}

// categoriesByName indexes a preview's categories for per-group asserts.
func categoriesByName(p Preview) map[string]Category {
	out := map[string]Category{}
	for _, c := range p.Categories {
		out[c.Name] = c
	}
	return out
}

func TestInventoryGroupsFilesByCategory(t *testing.T) {
	root := setupClaudeRoot(t)
	files := map[string]string{
		"CLAUDE.md":             "# standing instructions\n",
		"memory/notes.md":       "remember the build command\n",
		"skills/pdf/SKILL.md":   "# pdf skill\n",
		"commands/review.md":    "# review\n",
		"settings.json":         `{"model":"opus"}`,
		".mcp.json":             `{"servers":{}}`,
		"plugins/x/plugin.json": `{"name":"x"}`,
		"notes.txt":             "loose file\n",
	}
	total := writeAll(t, root, files)

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Harness != "claude" || preview.Root != root || !preview.Present {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.Files != len(files) || preview.Bytes != total {
		t.Fatalf("files/bytes = %d/%d, want %d/%d", preview.Files, preview.Bytes, len(files), total)
	}
	if len(preview.Excluded) != 0 || preview.Blocked {
		t.Fatalf("clean profile reports exclusions: %+v blocked=%v", preview.Excluded, preview.Blocked)
	}

	wantOrder := []string{
		CategoryMemory, CategorySkills, CategoryCommands,
		CategorySettings, CategoryMCP, CategoryPlugins, CategoryOther,
	}
	if got := preview.CategoryNames(); !slices.Equal(got, wantOrder) {
		t.Fatalf("category order = %v, want %v", got, wantOrder)
	}

	byName := categoriesByName(preview)
	for _, tc := range []struct {
		category string
		paths    []string
	}{
		{CategoryMemory, []string{"CLAUDE.md", "memory/notes.md"}},
		{CategorySkills, []string{"skills/pdf/SKILL.md"}},
		{CategoryCommands, []string{"commands/review.md"}},
		{CategorySettings, []string{"settings.json"}},
		{CategoryMCP, []string{".mcp.json"}},
		{CategoryPlugins, []string{"plugins/x/plugin.json"}},
		{CategoryOther, []string{"notes.txt"}},
	} {
		got := byName[tc.category]
		if !slices.Equal(got.Paths, tc.paths) {
			t.Errorf("%s paths = %v, want %v", tc.category, got.Paths, tc.paths)
		}
		if got.Files != len(tc.paths) {
			t.Errorf("%s files = %d, want %d", tc.category, got.Files, len(tc.paths))
		}
		var want int64
		for _, p := range tc.paths {
			want += int64(len(files[p]))
		}
		if got.Bytes != want {
			t.Errorf("%s bytes = %d, want %d", tc.category, got.Bytes, want)
		}
		if got.Truncated {
			t.Errorf("%s reports truncation for %d paths", tc.category, got.Files)
		}
	}
}

// A preview never aborts: unlike Discover, a scanner finding is one more
// reported exclusion, because the point is to show the user every file
// they have to fix and everything else that would still go.
func TestInventoryReportsExclusionsWithoutAborting(t *testing.T) {
	root := setupClaudeRoot(t)
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	writeAll(t, root, map[string]string{
		"settings.json":     `{"model":"opus"}`,
		".credentials.json": `{"token":"x"}`,
		"noise.log":         "log line\n",
		"memory/leak.md":    string(secret),
		IgnoreFileName:      "*.log\n",
	})

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Blocked {
		t.Error("a scanner finding must block the push")
	}
	if preview.Files != 1 || preview.Bytes != int64(len(`{"model":"opus"}`)) {
		t.Fatalf("files/bytes = %d/%d, want the one clean file", preview.Files, preview.Bytes)
	}
	if names := preview.CategoryNames(); !slices.Equal(names, []string{CategorySettings}) {
		t.Fatalf("categories = %v, want just settings", names)
	}

	byPath := map[string]Exclusion{}
	var paths []string
	for _, e := range preview.Excluded {
		byPath[e.Path] = e
		paths = append(paths, e.Path)
	}
	if want := []string{".credentials.json", "memory/leak.md", "noise.log"}; !slices.Equal(paths, want) {
		t.Fatalf("excluded = %v, want %v", paths, want)
	}
	if got := byPath[".credentials.json"]; got.Reason != ExcludeCredential || !strings.Contains(got.Detail, "claude") {
		t.Errorf("credential exclusion = %+v", got)
	}
	if got := byPath["noise.log"]; got.Reason != ExcludeIgnored || got.Detail != IgnoreFileName {
		t.Errorf("ignored exclusion = %+v", got)
	}
	leak := byPath["memory/leak.md"]
	if leak.Reason != ExcludeSecret || !strings.HasPrefix(leak.Detail, "secret detected (") {
		t.Fatalf("secret exclusion = %+v", leak)
	}
	if !strings.Contains(leak.Detail, " at ") {
		t.Errorf("secret detail names no location: %q", leak.Detail)
	}
}

// A harness the user does not run on this machine is a normal answer.
func TestInventoryMissingRootIsPresentFalse(t *testing.T) {
	root := setupClaudeRoot(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatalf("missing root must not be an error: %v", err)
	}
	if preview.Present || preview.Files != 0 || preview.Bytes != 0 || preview.Blocked {
		t.Fatalf("preview = %+v", preview)
	}
	if len(preview.Categories) != 0 || len(preview.Excluded) != 0 {
		t.Fatalf("categories = %v excluded = %v", preview.Categories, preview.Excluded)
	}
	if preview.Root != root {
		t.Errorf("root = %q, want %q", preview.Root, root)
	}
}

func TestInventoryReportsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs SeCreateSymbolicLinkPrivilege: Developer Mode or an elevated shell")
	}
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Excluded) != 1 {
		t.Fatalf("excluded = %+v", preview.Excluded)
	}
	got := preview.Excluded[0]
	if got.Path != "escape" || got.Reason != ExcludeSymlink || !strings.Contains(got.Detail, "symlink escape") {
		t.Fatalf("symlink exclusion = %+v", got)
	}
	if preview.Files != 1 {
		t.Errorf("files = %d, want the one real file", preview.Files)
	}
}

// TestInventorySymlinkEscapeAgreesWithPush pins the invariant a preview
// exists to keep: it promises exactly what a push carries. A symlink out
// of the root is skipped by both - the walk never reads a target and
// WalkDir never follows one, so aborting the profile over it only cost
// the user every other file.
func TestInventorySymlinkEscapeAgreesWithPush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs SeCreateSymbolicLinkPrivilege: Developer Mode or an elevated shell")
	}
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Blocked {
		t.Fatalf("a symlink escape blocked the preview: %q at %q", preview.BlockedReason, preview.BlockedPath)
	}
	if preview.Files != 1 {
		t.Errorf("files = %d, want the one real file carried", preview.Files)
	}
	// The preview and the push must agree about the same tree: both carry
	// settings.json, both report the link, neither reads the target.
	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("Discover refused a tree the preview offered: %v", err)
	}
	if len(files) != 1 || files[0].Path != "settings.json" {
		t.Errorf("discovered %+v, want only settings.json", files)
	}
	if len(skipped) != 1 || skipped[0].Reason != ExcludeSymlink {
		t.Errorf("skipped = %+v, want the escaping link", skipped)
	}
}

// TestInventoryExcludesOversizedFileUnreadNames the fix for the blocker: a
// file over the server's per-file cap is excluded from its stat alone. The
// proof that it is never read is that its content would fail the scanner -
// the fixture plants a secret in it, and the preview still comes back
// unblocked.
func TestInventoryExcludesOversizedFileUnread(t *testing.T) {
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

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Blocked {
		t.Fatalf("the oversized file was scanned: preview blocked by %q at %q",
			preview.BlockedReason, preview.BlockedPath)
	}
	var found Exclusion
	for _, e := range preview.Excluded {
		if e.Path == "notes/huge.jsonl" {
			found = e
		}
	}
	if found.Reason != ExcludeTooLarge {
		t.Fatalf("exclusions = %+v, want notes/huge.jsonl as %s", preview.Excluded, ExcludeTooLarge)
	}
	if !strings.Contains(found.Detail, "over the") {
		t.Errorf("detail does not name the limit: %q", found.Detail)
	}
	// Only the small file is offered, and its bytes are all that is counted.
	if preview.Files != 1 || preview.Bytes != int64(len(`{"ok":true}`)) {
		t.Errorf("files/bytes = %d/%d, want only settings.json", preview.Files, preview.Bytes)
	}
	// The push agrees, and says what it left behind.
	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("DiscoverFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "settings.json" {
		t.Errorf("discovered %d files, want only settings.json", len(files))
	}
	if len(skipped) != 1 || skipped[0].Reason != ExcludeTooLarge {
		t.Errorf("skipped = %+v, want the oversized file", skipped)
	}
}

// TestInventoryStopsAtTotalBudget proves the walk stops promising files
// once the per-snapshot cap is reached, so a preview can never offer an
// import the server rejects for size.
func TestInventoryStopsAtTotalBudget(t *testing.T) {
	root := setupClaudeRoot(t)
	// Just under the per-file cap each, so the total cap is what bites.
	body := strings.Repeat("x", profilesvc.MaxFileBytes-1)
	for i := range 24 {
		mustWrite(t, filepath.Join(root, "skills", fmt.Sprintf("s%02d.md", i)), body)
	}

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Bytes > profilesvc.MaxTotalBytes {
		t.Fatalf("preview promises %d bytes, over the %d-byte snapshot cap",
			preview.Bytes, profilesvc.MaxTotalBytes)
	}
	over := 0
	for _, e := range preview.Excluded {
		if e.Reason == ExcludeOverBudget {
			over++
		}
	}
	if over == 0 {
		t.Fatalf("nothing reported as over budget; excluded = %+v", preview.Excluded)
	}
	if preview.Files+over != 24 {
		t.Errorf("files %d + over-budget %d != 24", preview.Files, over)
	}
}

// TestInventoryHonorsContextCancellation covers the other half of the
// blocker: a caller that has gone away must be able to stop a walk of the
// user's home directory rather than leave it running.
func TestInventoryHonorsContextCancellation(t *testing.T) {
	root := setupClaudeRoot(t)
	for i := range 50 {
		mustWrite(t, filepath.Join(root, "skills", fmt.Sprintf("s%02d.md", i)), "body")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Inventory(ctx, "claude"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Inventory error = %v, want context.Canceled", err)
	}
}

// TestInventorySkipsNonRegularFiles covers the blocker: a unix socket in a
// profile root - ~/.codex/ipc/ipc.sock exists on any machine codex has run
// on - aborted the whole walk with "no such device or address", so the
// harness could be neither previewed nor pushed.
func TestInventorySkipsNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are a POSIX file type")
	}
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	sock := filepath.Join(root, "ipc", "ipc.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	defer func() { _ = listener.Close() }()

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatalf("a socket in the profile root aborted the walk: %v", err)
	}
	var got Exclusion
	for _, e := range preview.Excluded {
		if e.Path == "ipc/ipc.sock" {
			got = e
		}
	}
	if got.Reason != ExcludeNotRegular {
		t.Fatalf("exclusions = %+v, want ipc/ipc.sock as %s", preview.Excluded, ExcludeNotRegular)
	}
	if !strings.Contains(got.Detail, "socket") {
		t.Errorf("detail does not name the file type: %q", got.Detail)
	}
	if preview.Files != 1 {
		t.Errorf("files = %d, want only settings.json", preview.Files)
	}
	// The push agrees: a socket must not abort discovery either.
	files, _, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("DiscoverFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "settings.json" {
		t.Errorf("discovered %+v, want only settings.json", files)
	}
}

// TestInventoryDoesNotBlockOnAFifo is the half a socket cannot prove:
// os.ReadFile on a FIFO blocks until a writer appears, and the context
// check runs only between entries, so opening one would hang the walk on
// an fd nothing can reclaim. The walk must refuse it on its mode and
// return promptly.
func TestInventoryDoesNotBlockOnAFifo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mkfifo is POSIX-only")
	}
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Inventory(t.Context(), "claude")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a FIFO in the profile root failed the walk: %v", err)
		}
	case <-time.After(10 * time.Second):
		// Failing rather than hanging the suite: the point of the test is
		// that nothing waits on a FIFO that will never be written to.
		t.Fatal("the walk blocked on a FIFO instead of skipping it")
	}
}

// TestInventorySpendsBudgetByPriority covers the starvation the reviewer
// measured on a real ~/.claude: WalkDir visits lexically, so plugins/ and
// other/ sorted first and spent the whole 20 MiB, leaving zero skills and
// zero commands - exactly the configuration this feature exists to carry.
// The fixture reproduces that ordering: "plugins" and "zz-other" bracket
// "skills" and "commands" alphabetically, and together they overrun the
// budget.
func TestInventorySpendsBudgetByPriority(t *testing.T) {
	root := setupClaudeRoot(t)
	// Just under the per-file cap each, so the snapshot cap is what bites.
	body := strings.Repeat("x", profilesvc.MaxFileBytes-1)
	// 15 MiB of plugin cache, sorting before "skills".
	for i := range 15 {
		mustWrite(t, filepath.Join(root, "plugins", fmt.Sprintf("p%02d.json", i)), body)
	}
	// 15 MiB of loose files, sorting after "skills" but before nothing
	// that matters - "other" is last by priority either way.
	for i := range 15 {
		mustWrite(t, filepath.Join(root, "aaa-notes", fmt.Sprintf("n%02d.txt", i)), body)
	}
	mustWrite(t, filepath.Join(root, "skills", "pdf", "SKILL.md"), "# pdf skill\n")
	mustWrite(t, filepath.Join(root, "commands", "review.md"), "# review\n")
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# standing instructions\n")

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	carried := map[string]int{}
	for _, c := range preview.Categories {
		carried[c.Name] = c.Files
	}
	for _, want := range []string{CategoryMemory, CategorySkills, CategoryCommands} {
		if carried[want] == 0 {
			t.Errorf("no %s survived the budget; carried = %v", want, carried)
		}
	}
	if preview.Bytes > profilesvc.MaxTotalBytes {
		t.Errorf("preview promises %d bytes, over the cap", preview.Bytes)
	}
	// The bulk categories are what gets cut, not the configuration.
	over := 0
	for _, e := range preview.Excluded {
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

// TestInventoryDefaultIgnoresTransientDirs pins the second half of the
// same fix: claude's run-time directories are dropped by default, as one
// entry each rather than one per file, and the user's own ignore file can
// take them back.
func TestInventoryDefaultIgnoresTransientDirs(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# standing instructions\n")
	for _, dir := range []string{"projects", "shell-snapshots", "statsig", "todos"} {
		mustWrite(t, filepath.Join(root, dir, "a.jsonl"), "{}\n")
		mustWrite(t, filepath.Join(root, dir, "b.jsonl"), "{}\n")
	}

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Files != 1 {
		t.Errorf("files = %d, want only CLAUDE.md", preview.Files)
	}
	// One entry for the directory, not one per file inside it.
	if len(preview.Excluded) != 4 {
		t.Fatalf("excluded = %+v, want one entry per transient directory", preview.Excluded)
	}
	for _, e := range preview.Excluded {
		if e.Reason != ExcludeIgnored || !strings.Contains(e.Detail, "skipped by default") {
			t.Errorf("exclusion %+v does not say it is an Aether default", e)
		}
	}

	// The user's own file has the last word, gitignore-style.
	mustWrite(t, filepath.Join(root, IgnoreFileName), "!projects/\n")
	preview, err = Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Files != 3 {
		t.Errorf("files = %d, want CLAUDE.md plus the two re-included files", preview.Files)
	}
}

// TestInventoryCapsExclusions keeps a profile root full of dropped files
// from shipping thousands of rows for a surface to render, while the
// count stays exact.
func TestInventoryCapsExclusions(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, IgnoreFileName), "junk/\n")
	for i := range maxExclusions + 25 {
		mustWrite(t, filepath.Join(root, "junk", fmt.Sprintf("f%04d.txt", i)), "x")
	}
	// An ignored directory is reported once, so make each file its own
	// ignore match instead.
	mustWrite(t, filepath.Join(root, IgnoreFileName), "*.junk\n")
	for i := range maxExclusions + 25 {
		mustWrite(t, filepath.Join(root, fmt.Sprintf("f%04d.junk", i)), "x")
	}

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Excluded) != maxExclusions {
		t.Errorf("excluded = %d entries, want the %d cap", len(preview.Excluded), maxExclusions)
	}
	if preview.ExcludedTotal <= maxExclusions {
		t.Errorf("excluded_total = %d, want the exact count above the cap", preview.ExcludedTotal)
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

// TestInventoryCodexScratchTreeIgnored covers the last thing standing
// between codex and an import: codex plants a symlink to its own
// installed binary under tmp/ on every run, which escapes the profile
// root. The scratch tree is a default ignore, so the walk never reaches
// the link at all.
func TestInventoryCodexScratchTreeIgnored(t *testing.T) {
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

	preview, err := Inventory(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Blocked {
		t.Fatalf("codex is blocked by its own scratch tree: %q at %q",
			preview.BlockedReason, preview.BlockedPath)
	}
	if preview.Files != 2 {
		t.Errorf("files = %d, want config.toml and the skill", preview.Files)
	}
	// One entry for the scratch tree, not one per file inside it, and it
	// says the default is Aether's rather than the user's.
	var ignored []Exclusion
	for _, e := range preview.Excluded {
		if e.Reason == ExcludeIgnored {
			ignored = append(ignored, e)
		}
	}
	if len(ignored) != 1 || ignored[0].Path != "tmp" ||
		!strings.Contains(ignored[0].Detail, "skipped by default") {
		t.Fatalf("ignored = %+v, want the tmp tree as an Aether default", ignored)
	}
	// The push agrees.
	files, _, err := DiscoverFiles(t.Context(), "codex", nil)
	if err != nil {
		t.Fatalf("codex could not be pushed: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("discovered %d files, want 2", len(files))
	}
}

// TestInventoryClaudeRuntimeFilesIgnored pins the rest of claude's
// run-time output: a history log and the daemon's own state are not
// configuration another machine wants.
func TestInventoryClaudeRuntimeFilesIgnored(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# standing instructions\n")
	mustWrite(t, filepath.Join(root, "history.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(root, "file-history", "a.json"), "{}\n")
	mustWrite(t, filepath.Join(root, "daemon", "roster.json"), "{}\n")

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Files != 1 {
		t.Errorf("files = %d, want only CLAUDE.md; categories %v", preview.Files, preview.CategoryNames())
	}
}
