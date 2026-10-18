package selfupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

// stageFrom stages asset of tag v1.3.0 from srv over a fresh dst holding
// "old", fetching the manifest the way updateBinaries does.
func stageFrom(t *testing.T, srv *httptest.Server, asset string) (string, error) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := fetchManifest(t.Context(), srv.URL, "v1.3.0")
	if err != nil {
		return dst, err
	}
	staged, err := Stage(t.Context(), m, asset, dst)
	if err != nil {
		return dst, err
	}
	defer staged.Discard()
	return dst, staged.Commit()
}

// assertUntouched fails unless dst still holds "old" and nothing was left
// staged beside it.
func assertUntouched(t *testing.T, dst string) {
	t.Helper()
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("dst was modified: %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("staging leftovers in %s: %v", filepath.Dir(dst), entries)
	}
}

func TestStageReplacesBinary(t *testing.T) {
	body := []byte("new binary contents")
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{"aether-linux-amd64": body})

	dst, err := stageFrom(t, srv, "aether-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("dst = %q, want %q", got, body)
	}
	// Windows has no execute bit: os.Chmod there only toggles the
	// read-only attribute, so a writable file always reports 0666.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
		}
	}
}

func TestStageRejectsChecksumMismatch(t *testing.T) {
	f, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{"aether-linux-amd64": []byte("released")})
	f.served["aether-linux-amd64"] = []byte("tampered")

	dst, err := stageFrom(t, srv, "aether-linux-amd64")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	assertUntouched(t, dst)
}

func TestStageRejectsSizeMismatch(t *testing.T) {
	f, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{"aether-linux-amd64": []byte("released")})
	f.served["aether-linux-amd64"] = []byte("released plus a tail")

	dst, err := stageFrom(t, srv, "aether-linux-amd64")
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("err = %v, want a size mismatch", err)
	}
	assertUntouched(t, dst)
}

func TestStageRejectsUnlistedAsset(t *testing.T) {
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{"aether-linux-amd64": []byte("x")})

	_, err := stageFrom(t, srv, "aether-darwin-arm64")
	if err == nil || !strings.Contains(err.Error(), "does not publish aether-darwin-arm64") {
		t.Fatalf("err = %v, want an unlisted-asset error", err)
	}
}

func TestStageRejectsMissingAsset(t *testing.T) {
	// The manifest lists the asset; the release serves nothing.
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{})
	m := manifestFor("v1.3.0", map[string][]byte{"aether-linux-amd64": []byte("x")})
	m.baseURL = srv.URL
	dst := filepath.Join(t.TempDir(), "aether")
	_, err := Stage(t.Context(), m, "aether-linux-amd64", dst)
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("err = %v, want the 404", err)
	}
}

func TestUpdateRefusesAReleaseWithoutAManifest(t *testing.T) {
	f, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{"aether-" + runtime.GOOS + "-" + runtime.GOARCH: []byte("new")})
	f.unsigned = true
	self := filepath.Join(t.TempDir(), "aether")
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := UpdateBinaries(t.Context(), srv.URL, "v1.3.0", self, "aether")
	if err == nil || !strings.Contains(err.Error(), "v1.3.0") || !strings.Contains(err.Error(), "predates signed releases") {
		t.Fatalf("err = %v, want it to name v1.3.0 as predating signed releases", err)
	}
	assertUntouched(t, self)
}

// TestUpdateBinariesStagesEveryAssetFromTheManifest drives the whole path:
// the manifest is fetched and verified, then every binary is streamed,
// checked against it, and swapped in.
func TestUpdateBinariesStagesEveryAssetFromTheManifest(t *testing.T) {
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		suffix += ".exe"
	}
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{
		"aether" + suffix:        []byte("new cli"),
		"aether-server" + suffix: []byte("new server"),
	})
	dir := t.TempDir()
	self := filepath.Join(dir, "aether")
	for _, name := range []string{"aether", "aether-server"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	replaced, err := UpdateBinaries(t.Context(), srv.URL, "v1.3.0", self, "aether", "aether-server")
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 2 {
		t.Fatalf("replaced = %v, want both binaries", replaced)
	}
	for name, want := range map[string]string{"aether": "new cli", "aether-server": "new server"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestChecksumForHandlesBinaryModeMarker(t *testing.T) {
	// sha256sum emits "hash *name" in binary mode; both forms must parse.
	sums := []byte("aaaa  aether-linux-amd64\nbbbb *aether-linux-arm64\n")
	got, err := ChecksumFor(sums, "aether-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbbb" {
		t.Fatalf("checksum = %q, want bbbb", got)
	}
}

func TestSwapAsideKeepsTheOldBinaryBesideTheNewOne(t *testing.T) {
	dir := t.TempDir()
	dst, tmp := filepath.Join(dir, "aether.exe"), filepath.Join(dir, "staged")
	for path, body := range map[string]string{dst: "old", tmp: "new", dst + ".old": "older"} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := swapAside(tmp, dst); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{dst: "new", dst + ".old": "old"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file still exists: %v", err)
	}
}

func TestSwapAsideInstallsWhereNothingExists(t *testing.T) {
	dir := t.TempDir()
	dst, tmp := filepath.Join(dir, "aether.exe"), filepath.Join(dir, "staged")
	if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapAside(tmp, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "new" {
		t.Fatalf("%s = %q, %v; want new", dst, got, err)
	}
}

func TestSwapAsideRestoresTheBinaryWhenTheStagedFileIsGone(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "aether.exe")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := swapAside(filepath.Join(dir, "missing"), dst); err == nil {
		t.Fatal("swap succeeded without a staged file")
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "old" {
		t.Fatalf("%s = %q, %v; want the old binary back", dst, got, err)
	}
	if _, err := os.Stat(dst + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s.old left behind: %v", dst, err)
	}
}
