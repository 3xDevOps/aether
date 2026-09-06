package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestNormalizeAddr(t *testing.T) {
	for input, want := range map[string]string{
		"host":       "host:2222",
		"host:2200":  "host:2200",
		"127.0.0.1":  "127.0.0.1:2222",
		"[::1]":      "[::1]:2222",
		"[::1]:2200": "[::1]:2200",
		"::1":        "[::1]:2222",
	} {
		if got := normalizeAddr(input); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSavedKeyCarriesForward(t *testing.T) {
	prev := Config{
		Addr: "old:2222",
		Key:  "/keys/default",
		Links: []NamedLink{
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
		{name: "staging", want: "/keys/default"},
		{name: "new", want: "/keys/default"},
	}
	for _, tc := range cases {
		if got := savedKey(prev, tc.name); got != tc.want {
			t.Errorf("savedKey(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := savedKey(Config{}, "prod"); got != "" {
		t.Errorf("savedKey on first link = %q, want empty", got)
	}
}

func TestLinkConfig(t *testing.T) {
	prev := Config{Addr: "old:2222", Links: []NamedLink{{Name: "prod", Addr: "old:2222"}}}
	fresh := Config{Addr: "new:2222", User: "aether", Repo: "/src/repo"}
	got := linkConfig(fresh, prev, "")
	if got.Addr != "new:2222" || len(got.Links) != 1 || got.Links[0].Addr != "old:2222" {
		t.Fatalf("no-name linkConfig = %+v", got)
	}
	got = linkConfig(fresh, prev, "staging")
	want := NamedLink{Name: "staging", Addr: "new:2222", User: "aether", Repo: "/src/repo", AutoKey: true}
	if len(got.Links) != 2 || got.Links[1] != want {
		t.Fatalf("snapshot = %+v, want %+v", got.Links, want)
	}
	got = linkConfig(fresh, prev, "prod")
	if len(got.Links) != 1 || got.Links[0].Addr != "new:2222" {
		t.Fatalf("re-link prod links = %+v", got.Links)
	}
}

func TestLinkKeyPersistsAndRelinks(t *testing.T) {
	home := testhome.Isolate(t)
	key := testhome.Ed25519Key(t)
	private := filepath.Join(home, "work_key")
	signer := testhome.WriteSSHKey(t, private, key, "")
	public := private + ".pub"
	if err := os.WriteFile(public, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	got, err := linkKey("work_key", Config{}, "")
	if err != nil {
		t.Fatalf("linkKey(work_key): %v", err)
	}
	if got != private {
		t.Fatalf("linkKey = %q, want %q", got, private)
	}
	cfg := linkConfig(Config{Addr: "h:2222", User: "aether", Key: private}, Config{}, "prod")
	if cfg.Key != private || len(cfg.Links) != 1 || cfg.Links[0].Key != private || cfg.Links[0].AutoKey {
		t.Fatalf("saved config = %+v", cfg)
	}
	if got, _ = linkKey("", cfg, "prod"); got != private {
		t.Errorf("prod relink key = %q, want %q", got, private)
	}
	if got, err = linkKey("auto", cfg, "prod"); err != nil || got != "" {
		t.Errorf("linkKey(auto) = %q, %v", got, err)
	}
	cleared := linkConfig(Config{Addr: "h:2222", User: "aether"}, cfg, "prod")
	if cleared.Key != "" || len(cleared.Links) != 1 || cleared.Links[0].Key != "" || !cleared.Links[0].AutoKey {
		t.Fatalf("config after auto = %+v", cleared)
	}
	if _, err := linkKey(public, Config{}, ""); err == nil || !strings.Contains(err.Error(), "is a public key") {
		t.Errorf("linkKey(public) = %v, want public-key rejection", err)
	}
	if _, err := linkKey(filepath.Join(home, "missing"), Config{}, ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("linkKey(missing) = %v, want not found", err)
	}
}
