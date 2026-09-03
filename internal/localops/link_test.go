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
// config directory on every platform and proves the path landed inside
// the scratch home.
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
