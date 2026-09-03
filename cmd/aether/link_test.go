package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/testhome"
)

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

// --key is remembered per profile: an explicit path is stored absolute,
// a relink without the flag keeps what was saved for that profile, and a
// public-key file is refused before anything is dialed.
func TestLinkKeyPersistsAndRelinks(t *testing.T) {
	home := testhome.Isolate(t)
	key := testhome.Ed25519Key(t)
	private := filepath.Join(home, "work_key")
	signer := testhome.WriteSSHKey(t, private, key, "")
	public := private + ".pub"
	if err := os.WriteFile(public, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fresh link with a relative --key: saved absolute.
	rel, err := filepath.Rel(mustGetwd(t), private)
	if err != nil {
		t.Fatal(err)
	}
	got, err := linkKey(rel, cli.Config{}, "")
	if err != nil {
		t.Fatalf("linkKey(%s): %v", rel, err)
	}
	if got != private {
		t.Fatalf("linkKey = %q, want absolute %q", got, private)
	}

	// The saved config carries the key at the top level and in a named
	// profile, and Named hands it back.
	cfg := linkConfig(cli.Config{Addr: "h:2222", User: "aether", Key: private}, cli.Config{}, "prod")
	if cfg.Key != private || len(cfg.Links) != 1 || cfg.Links[0].Key != private {
		t.Fatalf("saved config = %+v", cfg)
	}
	if named, ok := cfg.Named("prod"); !ok || named.Key != private {
		t.Fatalf("Named(prod) = %+v, %v", named, ok)
	}

	// Relinking without --key keeps the profile's saved key, and only
	// that profile's.
	if got, _ = linkKey("", cfg, ""); got != private {
		t.Errorf("default relink key = %q, want %q", got, private)
	}
	if got, _ = linkKey("", cfg, "prod"); got != private {
		t.Errorf("prod relink key = %q, want %q", got, private)
	}
	if got, _ = linkKey("", cli.Config{Links: cfg.Links}, "staging"); got != "" {
		t.Errorf("staging relink key = %q, want discovery", got)
	}

	// The wrong half of the key pair is a clear error, not a parse failure.
	if _, err := linkKey(public, cli.Config{}, ""); err == nil || !strings.Contains(err.Error(), "is a public key") {
		t.Errorf("linkKey(%s) = %v, want public-key rejection", public, err)
	}
	if _, err := linkKey(filepath.Join(home, "missing"), cli.Config{}, ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("linkKey(missing) = %v, want not found", err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
