package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// serveReleases points `gui build` at handler for the rest of the test.
func serveReleases(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := desktopReleases
	desktopReleases = srv.URL
	t.Cleanup(func() { desktopReleases = old })
}

func TestPrebuiltDesktopOnlyDownloadsForAWindowsRelease(t *testing.T) {
	serveReleases(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected download of %s", r.URL.Path)
		http.NotFound(w, r)
	})
	for _, tc := range []struct {
		name, goos, tag, zip string
		source               bool
		prebuilt             bool
	}{
		{name: "linux release", goos: "linux", tag: "v1.2.3"},
		{name: "windows --source", goos: "windows", tag: "v1.2.3", source: true},
		{name: "windows dev build", goos: "windows", tag: "dev"},
		{name: "windows describe build", goos: "windows", tag: "v1.2.3-4-gabcdef0"},
		{name: "windows --zip", goos: "windows", tag: "dev", zip: "app.zip", prebuilt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var phases []string
			src, remove, err := prebuiltDesktop(context.Background(), tc.goos, tc.tag, tc.zip, tc.source, t.TempDir(), &bytes.Buffer{}, func(p string) { phases = append(phases, p) })
			remove()
			if err != nil {
				t.Fatal(err)
			}
			if (src != nil) != tc.prebuilt || len(phases) != 0 {
				t.Fatalf("prebuilt = %v, phases %q; want prebuilt %v and no phases", src != nil, phases, tc.prebuilt)
			}
		})
	}
}

// A release from before the prebuilt app existed still installs: the CLI
// says why and builds from source.
func TestPrebuiltDesktopFallsBackWhenTheReleaseHasNoApp(t *testing.T) {
	const tag = "v1.2.3"
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selfupdate.TrustKeysForTest(key.Public().(ed25519.PublicKey)))
	manifest, sig, err := selfupdate.SignManifest(selfupdate.Manifest{
		Schema:  selfupdate.ManifestSchema,
		Version: tag,
		Assets:  map[string]selfupdate.Asset{"aether-windows-amd64.exe": {SHA256: strings.Repeat("0", 64), Size: 1}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	serveReleases(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/" + tag + "/" + selfupdate.ManifestName:
			_, _ = w.Write(manifest)
		case "/releases/download/" + tag + "/" + selfupdate.SignatureName:
			_, _ = w.Write(sig)
		default:
			t.Errorf("unexpected download of %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	var notes bytes.Buffer
	var phases []string
	src, remove, err := prebuiltDesktop(context.Background(), "windows", tag, "", false, t.TempDir(), &notes, func(p string) { phases = append(phases, p) })
	remove()
	if err != nil || src != nil {
		t.Fatalf("prebuiltDesktop = %v, %v; want a source build", src != nil, err)
	}
	// docs/install.md quotes this line.
	if want := "release v1.2.3 does not publish aether-desktop-windows-" + runtime.GOARCH + ".zip; building the desktop app from source\n"; notes.String() != want {
		t.Fatalf("notes = %q, want %q", notes.String(), want)
	}
	if len(phases) != 1 || phases[0] != localops.PhaseDownloading {
		t.Fatalf("phases = %q, want only %q", phases, localops.PhaseDownloading)
	}
}

// Any other failure to reach the release is an error that names the way
// around it, never a silent source build.
func TestPrebuiltDesktopReportsAnUnreachableRelease(t *testing.T) {
	serveReleases(t, http.NotFound)
	_, remove, err := prebuiltDesktop(context.Background(), "windows", "v1.2.3", "", false, t.TempDir(), &bytes.Buffer{}, func(string) {})
	remove()
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "aether gui build --source") {
		t.Fatalf("err = %v, want the 404 and the --source hint", err)
	}
}

// A prebuilt install holds the build directory's lock until it is
// released, so a concurrent `gui build` cannot swap the app under it.
func TestPrebuiltDesktopHoldsTheBuildLock(t *testing.T) {
	dir := t.TempDir()
	_, release, err := prebuiltDesktop(context.Background(), "windows", "dev", "app.zip", false, dir, &bytes.Buffer{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if unlock, lockErr := localops.LockDesktopBuild(ctx, dir); lockErr == nil {
		unlock()
		t.Fatal("the build directory was not locked during the install")
	}
	release()
	unlock, err := localops.LockDesktopBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("the lock outlived release: %v", err)
	}
	unlock()
}
