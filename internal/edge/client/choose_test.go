package edgeclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Without --edge a machine keeps the hosted edge's address it already
// uses: a token there wins over the compiled default, and with no token
// the address a stored link names does, so an install set up at
// FormerURL behaves as before DefaultURL changed. Only a machine with
// no trace of either address takes DefaultURL.
func TestChooseKeepsTheHostedAddressInUse(t *testing.T) {
	const self, other = "https://edge.example", "https://edge2.example"
	for _, tc := range []struct {
		name             string
		signedIn, linked []string
		want             string
	}{
		{"nothing", nil, nil, DefaultURL},
		{"one token at a self-hosted edge", []string{self}, []string{FormerURL}, self},
		{"one token at the former address", []string{FormerURL}, nil, FormerURL},
		{"former and self-hosted tokens", []string{FormerURL, self}, nil, FormerURL},
		{"tokens at both hosted addresses", []string{FormerURL, DefaultURL, self}, nil, DefaultURL},
		{"two self-hosted tokens, a link at the former address", []string{self, other}, []string{self, FormerURL}, FormerURL},
		{"no token, a link at the former address", nil, []string{FormerURL}, FormerURL},
		{"no token, links at both hosted addresses", nil, []string{FormerURL, DefaultURL}, DefaultURL},
		// The link's own spelling, which its dial looks the token up under.
		{"no token, a link spelled another way", nil, []string{"https://EDGE.onaether.dev:443/"}, FormerURL + ":443"},
		{"no token, a self-hosted link", nil, []string{self}, DefaultURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			edges := map[string]map[string]any{}
			for _, o := range tc.signedIn {
				edges[o] = map[string]any{}
			}
			raw, err := json.Marshal(map[string]any{"edges": edges})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "edge-tokens.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Choose(dir, "", tc.linked)
			if err != nil {
				t.Fatal(err)
			}
			if c.URL() != tc.want {
				t.Errorf("Choose = %s, want %s", c.URL(), tc.want)
			}
			if c, err = Choose(dir, self, tc.linked); err != nil || c.URL() != self {
				t.Errorf("Choose with an edge named = %v, %v; want %s", c, err, self)
			}
		})
	}
}

func TestHostedTakesEitherAddressInAnySpelling(t *testing.T) {
	for u, want := range map[string]bool{
		DefaultURL: true, FormerURL: true, "https://edge.cloudfield.ai:443": true, "https://Edge.Onaether.dev/": true,
		"https://edge.cloudfield.ai:8443": false, "https://cloudfield.ai": false, "https://x.edge.cloudfield.ai": false, "not a url": false,
	} {
		if got := Hosted(u); got != want {
			t.Errorf("Hosted(%q) = %v, want %v", u, got, want)
		}
	}
}
