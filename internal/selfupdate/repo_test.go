package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseURLsNameTheRenamedRepository(t *testing.T) {
	base := DefaultChecker().BaseURL()
	for got, want := range map[string]string{
		base: "https://github.com/3xDevOps/cloudfield",
		Manifest{Version: "v1.3.0"}.AssetURL(ManifestName): "https://github.com/3xDevOps/cloudfield/releases/download/v1.3.0/aether-release.json",
		PointerURL(base, ChannelStable):                    "https://github.com/3xDevOps/cloudfield/releases/download/channels/stable.json",
	} {
		if got != want {
			t.Errorf("built %s, want %s", got, want)
		}
	}
}

// GitHub answers every URL under a renamed repository's old name with a 301
// to the same path under the new one. A binary built before the rename
// still resolves, verifies and downloads a release published after it.
func TestABinaryOnTheOldRepositoryNameFollowsTheRename(t *testing.T) {
	setVersion(t, "v0.9.0-beta.1", "abc1234")
	f := &fakeRelease{latest: "v0.9.0-beta.1", assets: testAssets, served: map[string][]byte{},
		builds: map[string]Release{"v0.9.0-beta.2": {Channel: ChannelBeta, Build: 412}}}
	f.setPointer(t, "v0.9.0-beta.2", 412)
	var moved atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/3xDevOps/Aether/", func(w http.ResponseWriter, r *http.Request) {
		moved.Add(1)
		http.Redirect(w, r, "/3xDevOps/cloudfield/"+strings.TrimPrefix(r.URL.Path, "/3xDevOps/Aether/"), http.StatusMovedPermanently)
	})
	mux.Handle("/3xDevOps/cloudfield/", http.StripPrefix("/3xDevOps/cloudfield", f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	old := srv.URL + "/3xDevOps/Aether"
	if tag, err := LatestTag(t.Context(), old+"/releases/latest"); err != nil || tag != "v0.9.0-beta.1" {
		t.Fatalf("latest under the old name = %q, %v; want v0.9.0-beta.1", tag, err)
	}
	c := NewChannelChecker(old, time.Hour, ChannelBeta, filepath.Join(t.TempDir(), "update.json"))
	got, err := c.CheckFresh(t.Context())
	if err != nil || got.Latest != "v0.9.0-beta.2" || !got.UpdateAvailable {
		t.Fatalf("beta pointer under the old name = %+v, %v; want v0.9.0-beta.2 offered", got, err)
	}
	m, err := FetchManifestFrom(t.Context(), old, got.Latest)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "aether")
	staged, err := Stage(t.Context(), m, "aether-linux-amd64", dst)
	if err != nil {
		t.Fatalf("download under the old name: %v", err)
	}
	t.Cleanup(staged.Discard)
	if body, err := os.ReadFile(staged.tmp); err != nil || string(body) != "binary" {
		t.Fatalf("staged %q, %v; want the published binary", body, err)
	}
	// latest, the pointer and its signature, the manifest and its signature
	// for the check and again here, and the asset.
	if n := moved.Load(); n != 8 {
		t.Fatalf("%d requests were redirected from the old name, want all 8", n)
	}
}
