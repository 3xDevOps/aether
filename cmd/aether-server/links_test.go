package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyServeUnderSystemdLinks runs this binary, installed as
// aether-server beside an aether, under systemd's INVOCATION_ID in every
// mode a session runs it in: none creates a link. `serve` does, which is
// what a release that an older updater installed relies on to complete
// the layout at its first start.
func TestOnlyServeUnderSystemdLinks(t *testing.T) {
	if argv := os.Getenv("AETHER_SERVER_TEST_ARGV"); argv != "" {
		os.Args = strings.Split(argv, "\x1f")
		main()
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("as root, serve would link the host's own systemd unit")
	}
	run := func(t *testing.T, dir string, argv ...string) {
		t.Helper()
		cmd := exec.Command(filepath.Join(dir, "aether-server"), "-test.run=^TestOnlyServeUnderSystemdLinks$")
		cmd.Env = append(os.Environ(), "INVOCATION_ID=0123456789abcdef", "HOME="+t.TempDir(),
			"AETHER_SERVER_TEST_ARGV="+strings.Join(argv, "\x1f"))
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		t.Logf("%v: %v\n%s", argv, err, out)
	}
	install := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		self, err := os.Open(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = self.Close() }()
		f, err := os.OpenFile(filepath.Join(dir, "aether-server"), os.O_CREATE|os.O_WRONLY, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(f, self); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "aether"), []byte("the cli"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	links := func(dir string) []string {
		found, _ := filepath.Glob(filepath.Join(dir, "cloudfield*"))
		return found
	}

	dir := install(t)
	for _, argv := range [][]string{
		{"/usr/local/bin/sudo"},
		{"/usr/local/bin/aether-internal", "--help"},
		{"/usr/local/bin/cloudfield-internal", "--help"},
		{"aether-server", "dev-exec"},
		{"aether-server", "acp-keeper"},
		{"aether-server", "guest"},
		{"aether-server", "vm-monitor"},
		{"aether-server", "mcp", "-h"},
	} {
		run(t, dir, argv...)
		if got := links(dir); len(got) != 0 {
			t.Fatalf("%v created %v", argv, got)
		}
	}

	run(t, dir, "aether-server", "serve", "-h")
	for link, target := range map[string]string{"cloudfield": "aether", "cloudfield-server": "aether-server"} {
		if got, err := os.Readlink(filepath.Join(dir, link)); err != nil || got != target {
			t.Fatalf("after serve under systemd, %s -> %q, %v; want %s", link, got, err, target)
		}
	}
}
