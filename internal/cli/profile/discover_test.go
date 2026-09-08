package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	return setupHarnessRoot(t, "claude")
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

func TestDiscoverAllowSecretDoesNotMatchBasenameAlone(t *testing.T) {
	root := setupClaudeRoot(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "nested", "settings.json"), string(fixture))
	files, skipped, err := DiscoverFiles(t.Context(), "claude", []string{"settings.json"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names(files)["nested/settings.json"]; ok {
		t.Fatal("basename allow should not cover nested/settings.json")
	}
	if len(skipped) != 1 || skipped[0].Path != "nested/settings.json" || skipped[0].Reason != ExcludeSecret {
		t.Fatalf("skipped = %+v, want nested/settings.json", skipped)
	}
}

// vendoredFixture is a marketplace plugin's own test file, at the path
// shape claude installs one under:
// plugins/cache/<marketplace>/<plugin>/<version>/... . version is a
// separate argument because the fix must not key off it.
func vendoredFixture(version string) string {
	return "plugins/cache/claude-plugins-official/notes-toolkit/" + version +
		"/tests/ws-protocol.test.js"
}

// marketplaceFixture is the same file in the other tree claude fills:
// the clone of the marketplace repository, which carries plugin sources
// inline and has no version segment at all.
func marketplaceFixture() string {
	return "plugins/marketplaces/claude-plugins-official/plugins/notes-toolkit/tests/ws-protocol.test.js"
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
	// The marketplace clone holds the same sources inline and is the
	// larger tree on a stock install, so it has to be covered too.
	flagged := []string{vendoredFixture("6.3.0"), vendoredFixture("6.4.0"), marketplaceFixture()}
	for _, rel := range flagged {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), string(secret))
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
	for _, rel := range flagged {
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
		// The line belongs with the rule, not trailing after the whole
		// sentence.
		if !strings.Contains(s.Detail, ") at ") {
			t.Errorf("%s detail does not carry the location with the rule: %q", rel, s.Detail)
		}
	}
}

// The vendored carve-out is the plugin trees claude installs into. A
// file the user wrote is reported as their own secret to remove,
// including a file named plugins/cache itself and one elsewhere under
// plugins/.
func TestDiscoverOwnSecretOutsidePluginTrees(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"memory/notes.md", "plugins/config.json", "plugins/cache"} {
		t.Run(rel, func(t *testing.T) {
			root := setupClaudeRoot(t)
			mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
			mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), string(secret))
			files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := names(files)["settings.json"]; !ok {
				t.Errorf("settings.json was not carried: %v", names(files))
			}
			if len(skipped) != 1 || skipped[0].Path != rel || skipped[0].Reason != ExcludeSecret {
				t.Fatalf("skipped = %+v, want %s as the user's own secret", skipped, rel)
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

// vendoredRoots is keyed by harness, and only claude installs plugins
// into those trees. The same path under another harness's profile root
// is a directory the user made, so the finding is reported as theirs.
func TestDiscoverVendoredRootsAreClaudeOnly(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rel := vendoredFixture("6.3.0")
	for _, name := range []string{"codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			root := setupHarnessRoot(t, name)
			mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), string(secret))

			_, skipped, err := DiscoverFiles(t.Context(), name, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(skipped) != 1 || skipped[0].Path != rel || skipped[0].Reason != ExcludeSecret {
				t.Fatalf("skipped = %+v, want %s as the user's own secret under %s", skipped, rel, name)
			}
		})
	}
}

// A secret in a file the user wrote used to refuse the whole walk, so one
// curl example under skills/ kept every other file off the server. It now
// drops that one file and reports it, the way a vendored finding does.
func TestDiscoverOwnSecretDropsOnlyThatFile(t *testing.T) {
	root := setupClaudeRoot(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "skills", "deploy", "README.md"), string(fixture))

	files, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("a secret in the user's own file refused the whole profile: %v", err)
	}
	got := names(files)
	if _, ok := got["settings.json"]; !ok {
		t.Errorf("settings.json was not carried: %v", got)
	}
	if _, ok := got["skills/deploy/README.md"]; ok {
		t.Error("the flagged file was carried")
	}
	if len(skipped) != 1 || skipped[0].Path != "skills/deploy/README.md" || skipped[0].Reason != ExcludeSecret {
		t.Fatalf("skipped = %+v, want the flagged file", skipped)
	}
	// The member has to be able to find the line, so the reported detail
	// carries the scanner's rule and location, not just "secret".
	if !strings.Contains(skipped[0].Detail, "secret detected") || !strings.Contains(skipped[0].Detail, " at ") {
		t.Fatalf("detail = %q, want the rule and location", skipped[0].Detail)
	}
}

// A terminal shows the member nothing before it uploads, so the CLI has
// to hear them name each flagged file. A plugin's own fixture is never
// counted: there is no secret in it for anyone to remove.
func TestUnacknowledgedSecrets(t *testing.T) {
	root := setupClaudeRoot(t)
	secret, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	vendored := vendoredFixture("6.3.0")
	mustWrite(t, filepath.Join(root, "skills", "deploy", "README.md"), string(secret))
	mustWrite(t, filepath.Join(root, filepath.FromSlash(vendored)), string(secret))

	_, skipped, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	flagged := UnacknowledgedSecrets(root, skipped, nil)
	if len(flagged) != 1 || flagged[0].Path != "skills/deploy/README.md" {
		t.Fatalf("flagged = %+v, want the file the member wrote", flagged)
	}
	if named := UnacknowledgedSecrets(root, skipped, []string{"skills/deploy/README.md"}); len(named) != 0 {
		t.Fatalf("flagged = %+v after --skip-secret named it", named)
	}
	// An absolute path answers it too, the way --allow-secret takes one.
	abs := filepath.Join(root, "skills", "deploy", "README.md")
	if named := UnacknowledgedSecrets(root, skipped, []string{abs}); len(named) != 0 {
		t.Fatalf("flagged = %+v after --skip-secret named %s", named, abs)
	}
	// The printed command writes a path starting with a dash relative to
	// the current directory, so that spelling has to answer the finding
	// as well - otherwise the command a member pastes does nothing.
	dotted := "./skills/deploy/README.md"
	if named := UnacknowledgedSecrets(root, skipped, []string{dotted}); len(named) != 0 {
		t.Fatalf("flagged = %+v after --skip-secret named %s", named, dotted)
	}
}
