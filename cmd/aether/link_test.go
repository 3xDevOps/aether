package main

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
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
