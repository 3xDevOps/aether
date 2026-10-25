package localops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	updated, url, _, err := LinkRepo(cfg, repo, "ws_1")
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
	if saved.Repo != updated.Repo || saved.Addr != "host:2222" ||
		!slices.Equal(saved.Repos, []cli.LinkedRepo{{WorkspaceID: "ws_1", Path: updated.Repo}}) {
		t.Fatalf("saved config = %+v", saved)
	}

	// Linking again updates the existing remote instead of failing on
	// a duplicate add.
	cfg2 := cli.Config{Addr: "other:2222", User: "alice"}
	_, url2, _, err := LinkRepo(cfg2, repo, "ws_2")
	if err != nil {
		t.Fatalf("second LinkRepo: %v", err)
	}
	if got := git(t, repo, "remote", "get-url", "aether"); got != url2 {
		t.Fatalf("remote url after relink = %q, want %q", got, url2)
	}
}

func TestRepoForReadsAnOldConfigsClone(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "remote", "add", "aether", "ssh://aether@host:2222/ws_old.git")
	cfg := cli.Config{Addr: "host:2222", Repo: repo, Repos: []cli.LinkedRepo{{WorkspaceID: "ws_new", Path: "/src/new"}}}
	for workspace, want := range map[string]string{"ws_old": repo, "ws_new": "/src/new", "ws_other": ""} {
		if got, err := RepoFor(cfg, workspace); err != nil || got != want {
			t.Errorf("RepoFor(%s) = %q, %v; want %q", workspace, got, err, want)
		}
	}
	cfg.Repo = filepath.Join(repo, "moved")
	if got, err := RepoFor(cfg, "ws_new"); err != nil || got != "/src/new" {
		t.Errorf("RepoFor(ws_new) with the old clone gone = %q, %v; want the recorded clone", got, err)
	}
	if _, err := RepoFor(cfg, "ws_old"); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Errorf("RepoFor(ws_old) with the old clone gone: err = %v, want git's error naming it", err)
	}
}

// Linking a second clone from a config that names its first only in Repo
// keeps the first linked to its workspace.
func TestLinkRepoKeepsAnOldConfigsClone(t *testing.T) {
	requireGit(t)
	useTempConfigDir(t)
	old := t.TempDir()
	git(t, old, "init")
	git(t, old, "remote", "add", "aether", "ssh://alice@host:2222/ws_old.git")
	repo := t.TempDir()
	git(t, repo, "init")
	updated, _, _, err := LinkRepo(cli.Config{Addr: "host:2222", User: "alice", Repo: old}, repo, "ws_new")
	if err != nil {
		t.Fatal(err)
	}
	for workspace, want := range map[string]string{"ws_old": old, "ws_new": updated.Repo} {
		if got, err := RepoFor(updated, workspace); err != nil || got != want {
			t.Errorf("RepoFor(%s) = %q, %v; want %q", workspace, got, err, want)
		}
	}
}

// A clone another process saved after the caller read its config survives a
// link made through a named profile.
func TestLinkRepoKeepsCloneSavedMeanwhile(t *testing.T) {
	requireGit(t)
	useTempConfigDir(t)
	repo := t.TempDir()
	git(t, repo, "init")
	selected := cli.Config{Addr: "prod:2222", User: "alice", Active: "prod"}
	onDisk := cli.Config{
		Addr:  "default:2222",
		Links: []cli.NamedLink{{Name: "prod", Addr: "prod:2222", User: "alice"}},
		Repos: []cli.LinkedRepo{{WorkspaceID: "ws_other", Path: "/clones/other"}},
	}
	if err := cli.Save(onDisk); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LinkRepo(selected, repo, "ws_new"); err != nil {
		t.Fatal(err)
	}
	got, err := cli.Load()
	if err != nil {
		t.Fatal(err)
	}
	for workspace, want := range map[string]string{"ws_other": "/clones/other", "ws_new": got.Links[0].Repo} {
		if p, _ := RepoFor(got, workspace); p != want {
			t.Errorf("RepoFor(%s) = %q; want %q", workspace, p, want)
		}
	}
}

func TestLinkRepoRefusesChangedNamedProfileBeforeWritingRemote(t *testing.T) {
	requireGit(t)
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			useTempConfigDir(t)
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "remote", "add", "aether", "ssh://alice@original:2222/ws_original.git")
			selected := cli.Config{Addr: "prod:2222", User: "alice", Active: "prod"}
			saved := cli.Config{Addr: "default:2222"}
			if changed {
				saved.Links = []cli.NamedLink{{Name: "prod", Addr: "replacement:2222", User: "alice"}}
			}
			if err := cli.Save(saved); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := LinkRepo(selected, repo, "ws_new"); err == nil {
				t.Fatal("link accepted a removed or changed profile")
			}
			if got := git(t, repo, "remote", "get-url", "aether"); got != "ssh://alice@original:2222/ws_original.git" {
				t.Fatalf("remote changed on refusal: %s", got)
			}
		})
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
	if _, _, _, err = LinkRepo(cli.Config{Addr: "host:2222", User: "alice"}, repo, "ws_1"); err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	if _, _, _, err = LinkRepo(cli.Config{Addr: "other:2222", User: "alice"}, repo, "ws_2"); err != nil {
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
	if _, _, _, err := LinkRepo(cli.Config{Addr: "host:2222"}, t.TempDir(), "ws_1"); err == nil {
		t.Fatal("LinkRepo accepted a non-git directory")
	}
	if _, _, _, err := LinkRepo(cli.Config{Addr: "host:2222"}, "", "ws_1"); err == nil {
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

func TestOriginURL(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	git(t, repo, "init")

	got, err := OriginURL(repo)
	if err != nil || got != "" {
		t.Fatalf("OriginURL without an origin = (%q, %v), want (\"\", nil)", got, err)
	}

	const upstream = "https://github.com/acme/app.git"
	git(t, repo, "remote", "add", "origin", upstream)
	got, err = OriginURL(repo)
	if err != nil {
		t.Fatalf("OriginURL: %v", err)
	}
	if got != upstream {
		t.Fatalf("OriginURL = %q, want %q", got, upstream)
	}

	if _, err := OriginURL(filepath.Join(repo, "not-a-repo")); err == nil {
		t.Fatal("OriginURL outside a repository should fail")
	}
}

func TestMoveRemoteMovesEveryLinkedClone(t *testing.T) {
	requireGit(t)
	testhome.Isolate(t)
	prev := cli.Config{Addr: "old.example:2222", User: "aether"}
	cfg := cli.Config{User: "aether", ServerID: testServerID}
	clone := func(url string) string {
		repo := t.TempDir()
		git(t, repo, "init")
		if err := GitRemote(repo, url, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		return repo
	}
	first := clone(cli.GitURL("aether", prev.GitHost(), "ws_1"))
	second := clone(cli.GitURL("aether", prev.GitHost(), "ws_2"))
	other := clone(cli.GitURL("aether", "other.example:2222", "ws_3"))
	cfg.Repo = second
	cfg.Repos = []cli.LinkedRepo{{WorkspaceID: "ws_1", Path: first}, {WorkspaceID: "ws_2", Path: second}, {WorkspaceID: "ws_3", Path: other}}

	if _, err := MoveRemote(prev, cfg, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for repo, ws := range map[string]string{first: "ws_1", second: "ws_2"} {
		if got, want := git(t, repo, "remote", "get-url", "aether"), cli.GitURL("aether", cfg.GitHost(), ws); got != want {
			t.Errorf("%s remote = %q, want %q", ws, got, want)
		}
	}
	if got, want := git(t, other, "remote", "get-url", "aether"), cli.GitURL("aether", "other.example:2222", "ws_3"); got != want {
		t.Errorf("another server's remote = %q, want it untouched (%q)", got, want)
	}
}

func TestLinkRepoReturnsGitsGuidanceOnSuccess(t *testing.T) {
	requireGit(t)
	useTempConfigDir(t)
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "core.sshCommand", "ssh -i ~/.ssh/work")
	cfg := cli.Config{User: "aether", EdgeURL: "https://edge.example", ServerID: testServerID}
	_, _, out, err := LinkRepo(cfg, repo, "ws_1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "config core.sshCommand") {
		t.Fatalf("output %q lacks the command that repairs core.sshCommand", out)
	}
}
