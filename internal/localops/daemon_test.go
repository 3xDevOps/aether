package localops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonInstallAndStatus(t *testing.T) {
	// os.UserHomeDir reads HOME on unix and USERPROFILE on windows, so
	// both have to point at the scratch home for this to stay hermetic
	// and to exercise the Scheduled Task rendering on windows.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	installed, unitPath, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus before install: %v", err)
	}
	if installed {
		t.Fatalf("fresh home reports installed at %s", unitPath)
	}
	if unitPath == "" {
		t.Fatal("DaemonStatus returned no unit path")
	}

	keyPath := filepath.Join(t.TempDir(), "aether_ed25519")
	path, note, err := InstallDaemon("host:2222", t.TempDir(), keyPath)
	if err != nil {
		t.Fatalf("InstallDaemon: %v", err)
	}
	if path != unitPath {
		t.Fatalf("install path %s != status path %s", path, unitPath)
	}
	if !strings.Contains(note, "activate it with: ") {
		t.Fatalf("note = %q", note)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// systemd joins argv with spaces, launchd wraps each arg in XML, and
	// the Scheduled Task puts them in <Arguments>; the server address
	// appears verbatim in all three.
	if !strings.Contains(string(body), "host:2222") {
		t.Fatalf("unit content lacks the server address: %q", body)
	}
	// The daemon dials with the linked key, not the default one.
	if !strings.Contains(string(body), keyPath) {
		t.Fatalf("unit content lacks the key path %s: %q", keyPath, body)
	}

	installed, unitPath2, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus after install: %v", err)
	}
	if !installed || unitPath2 != path {
		t.Fatalf("status after install = %v %s", installed, unitPath2)
	}
}

func TestInstallDaemonRequiresServer(t *testing.T) {
	if _, _, err := InstallDaemon("", ".", ""); err == nil {
		t.Fatal("InstallDaemon accepted an empty server")
	}
}
