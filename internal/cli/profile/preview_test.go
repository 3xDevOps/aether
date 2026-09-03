package profile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

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

// TestInventorySymlinkEscapeBlocks pins the invariant finding 2 broke: a
// preview must never promise files a push then refuses. A symlink escape
// aborts Discover, so it has to block the preview too - and it carries no
// --allow-secret override, which is why the reason travels with the flag.
func TestInventorySymlinkEscapeBlocks(t *testing.T) {
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
	if !preview.Blocked {
		t.Fatal("a symlink escape must block the preview: the push refuses it")
	}
	if preview.BlockedReason != ExcludeSymlink || preview.BlockedPath != "escape" {
		t.Fatalf("blocked reason/path = %q %q", preview.BlockedReason, preview.BlockedPath)
	}
	// The preview and the push must agree about the same tree.
	if _, _, err := DiscoverFiles(t.Context(), "claude", nil); err == nil {
		t.Fatal("Discover accepted a symlink escape the preview blocked")
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
	mustWrite(t, filepath.Join(root, "projects", "huge.jsonl"), string(big))

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
		if e.Path == "projects/huge.jsonl" {
			found = e
		}
	}
	if found.Reason != ExcludeTooLarge {
		t.Fatalf("exclusions = %+v, want projects/huge.jsonl as %s", preview.Excluded, ExcludeTooLarge)
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
