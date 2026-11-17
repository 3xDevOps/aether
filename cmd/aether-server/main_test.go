package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionCLINames(t *testing.T) {
	if name := os.Getenv("AETHER_SERVER_ARGV0"); name != "" {
		os.Args = []string{"/usr/local/bin/" + name, "--help"}
		main()
		return
	}
	help := func(name string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionCLINames$")
		cmd.Env = append(os.Environ(), "AETHER_SERVER_ARGV0="+name)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s --help: %v, stderr %q", name, err, stderr.String())
		}
		return string(out)
	}
	server, old := help("aether-server"), help("aether-internal")
	if old == server || !strings.HasPrefix(old, "usage: aether-internal <command>") {
		t.Fatalf("aether-internal --help printed the server usage:\n%s", old)
	}
	// Usage shows the name the command was run by; no help line names a
	// persisted path.
	if got, want := help("cloudfield-internal"), strings.ReplaceAll(old, "aether-internal", "cloudfield-internal"); got != want {
		t.Fatalf("cloudfield-internal --help =\n%s\nwant the aether-internal help under its own name:\n%s", got, want)
	}
}
