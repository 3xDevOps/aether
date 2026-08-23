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

	files, err := Discover("claude", nil)
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

	files, err := Discover("claude", nil)
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

	files, err := Discover("claude", nil)
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

func TestDiscoverSymlinkEscapeRejected(t *testing.T) {
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
	_, err := Discover("claude", nil)
	if err == nil {
		t.Fatal("expected symlink escape error")
	}
	var de *DiscoverError
	if !asDiscover(err, &de) || !strings.Contains(err.Error(), "symlink escape") {
		t.Fatalf("err = %v, want symlink escape", err)
	}
}

func TestDiscoverEmbeddedTokenBlocked(t *testing.T) {
	root := setupClaudeRoot(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "settings.json"), string(fixture))
	_, err = Discover("claude", nil)
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
	files, err := Discover("claude", []string{"settings.json"})
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
	_, err = Discover("claude", []string{"settings.json"})
	if err == nil {
		t.Fatal("basename allow should not cover nested/settings.json")
	}
	var de *DiscoverError
	if !asDiscover(err, &de) || de.Path != "nested/settings.json" {
		t.Fatalf("err = %v", err)
	}
}
