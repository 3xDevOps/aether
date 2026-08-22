package localops

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDaemonInstallAndStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME override does not steer os.UserHomeDir on windows")
	}
	t.Setenv("HOME", t.TempDir())

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

	path, note, err := InstallDaemon("host:2222", t.TempDir())
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
	// systemd joins argv with spaces, launchd wraps each arg in XML;
	// the server address appears verbatim in both.
	if !strings.Contains(string(body), "host:2222") {
		t.Fatalf("unit content lacks the server address: %q", body)
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
	if _, _, err := InstallDaemon("", "."); err == nil {
		t.Fatal("InstallDaemon accepted an empty server")
	}
}
