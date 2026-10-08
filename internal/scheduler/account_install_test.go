package scheduler

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// installAgent places an executable at rel in home, linked from
// ~/.local/bin/<name> by link when link is not empty.
func installAgent(t *testing.T, home, name, rel, link string) {
	t.Helper()
	file := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if link == "" {
		return
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
}

// installClaude installs claude as its native installer does: a versioned
// binary under ~/.local/share/claude linked by absolute path.
func installClaude(t *testing.T, home string) {
	t.Helper()
	installAgent(t, home, "claude", ".local/share/claude/versions/1/claude", "/root/.local/share/claude/versions/1/claude")
}

func (e *shareEnv) launchSpec(t *testing.T, harnessName string) runtime.Spec {
	t.Helper()
	run, err := e.launch(t, harnessName)
	if err != nil {
		t.Fatalf("%s: Launch: %v", harnessName, err)
	}
	return e.rt.byName(string(run.ID)).spec
}

// ownerMounts returns the mounts a spec takes from the owner's home.
func (e *shareEnv) ownerMounts(spec runtime.Spec) []runtime.Mount {
	var out []runtime.Mount
	for _, m := range spec.Mounts {
		if m.HostPath == e.ownerHome {
			out = append(out, m)
		}
	}
	return out
}

func assertNoBorrowedPath(t *testing.T, spec runtime.Spec) {
	t.Helper()
	if strings.Contains(spec.Env["PATH"], accountBin) {
		t.Fatalf("PATH = %q, want no borrowed installation on it", spec.Env["PATH"])
	}
}

// A launcher with no installation of their own runs the owner's: the owner's
// ~/.local/bin and ~/.local/lib beside each other under ~/.aether/account,
// the harness's install directory at its own path, all read-only, and the
// borrowed bin on PATH after the launcher's own. Nothing else of the owner's
// is exposed, not even the rest of ~/.local/share.
func TestSharedLaunchBorrowsTheOwnersInstallation(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, append([]string{".claude/.credentials.json", ".local/lib/node_modules/tool/index.js"}, ownerState...)...)
	installClaude(t, e.ownerHome)
	spec := e.launchSpec(t, "claude")
	want := []runtime.Mount{
		{HostPath: e.ownerHome, Subpath: ".claude/.credentials.json", ContainerPath: "/root/.claude/.credentials.json"},
		{HostPath: e.ownerHome, Subpath: ".local/bin", ContainerPath: "/root/.aether/account/bin", ReadOnly: true},
		{HostPath: e.ownerHome, Subpath: ".local/lib", ContainerPath: "/root/.aether/account/lib", ReadOnly: true},
		{HostPath: e.ownerHome, Subpath: ".local/share/claude", ContainerPath: "/root/.local/share/claude", ReadOnly: true},
	}
	if got := e.ownerMounts(spec); !slices.Equal(got, want) {
		t.Fatalf("owner mounts = %+v, want %+v", got, want)
	}
	e.assertOwnerExposedOnlyAt(t, spec, ".claude/.credentials.json", ".local/bin", ".local/lib", ".local/share/claude")
	entries := strings.Split(spec.Env["PATH"], ":")
	own, borrowed := slices.Index(entries, "/root/.local/bin"), slices.Index(entries, "/root/.aether/account/bin")
	if own < 0 || borrowed != len(entries)-1 || own > borrowed {
		t.Fatalf("PATH = %q, want /root/.local/bin first and /root/.aether/account/bin last", spec.Env["PATH"])
	}
	for _, rel := range []string{accountBin, accountLib, ".local/share/claude"} {
		info, err := os.Lstat(filepath.Join(e.adaHome, filepath.FromSlash(rel)))
		if err != nil || !info.IsDir() {
			t.Fatalf("launcher mountpoint %s = %v, %v; want a directory", rel, info, err)
		}
	}

	// omp is one binary in ~/.local/bin: no install directory, and a missing
	// ~/.local/lib is skipped.
	f := newShareEnv(t, nil)
	writeHomeFiles(t, f.ownerHome, append([]string{".omp/agent/agent.db"}, ownerState...)...)
	installAgent(t, f.ownerHome, "omp", ".local/bin/omp", "")
	spec = f.launchSpec(t, "omp")
	f.assertOwnerExposedOnlyAt(t, spec, ".omp/agent", ".local/bin")
	if got := f.ownerMounts(spec)[1]; got != (runtime.Mount{HostPath: f.ownerHome, Subpath: ".local/bin", ContainerPath: "/root/.aether/account/bin", ReadOnly: true}) {
		t.Fatalf("omp bin mount = %+v", got)
	}

	// An npm install links ~/.local/bin/codex relatively into ~/.local/lib,
	// which resolves inside the relocated pair.
	g := newShareEnv(t, func(cfg *Config) {
		cfg.Harnesses["codex"] = HarnessSpec{TUIArgs: []string{"fake-codex", "{task}"}}
	})
	writeHomeFiles(t, g.ownerHome, ".codex/auth.json")
	installAgent(t, g.ownerHome, "codex", ".local/lib/node_modules/@openai/codex/bin/codex.js", "../lib/node_modules/@openai/codex/bin/codex.js")
	spec = g.launchSpec(t, "codex")
	want = []runtime.Mount{
		{HostPath: g.ownerHome, Subpath: ".codex/auth.json", ContainerPath: "/root/.codex/auth.json"},
		{HostPath: g.ownerHome, Subpath: ".local/bin", ContainerPath: "/root/.aether/account/bin", ReadOnly: true},
		{HostPath: g.ownerHome, Subpath: ".local/lib", ContainerPath: "/root/.aether/account/lib", ReadOnly: true},
	}
	if got := g.ownerMounts(spec); !slices.Equal(got, want) {
		t.Fatalf("codex owner mounts = %+v, want %+v", got, want)
	}
}

// A launcher who has the executable runs their own installation, and a launch
// where neither side has it mounts no installation: either way only the login
// comes from the owner and PATH is the launcher's.
func TestSharedLaunchKeepsTheLaunchersInstallation(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, e *shareEnv){
		"launcher has it": func(t *testing.T, e *shareEnv) {
			installClaude(t, e.ownerHome)
			installAgent(t, e.adaHome, "claude", ".local/bin/claude", "")
		},
		"neither has it": func(t *testing.T, e *shareEnv) {
			installAgent(t, e.ownerHome, "gh", ".local/bin/gh", "")
		},
		// A link that leaves the directories a borrowed installation mounts
		// would dangle in the run, so it does not count as one.
		"owner's link leaves the borrowed directories": func(t *testing.T, e *shareEnv) {
			installAgent(t, e.ownerHome, "claude", "tools/claude", "/root/tools/claude")
		},
		"owner's relative link leaves the borrowed directories": func(t *testing.T, e *shareEnv) {
			installAgent(t, e.ownerHome, "claude", "tools/claude", "../../tools/claude")
		},
		// ~/.local/bin and ~/.local/lib are mounted under ~/.aether/account,
		// so an absolute link into them points at the launcher's own ~/.local
		// in the run.
		"owner's absolute link into .local/lib": func(t *testing.T, e *shareEnv) {
			installAgent(t, e.ownerHome, "claude", ".local/lib/claude/bin/claude", "/root/.local/lib/claude/bin/claude")
		},
		// The install directory is mounted at its own path, so a relative
		// link from the relocated bin would miss it.
		"owner's relative link into the install directory": func(t *testing.T, e *shareEnv) {
			installAgent(t, e.ownerHome, "claude", ".local/share/claude/versions/1/claude", "../share/claude/versions/1/claude")
		},
	}
	for name, install := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newShareEnv(t, nil)
			writeHomeFiles(t, e.ownerHome, append([]string{".claude/.credentials.json"}, ownerState...)...)
			install(t, e)
			spec := e.launchSpec(t, "claude")
			e.assertOwnerExposedOnlyAt(t, spec, ".claude/.credentials.json")
			assertNoBorrowedPath(t, spec)
			if _, err := os.Lstat(filepath.Join(e.adaHome, ".aether", "account")); !os.IsNotExist(err) {
				t.Fatalf("launcher's ~/.aether/account = %v, want no borrowed mountpoints", err)
			}
		})
	}

	// The owner's own launch is unchanged.
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	installClaude(t, e.ownerHome)
	own, err := e.sched.Launch(t.Context(), e.ws.ID, e.owner.ID, e.owner.ID, "own", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("owner Launch: %v", err)
	}
	spec := e.rt.byName(string(own.ID)).spec
	if len(spec.Mounts) != 2 || spec.Mounts[0] != (runtime.Mount{HostPath: e.ownerHome, ContainerPath: "/root"}) {
		t.Fatalf("own launch mounts = %+v, want only owner's home/cache", spec.Mounts)
	}
	assertNoBorrowedPath(t, spec)
}

// An owner's ~/.local/bin that is itself a symlink points outside the
// directories a borrowed installation mounts, so it is no installation to
// borrow: the launch gets nothing of the owner's beyond the login.
func TestSharedLaunchIgnoresASymlinkedInstallation(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	installAgent(t, e.ownerHome, "claude", ".local/bin2/claude", "")
	if err := os.Symlink("bin2", filepath.Join(e.ownerHome, ".local", "bin")); err != nil {
		t.Fatal(err)
	}
	spec := e.launchSpec(t, "claude")
	e.assertOwnerExposedOnlyAt(t, spec, ".claude/.credentials.json")
	assertNoBorrowedPath(t, spec)
}
