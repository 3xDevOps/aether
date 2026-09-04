package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
)

// --key picks the private key the link dials with, and the path is saved
// so every later command that dials the server uses the same key.
// Relative paths are resolved now, because the next command runs from
// wherever the user happens to be.
func TestParseLinkArgsKey(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("deploy_key", nil, 0o600); err != nil {
		t.Fatal(err)
	}
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
	// ~/.ssh/id_ed25519 or to the key a previous link saved.
	opts, err = parseLinkArgs([]string{"my-server"})
	if err != nil {
		t.Fatalf("parseLinkArgs without --key: %v", err)
	}
	if opts.cfg.Key != "" {
		t.Errorf("cfg.Key = %q, want empty without --key", opts.cfg.Key)
	}
}

// A --key path that is not there must fail on the path the user typed,
// before the dial turns it into an authentication failure naming no file.
func TestParseLinkArgsMissingKey(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here")

	_, err := parseLinkArgs([]string{"my-server", "--key", missing})
	if err == nil {
		t.Fatal("parseLinkArgs accepted a --key path that does not exist")
	}
	for _, want := range []string{"link --key", missing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// Re-linking without --key must keep the key the last link saved, for the
// default link and for a named profile: dropping it would silently send
// the next command back to ~/.ssh/id_ed25519.
func TestSavedKeyCarriesForward(t *testing.T) {
	prev := cli.Config{
		Addr: "old:2222",
		Key:  "/keys/default",
		Links: []cli.NamedLink{
			{Name: "prod", Addr: "prod:2222", Key: "/keys/prod"},
			{Name: "staging", Addr: "staging:2222"},
		},
	}

	cases := []struct {
		name string
		want string
	}{
		{name: "", want: "/keys/default"},
		{name: "prod", want: "/keys/prod"},
		// The profile saved no key of its own, so Named falls back to
		// the default link's.
		{name: "staging", want: "/keys/default"},
		// A profile that does not exist yet has nothing but the default.
		{name: "new", want: "/keys/default"},
	}
	for _, tc := range cases {
		if got := savedKey(prev, tc.name); got != tc.want {
			t.Errorf("savedKey(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := savedKey(cli.Config{}, "prod"); got != "" {
		t.Errorf("savedKey on a first link = %q, want empty", got)
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
