package localops

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// releaseServer serves tag's manifest, signed by a key the test trusts,
// listing assets, and serves body for every asset download.
func releaseServer(t *testing.T, tag string, assets map[string][]byte, body func(name string) []byte) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selfupdate.TrustKeysForTest(key.Public().(ed25519.PublicKey)))
	// A manifest has to list something; the CLI stands in for the rest.
	m := selfupdate.Manifest{Schema: selfupdate.ManifestSchema, Version: tag, Assets: map[string]selfupdate.Asset{
		"aether-windows-amd64.exe": {SHA256: strings.Repeat("0", 64), Size: 1},
	}}
	for name, data := range assets {
		sum := sha256.Sum256(data)
		m.Assets[name] = selfupdate.Asset{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	}
	manifest, sig, err := selfupdate.SignManifest(m, key)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/releases/download/" + tag + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, prefix)
		switch {
		case !ok:
			http.NotFound(w, r)
		case name == selfupdate.ManifestName:
			_, _ = w.Write(manifest)
		case name == selfupdate.SignatureName:
			_, _ = w.Write(sig)
		default:
			_, _ = w.Write(body(name))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDownloadDesktopChecksTheSignedManifest(t *testing.T) {
	const tag = "v1.2.3"
	name := selfupdate.DesktopAssetName("arm64")
	app := zipped(t, []archiveFile{{name: desktopExe, body: "exe"}})
	served := app
	base := releaseServer(t, tag, map[string][]byte{name: app}, func(string) []byte { return served })
	dir := t.TempDir()

	archive, err := DownloadDesktop(context.Background(), base, tag, "arm64", dir, io.Discard)
	if err != nil {
		t.Fatalf("DownloadDesktop: %v", err)
	}
	if got, readErr := os.ReadFile(archive); readErr != nil || string(got) != string(app) {
		t.Fatalf("downloaded %d bytes (%v), want the %d the release published", len(got), readErr, len(app))
	}
	_ = os.Remove(archive)

	served = append([]byte(nil), app...)
	served[len(served)-1] ^= 0xff
	_, err = DownloadDesktop(context.Background(), base, tag, "arm64", dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a tampered zip was accepted: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("a rejected download was left behind: %v", left)
	}
}

func TestDownloadDesktopReportsAReleaseWithoutTheApp(t *testing.T) {
	base := releaseServer(t, "v1.2.3", nil, func(string) []byte {
		t.Error("nothing should be downloaded from a release without the app")
		return nil
	})
	_, err := DownloadDesktop(context.Background(), base, "v1.2.3", "amd64", t.TempDir(), io.Discard)
	if !errors.Is(err, ErrNoPrebuiltDesktop) || !strings.Contains(err.Error(), selfupdate.DesktopAssetName("amd64")) {
		t.Fatalf("err = %v, want ErrNoPrebuiltDesktop naming the asset", err)
	}
}

// The zip is the whole app, installed through the same staging and swap as
// a local build, with the CLI recorded beside it rather than inside the
// prebuilt asar.
func TestInstallDesktopFromZipRecordsTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux layout uses unix permissions")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	archive := writeArchive(t, zipped(t, []archiveFile{
		{name: desktopExe, body: "exe"},
		{name: "resources/app.asar", body: "asar"},
	}), "app.zip")
	cli := filepath.Join(home, ".local", "bin", "aether")

	app, err := InstallDesktop("linux", home, DesktopFromZip(archive), cli, []byte("png"))
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(app.App, "resources", "app.asar")); readErr != nil || string(got) != "asar" {
		t.Fatalf("resources/app.asar = %q, %v", got, readErr)
	}
	raw, err := os.ReadFile(filepath.Join(app.App, "resources", "aether-cli.json"))
	if err != nil {
		t.Fatal(err)
	}
	var binding struct{ Path string }
	if err := json.Unmarshal(raw, &binding); err != nil || binding.Path != cli {
		t.Fatalf("aether-cli.json = %s (%v), want path %q", raw, err, cli)
	}
}

func TestDesktopFromZipRefusesWhatIsNotTheApp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []archiveFile
		want  string
	}{
		{"no executable", []archiveFile{{name: "win-unpacked/" + desktopExe, body: "exe"}}, "no aether-desktop.exe at its root"},
		{"escaping entry", []archiveFile{{name: desktopExe, body: "exe"}, {name: "../escaped", body: "x"}}, "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "staged")
			err := DesktopFromZip(writeArchive(t, zipped(t, tc.files), "app.zip"))(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped")); !os.IsNotExist(err) {
				t.Fatalf("an entry escaped the staging directory: %v", err)
			}
		})
	}
}

func TestExtractZipStopsAtItsSizeLimit(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	archive := writeArchive(t, zipped(t, []archiveFile{
		{name: "a", body: "12345"},
		{name: "b", body: "67890"},
	}), "big.zip")
	if err := extractZip(archive, root, 8); err == nil || !strings.Contains(err.Error(), "expands past 8 bytes") {
		t.Fatalf("err = %v, want the size limit", err)
	}
}
