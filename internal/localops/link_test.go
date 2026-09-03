package localops

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// writeTestFile is a tiny os.WriteFile wrapper shared by the git-backed
// tests.
func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// useTempConfigDir keeps cli.Save/cli.Load away from the real user
// config directory: testhome.Isolate sets AETHER_CONFIG_DIR, which
// cli.Path reads ahead of every platform lookup, and this proves the
// resolved path landed inside the scratch home.
func useTempConfigDir(t *testing.T) {
	t.Helper()
	dir := testhome.Isolate(t)
	path, err := cli.Path()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(dir, path); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("config path %s escaped %s", path, dir)
	}
}

func TestLinkRepoSetsRemoteAndSavesConfig(t *testing.T) {
	requireGit(t)
	useTempConfigDir(t)
	repo := t.TempDir()
	git(t, repo, "init")

	cfg := cli.Config{Addr: "host:2222", User: "alice"}
	updated, url, err := LinkRepo(cfg, repo, "ws_1")
	if err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	if want := "ssh://alice@host:2222/ws_1.git"; url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
	if updated.Repo == "" {
		t.Fatal("updated config has no repo")
	}
	if got := git(t, repo, "remote", "get-url", "aether"); got != url {
		t.Fatalf("remote url = %q, want %q", got, url)
	}

	saved, err := cli.Load()
	if err != nil {
		t.Fatalf("Load after LinkRepo: %v", err)
	}
	if saved.Repo != updated.Repo || saved.Addr != "host:2222" {
		t.Fatalf("saved config = %+v", saved)
	}

	// Linking again updates the existing remote instead of failing on
	// a duplicate add.
	cfg2 := cli.Config{Addr: "other:2222", User: "alice"}
	_, url2, err := LinkRepo(cfg2, repo, "ws_2")
	if err != nil {
		t.Fatalf("second LinkRepo: %v", err)
	}
	if got := git(t, repo, "remote", "get-url", "aether"); got != url2 {
		t.Fatalf("remote url after relink = %q, want %q", got, url2)
	}
}

// TestLinkRepoLeavesRealConfigAlone stands a sentinel config where the
// platform lookup says the user's real one lives (a scratch stand-in,
// never the developer's own), isolates the test, then points HOME,
// XDG_CONFIG_HOME, and AppData back at the sentinel. AETHER_CONFIG_DIR
// still names the scratch directory, and LinkRepo must write only there.
// This guards against the macOS overwrite an earlier helper caused by
// setting XDG_CONFIG_HOME and AppData alone.
func TestLinkRepoLeavesRealConfigAlone(t *testing.T) {
	requireGit(t)
	real := t.TempDir()
	t.Setenv("HOME", real)
	t.Setenv("USERPROFILE", real)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(real, ".config"))
	t.Setenv("AppData", filepath.Join(real, "AppData", "Roaming"))
	t.Setenv(testhome.ConfigDirEnv, "")
	realPath, err := cli.Path()
	if err != nil {
		t.Fatal(err)
	}
	if rel, rerr := filepath.Rel(real, realPath); rerr != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("stand-in real config %s is outside %s", realPath, real)
	}
	if err = os.MkdirAll(filepath.Dir(realPath), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := "{\"addr\":\"sentinel:2222\"}\n"
	if err = os.WriteFile(realPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	useTempConfigDir(t)
	pinned, err := cli.Path()
	if err != nil {
		t.Fatal(err)
	}
	if pinned == realPath {
		t.Fatalf("isolation reused the stand-in real path %s", realPath)
	}
	// Every platform lookup names the stand-in again; only the dedicated
	// variable still points at the scratch directory.
	t.Setenv("HOME", real)
	t.Setenv("USERPROFILE", real)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(real, ".config"))
	t.Setenv("AppData", filepath.Join(real, "AppData", "Roaming"))
	if dir, derr := os.UserConfigDir(); derr != nil || !strings.HasPrefix(realPath, dir) {
		t.Fatalf("platform lookup does not point at the stand-in: dir %q err %v", dir, derr)
	}

	repo := t.TempDir()
	git(t, repo, "init")
	if _, _, err = LinkRepo(cli.Config{Addr: "host:2222", User: "alice"}, repo, "ws_1"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	if _, _, err = LinkRepo(cli.Config{Addr: "other:2222", User: "alice"}, repo, "ws_2"); err != nil {
		t.Fatalf("second LinkRepo: %v", err)
	}

	got, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatalf("LinkRepo rewrote the stand-in real config %s:\n%s", realPath, got)
	}
	saved, err := os.ReadFile(pinned)
	if err != nil {
		t.Fatalf("pinned config %s not written: %v", pinned, err)
	}
	if !strings.Contains(string(saved), "other:2222") {
		t.Fatalf("pinned config = %s", saved)
	}
}

func TestLinkRepoRejectsNonRepo(t *testing.T) {
	requireGit(t)
	useTempConfigDir(t)
	if _, _, err := LinkRepo(cli.Config{Addr: "host:2222"}, t.TempDir(), "ws_1"); err == nil {
		t.Fatal("LinkRepo accepted a non-git directory")
	}
	if _, _, err := LinkRepo(cli.Config{Addr: "host:2222"}, "", "ws_1"); err == nil {
		t.Fatal("LinkRepo accepted an empty repo path")
	}
}

func TestGitRemoteAddsThenUpdates(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	git(t, repo, "init")

	if err := GitRemote(repo, "ssh://a@h:1/x.git", io.Discard, io.Discard); err != nil {
		t.Fatalf("GitRemote add: %v", err)
	}
	if got := git(t, repo, "remote", "get-url", "aether"); got != "ssh://a@h:1/x.git" {
		t.Fatalf("remote after add = %q", got)
	}
	if err := GitRemote(repo, "ssh://b@h:2/y.git", io.Discard, io.Discard); err != nil {
		t.Fatalf("GitRemote set-url: %v", err)
	}
	if got := git(t, repo, "remote", "get-url", "aether"); got != "ssh://b@h:2/y.git" {
		t.Fatalf("remote after set-url = %q", got)
	}
}
