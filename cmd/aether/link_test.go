package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
)

// --key picks the private key the link dials with, and the path is saved
// so every later command uses the same key. Relative paths are resolved
// now, because the next command runs from wherever the user happens to be.
func TestParseLinkArgsKey(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	opts, err := parseLinkArgs([]string{"my-server", "--key", "deploy_key", "--name", "prod"})
	if err != nil {
		t.Fatalf("parseLinkArgs: %v", err)
	}
	want := filepath.Join(cwd, "deploy_key")
	if opts.cfg.Key != want {
		t.Errorf("cfg.Key = %q, want %q", opts.cfg.Key, want)
	}

	saved := linkConfig(opts.cfg, cli.Config{}, opts.name)
	if saved.Key != want {
		t.Errorf("saved Key = %q, want %q", saved.Key, want)
	}
	if len(saved.Links) != 1 || saved.Links[0].Key != want {
		t.Errorf("saved profile = %+v, want Key %q", saved.Links, want)
	}

	// Without --key the config stays empty, so cli.Config falls back to
	// ~/.ssh/id_ed25519.
	opts, err = parseLinkArgs([]string{"my-server"})
	if err != nil {
		t.Fatalf("parseLinkArgs without --key: %v", err)
	}
	if opts.cfg.Key != "" {
		t.Errorf("cfg.Key = %q, want empty without --key", opts.cfg.Key)
	}
}

// linkConfig without --name must not touch the profile list beyond
// carrying it forward; with --name it snapshots the fresh link under
// that name, replacing an existing profile of the same name.
func TestLinkConfig(t *testing.T) {
	prev := cli.Config{
		Addr:  "old:2222",
		Links: []cli.NamedLink{{Name: "prod", Addr: "old:2222"}},
	}
	fresh := cli.Config{Addr: "new:2222", User: "aether", Repo: "/src/repo"}

	// No name: top-level fields change, saved profiles ride along untouched.
	got := linkConfig(fresh, prev, "")
	if got.Addr != "new:2222" || len(got.Links) != 1 || got.Links[0].Addr != "old:2222" {
		t.Fatalf("no-name linkConfig = %+v", got)
	}

	// New name: the fresh link is snapshotted alongside the existing one.
	got = linkConfig(fresh, prev, "staging")
	if len(got.Links) != 2 {
		t.Fatalf("staging linkConfig links = %+v", got.Links)
	}
	want := cli.NamedLink{Name: "staging", Addr: "new:2222", User: "aether", Repo: "/src/repo"}
	if got.Links[1] != want {
		t.Fatalf("snapshot = %+v, want %+v", got.Links[1], want)
	}

	// Existing name: the profile is replaced, not duplicated.
	got = linkConfig(fresh, prev, "prod")
	if len(got.Links) != 1 || got.Links[0].Addr != "new:2222" {
		t.Fatalf("re-link prod links = %+v", got.Links)
	}

	// No previous config at all (first link).
	got = linkConfig(fresh, cli.Config{}, "prod")
	if len(got.Links) != 1 || got.Links[0].Name != "prod" {
		t.Fatalf("first-link links = %+v", got.Links)
	}
}
