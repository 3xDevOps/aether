package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/harness"
)

func TestDiscoverExcludesRegistryCredentials(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"model":"opus"}`)
	mustWrite(t, filepath.Join(root, ".credentials.json"), `{"token":"secret"}`)
	mustWrite(t, filepath.Join(root, "auth.json"), `{"token":"secret"}`)
	mustWrite(t, filepath.Join(root, "keychain"), "k")
	mustWrite(t, filepath.Join(root, "commands", "review.md"), "# review\n")

	files, err := Discover(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := names(files)
	if _, ok := got["settings.json"]; !ok {
		t.Fatalf("settings.json missing: %v", got)
	}
	if _, ok := got["commands/review.md"]; !ok {
		t.Fatalf("commands/review.md missing: %v", got)
	}
	for _, denied := range []string{".credentials.json", "auth.json", "keychain"} {
		if _, ok := got[denied]; ok {
			t.Errorf("credential path %s was not excluded", denied)
		}
	}
}

func TestDiscoverIgnoreFilePatternsAndPrecedence(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "noise.log"), "log")
	mustWrite(t, filepath.Join(root, "keep.log"), "keep")
	mustWrite(t, filepath.Join(root, "tmp", "x.txt"), "x")
	mustWrite(t, filepath.Join(root, IgnoreFileName), "*.log\ntmp/\n!keep.log\n")

	files, err := Discover(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := names(files)
	if _, ok := got["settings.json"]; !ok {
		t.Fatalf("settings.json missing: %v", got)
	}
	if _, ok := got["keep.log"]; !ok {
		t.Fatalf("negation did not re-include keep.log: %v", got)
	}
	if _, ok := got["noise.log"]; ok {
		t.Errorf("noise.log should be ignored")
	}
	if _, ok := got["tmp/x.txt"]; ok {
		t.Errorf("tmp/x.txt should be ignored by dir pattern")
	}
	if _, ok := got[IgnoreFileName]; ok {
		t.Errorf("%s must not be uploaded", IgnoreFileName)
	}
}

func TestDiscoverNegationCannotReincludeDenied(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, ".credentials.json"), `{"token":"no"}`)
	mustWrite(t, filepath.Join(root, "auth.json"), `{"token":"no"}`)
	mustWrite(t, filepath.Join(root, IgnoreFileName), "*\n!settings.json\n!.credentials.json\n!auth.json\n")

	files, err := Discover(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := names(files)
	if _, ok := got["settings.json"]; !ok {
		t.Fatalf("settings.json should be re-included by negation: %v", got)
	}
	if _, ok := got[".credentials.json"]; ok {
		t.Error("negation re-included registry DenyNames path")
	}
	if _, ok := got["auth.json"]; ok {
		t.Error("negation re-included extra credential name")
	}
}

// TestDiscoverSymlinkEscapeSkipped pins what a link out of the profile
// root costs: the link, and nothing else. Symlinking skills into a shared
// directory is an ordinary setup, and aborting the whole profile over it
// left the user no override - while keeping no bytes off the server that
// skipping does not, since the walk never reads a target and WalkDir
// never follows one.
func TestDiscoverSymlinkEscapeSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs SeCreateSymbolicLinkPrivilege: Developer Mode or an elevated shell")
	}
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "skills", "local", "SKILL.md"), "# local\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "skills", "shared")); err != nil {
		t.Fatal(err)
	}

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("a symlink escape refused the whole profile: %v", err)
	}
	got := names(files)
	for _, want := range []string{"settings.json", "skills/local/SKILL.md"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s was not carried: %v", want, got)
		}
	}
	if len(skipped) != 1 || skipped[0].Path != "skills/shared" || skipped[0].Reason != ExcludeSymlink {
		t.Fatalf("skipped = %+v, want the escaping link", skipped)
	}
	// The escaping target's bytes never reach the caller under any path.
	for _, f := range files {
		if strings.Contains(string(f.Content), "secret") {
			t.Fatalf("%s carries the escaping target's content", f.Path)
		}
	}
}

func TestDiscoverEmbeddedTokenBlocked(t *testing.T) {
	root := setupClaudeRoot(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), string(fixture))
	_, err = Discover(t.Context(), "claude", nil)
	if err == nil {
		t.Fatal("expected secret finding")
	}
	var de *DiscoverError
	if !asDiscover(err, &de) || !strings.Contains(err.Error(), "secret detected") {
		t.Fatalf("err = %v, want secret detected", err)
	}
	if de.Path != "settings.json" || de.Location == "" {
		t.Fatalf("finding path/location = %s %s", de.Path, de.Location)
	}
}

func TestDiscoverAllowSecretSucceeds(t *testing.T) {
	root := setupClaudeRoot(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), string(fixture))
	files, err := Discover(t.Context(), "claude", []string{"settings.json"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names(files)["settings.json"]; !ok {
		t.Fatalf("allow-secret should include settings.json: %v", names(files))
	}
}

func TestBuildPushParamsDeltaOmitsKnownBlobs(t *testing.T) {
	files := []LocalFile{
		{Path: "a.json", Mode: 0o644, Content: []byte(`{"a":1}`)},
		{Path: "b.json", Mode: 0o644, Content: []byte(`{"b":2}`)},
	}
	params, err := BuildPushParams(nil, "claude", files, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(params.Paths) != 2 || len(params.Blobs) != 2 {
		t.Fatalf("full upload: paths=%d blobs=%d", len(params.Paths), len(params.Blobs))
	}
}

func setupClaudeRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	userHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHome = os.UserHomeDir })
	p, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("claude harness missing")
	}
	root := filepath.Join(home, filepath.FromSlash(p.LocalRoot))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(files []LocalFile) map[string]LocalFile {
	out := map[string]LocalFile{}
	for _, f := range files {
		out[f.Path] = f
	}
	return out
}

func asDiscover(err error, de **DiscoverError) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*DiscoverError)
	if ok {
		*de = e
	}
	return ok
}

func TestDiscoverAllowSecretDoesNotMatchBasenameAlone(t *testing.T) {
	root := setupClaudeRoot(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "nested", "settings.json"), string(fixture))
	_, err = Discover(t.Context(), "claude", []string{"settings.json"})
	if err == nil {
		t.Fatal("basename allow should not cover nested/settings.json")
	}
	var de *DiscoverError
	if !asDiscover(err, &de) || de.Path != "nested/settings.json" {
		t.Fatalf("err = %v", err)
	}
}

// vendoredFixture is a marketplace plugin's own test file, at the path
// shape claude installs one under:
// plugins/cache/<marketplace>/<plugin>/<version>/... . version is a
// separate argument because the fix must not key off it.
func vendoredFixture(version string) string {
	return "plugins/cache/claude-plugins-official/notes-toolkit/" + version +
		"/tests/brainstorm-server/ws-protocol.test.js"
}

// A secret-shaped string in an installed plugin's test fixture is not
// the user's to remove, and its path carries the plugin version, so the
// old --allow-secret override died on the next plugin update. It must
// drop the one file and let the import through.
func TestDiscoverVendoredPluginFindingSkipsWithoutRefusing(t *testing.T) {
	root := setupClaudeRoot(t)
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), `{"model":"opus"}`)
	mustWrite(t, filepath.Join(root, "skills", "review", "SKILL.md"), "# review\n")
	mustWrite(t, filepath.Join(root, "plugins", "cache", "claude-plugins-official",
		"notes-toolkit", "6.3.0", "README.md"), "# notes-toolkit\n")
	// Two installed versions of the same plugin, which is the ordinary
	// state of a plugin cache. Nothing here may key off either segment.
	for _, version := range []string{"6.3.0", "6.4.0"} {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(vendoredFixture(version))), string(secret))
	}

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("a vendored plugin fixture refused the whole push: %v", err)
	}
	got := names(files)
	for _, want := range []string{
		"settings.json",
		"skills/review/SKILL.md",
		"plugins/cache/claude-plugins-official/notes-toolkit/6.3.0/README.md",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s missing from the push: %v", want, got)
		}
	}
	byPath := map[string]Exclusion{}
	for _, s := range skipped {
		byPath[s.Path] = s
	}
	for _, version := range []string{"6.3.0", "6.4.0"} {
		rel := vendoredFixture(version)
		if _, ok := got[rel]; ok {
			t.Errorf("%s was pushed despite a scanner finding", rel)
		}
		s, ok := byPath[rel]
		if !ok {
			t.Fatalf("%s was dropped without being reported: %v", rel, skipped)
		}
		if s.Reason != ExcludeVendoredSecret {
			t.Errorf("%s reason = %q, want %q", rel, s.Reason, ExcludeVendoredSecret)
		}
		if !strings.Contains(s.Detail, "third-party plugin content") {
			t.Errorf("%s detail does not name it as third-party: %q", rel, s.Detail)
		}
	}
}

// The vendored carve-out is the plugin cache only. A file the user wrote
// still refuses the push, including elsewhere under plugins/.
func TestDiscoverOwnSecretStillRefusesOutsidePluginCache(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"memory/notes.md", "plugins/config.json"} {
		t.Run(rel, func(t *testing.T) {
			root := setupClaudeRoot(t)
			mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
			mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), string(secret))
			if _, err := Discover(t.Context(), "claude", nil); err == nil {
				t.Fatalf("a finding in %s must refuse the push", rel)
			} else {
				var de *DiscoverError
				if !asDiscover(err, &de) || de.Path != rel {
					t.Fatalf("err = %v", err)
				}
			}
		})
	}
}

// --allow-secret still carries a vendored file when the user wants it,
// so the carve-out drops files rather than taking the choice away.
func TestDiscoverAllowSecretCarriesVendoredFile(t *testing.T) {
	root := setupClaudeRoot(t)
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rel := vendoredFixture("6.3.0")
	mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), string(secret))

	files, err := Discover(t.Context(), "claude", []string{rel})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names(files)[rel]; !ok {
		t.Fatalf("--allow-secret did not carry %s: %v", rel, names(files))
	}
}
