package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadOldFormat proves a config file written before Links existed
// still parses to exactly the same Config and re-saves without gaining
// a links key.
func TestLoadOldFormat(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{
  "addr": "host:2222",
  "user": "alice",
  "key": "/home/alice/.ssh/id_ed25519",
  "repo": "/src/repo",
  "known_hosts": "/home/alice/.ssh/known_hosts"
}
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Addr:       "host:2222",
		User:       "alice",
		Key:        "/home/alice/.ssh/id_ed25519",
		Repo:       "/src/repo",
		KnownHosts: "/home/alice/.ssh/known_hosts",
	}
	if cfg.Addr != want.Addr || cfg.User != want.User || cfg.Key != want.Key ||
		cfg.Repo != want.Repo || cfg.KnownHosts != want.KnownHosts || len(cfg.Links) != 0 {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}

	// Re-saving must not invent a links key for a link-less config.
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["links"]; ok {
		t.Fatalf("re-saved old config gained a links key: %s", body)
	}
}

// TestLinksRoundTrip proves named links survive Save/Load.
func TestLinksRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := Config{
		Addr: "host:2222",
		User: "alice",
		Links: []NamedLink{
			{Name: "prod", Addr: "prod:2222"},
			{Name: "staging", Addr: "staging:2222", User: "bob"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Links) != 2 || got.Links[0] != cfg.Links[0] || got.Links[1] != cfg.Links[1] {
		t.Fatalf("Links = %+v, want %+v", got.Links, cfg.Links)
	}
}

// TestNamedOverlay proves Named overlays only the profile's non-empty
// fields on the top-level defaults and sets Active.
func TestNamedOverlay(t *testing.T) {
	cfg := Config{
		Addr:       "default:2222",
		User:       "alice",
		Key:        "/default/key",
		Repo:       "/default/repo",
		KnownHosts: "/default/kh",
		Links: []NamedLink{
			{Name: "prod", Addr: "prod:2222", User: "deploy"},
		},
	}

	got, ok := cfg.Named("prod")
	if !ok {
		t.Fatal("Named(prod) = false")
	}
	if got.Active != "prod" {
		t.Errorf("Active = %q, want prod", got.Active)
	}
	// Overridden fields come from the link.
	if got.Addr != "prod:2222" || got.User != "deploy" {
		t.Errorf("overlay = addr %q user %q", got.Addr, got.User)
	}
	// Empty link fields keep the top-level defaults.
	if got.Key != "/default/key" || got.Repo != "/default/repo" || got.KnownHosts != "/default/kh" {
		t.Errorf("defaults lost: key %q repo %q known_hosts %q", got.Key, got.Repo, got.KnownHosts)
	}
	// The source config is untouched.
	if cfg.Active != "" || cfg.Addr != "default:2222" {
		t.Errorf("Named mutated its receiver: %+v", cfg)
	}

	if _, ok := cfg.Named("nope"); ok {
		t.Error("Named(nope) = true, want false")
	}
}

// TestUpsertLink proves insert, replace-by-name, and that the original
// slice is never mutated.
func TestUpsertLink(t *testing.T) {
	base := Config{Addr: "host:2222", Links: []NamedLink{{Name: "prod", Addr: "old:2222"}}}

	added := UpsertLink(base, NamedLink{Name: "staging", Addr: "staging:2222"})
	if len(added.Links) != 2 || added.Links[1].Name != "staging" {
		t.Fatalf("insert: Links = %+v", added.Links)
	}

	replaced := UpsertLink(base, NamedLink{Name: "prod", Addr: "new:2222"})
	if len(replaced.Links) != 1 || replaced.Links[0].Addr != "new:2222" {
		t.Fatalf("replace: Links = %+v", replaced.Links)
	}

	// base is untouched by either upsert.
	if len(base.Links) != 1 || base.Links[0].Addr != "old:2222" {
		t.Fatalf("UpsertLink mutated its input: %+v", base.Links)
	}
}
