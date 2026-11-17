package edge

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"testing"
)

// TestAssetCopiesMatchTheirSources checks that the fonts, licences, logo and
// icons the edge embeds are the dashboard's own files, unchanged.
func TestAssetCopiesMatchTheirSources(t *testing.T) {
	for copied, source := range map[string]string{
		"templates/fonts/saira-latin.woff2":               "../../../web/public/fonts/saira-latin.woff2",
		"templates/fonts/LICENSE-saira.txt":               "../../../web/public/fonts/LICENSE-saira.txt",
		"templates/fonts/LICENSE-dm-sans.txt":             "../../../web/public/fonts/LICENSE-dm-sans.txt",
		"templates/fonts/dm-sans-latin-wght-normal.woff2": "../../../web/node_modules/@fontsource-variable/dm-sans/files/dm-sans-latin-wght-normal.woff2",
		"templates/images/cloudfield-logo-small.svg":      "../../../web/public/cloudfield-logo-small.svg",
		"templates/images/cloudfield-favicon.svg":         "../../../web/public/cloudfield-favicon.svg",
		"templates/images/favicon-32.png":                 "../../../web/public/icons/favicon-32.png",
		"templates/images/apple-touch-icon.png":           "../../../web/public/icons/apple-touch-icon.png",
	} {
		want, err := os.ReadFile(source)
		if errors.Is(err, fs.ErrNotExist) {
			// web/node_modules exists only after bun install.
			t.Logf("%s: no %s to compare with", copied, source)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := templateFS.ReadFile(copied)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s; copy it again", copied, source)
		}
	}
}

func TestAssetsServedOnBothOrigins(t *testing.T) {
	h := newHarness(t)
	for _, origin := range []string{testSignin, testRelay} {
		b := h.browser(t)
		b.origin = origin
		for _, a := range pageAssets {
			resp, body := b.get(t, a.path)
			want, err := templateFS.ReadFile("templates/" + a.file)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || body != string(want) {
				t.Errorf("GET %s on %s: %s, %d bytes, want the %d bytes of %s", a.path, origin, resp.Status, len(body), len(want), a.file)
			}
			if got := resp.Header.Get("Content-Type"); got != a.contentType {
				t.Errorf("GET %s on %s: Content-Type %q, want %q", a.path, origin, got, a.contentType)
			}
		}
	}
}
