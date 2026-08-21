package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestTagFollowsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/v1.2.3", http.StatusFound)
	}))
	defer srv.Close()

	tag, err := LatestTag(t.Context(), srv.URL+"/releases/latest")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("tag = %q, want v1.2.3", tag)
	}
}

func TestLatestTagRejectsNonReleaseRedirect(t *testing.T) {
	// A repo without releases redirects to the releases index page.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := LatestTag(t.Context(), srv.URL+"/releases/latest"); err == nil {
		t.Fatal("expected an error for a redirect to the releases index")
	}
}

func TestLatestTagRejectsNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := LatestTag(t.Context(), srv.URL+"/releases/latest"); err == nil {
		t.Fatal("expected an error when the endpoint does not redirect")
	}
}

// releaseServer serves checksums.txt and one asset the way a GitHub release
// does, with the checksum optionally poisoned.
func releaseServer(t *testing.T, asset string, body []byte, poison bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if poison {
		digest = strings.Repeat("0", 64)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", digest, asset)
	})
	mux.HandleFunc("/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyReplacesBinary(t *testing.T) {
	body := []byte("new binary contents")
	srv := releaseServer(t, "aether-linux-amd64", body, false)

	dst := filepath.Join(t.TempDir(), "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Apply(t.Context(), srv.URL, "aether-linux-amd64", dst); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("dst = %q, want %q", got, body)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	srv := releaseServer(t, "aether-linux-amd64", []byte("tampered"), true)

	dst := filepath.Join(t.TempDir(), "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Apply(t.Context(), srv.URL, "aether-linux-amd64", dst)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	got, rerr := os.ReadFile(dst)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "old" {
		t.Fatalf("dst was modified despite the mismatch: %q", got)
	}
	// The staged temp file must not be left behind.
	entries, rerr := os.ReadDir(filepath.Dir(dst))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 1 {
		t.Fatalf("staging leftovers in %s: %v", filepath.Dir(dst), entries)
	}
}

func TestApplyRejectsUnlistedAsset(t *testing.T) {
	srv := releaseServer(t, "aether-linux-amd64", []byte("x"), false)

	dst := filepath.Join(t.TempDir(), "aether")
	err := Apply(t.Context(), srv.URL, "aether-darwin-arm64", dst)
	if err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("err = %v, want an unlisted-asset error", err)
	}
}

func TestApplyRejectsMissingAsset(t *testing.T) {
	// checksums.txt lists the asset but the download 404s.
	mux := http.NewServeMux()
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), "aether-linux-amd64")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "aether")
	if err := Apply(t.Context(), srv.URL, "aether-linux-amd64", dst); err == nil {
		t.Fatal("expected an error for a missing asset")
	}
}

func TestChecksumForHandlesBinaryModeMarker(t *testing.T) {
	// sha256sum emits "hash *name" in binary mode; both forms must parse.
	sums := []byte("aaaa  aether-linux-amd64\nbbbb *aether-linux-arm64\n")
	got, err := checksumFor(sums, "aether-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbbb" {
		t.Fatalf("checksum = %q, want bbbb", got)
	}
}
