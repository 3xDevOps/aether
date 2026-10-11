package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestSSHArgvPutsTheRunOptionsAheadOfTheCallers(t *testing.T) {
	testhome.Isolate(t)
	argv, err := sshArgv([]string{"-L", "3000:localhost:3000", "-o", "ProxyCommand=evil", "run-gejd5cbk74", "ls", "-la"})
	if err != nil {
		t.Fatal(err)
	}
	options, err := cli.RunSSHOptions()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, o := range options {
		want = append(want, "-o", o.Key+"="+o.Value)
	}
	want = append(want, "-L", "3000:localhost:3000", "-o", "ProxyCommand=evil", "run-gejd5cbk74.aether", "ls -la")
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q\nwant   %q", argv, want)
	}
	if argv[1] != "HostName=%h" || !strings.Contains(argv[3], " ssh --stdio ") || !strings.Contains(argv[3], "%h") {
		t.Fatalf("leading options = %q, want the host name pinned and this binary as the ProxyCommand", argv[:4])
	}

	// A host already written in full is not suffixed twice.
	if argv, err = sshArgv([]string{"run-gejd5cbk74.aether"}); err != nil || argv[len(argv)-1] != "run-gejd5cbk74.aether" {
		t.Fatalf("argv for a full host name = %q %v", argv, err)
	}
	if _, err = sshArgv(nil); err == nil || !strings.Contains(err.Error(), sshUsage) {
		t.Fatalf("no run = %v, want the usage", err)
	}
}

func TestSSHStdioNeedsALink(t *testing.T) {
	testhome.Isolate(t)
	if err := runSSH([]string{"--stdio", "run-gejd5cbk74.aether"}); !errors.Is(err, cli.ErrNotLinked) {
		t.Fatalf("ssh --stdio without a link = %v, want %v", err, cli.ErrNotLinked)
	}
}

// ssh passes on whatever host name a link or a repository gave it, so a name
// that is not a run's is refused before the link is read or anything dialed.
func TestSSHRefusesAHostThatIsNotARun(t *testing.T) {
	testhome.Isolate(t)
	for _, host := range []string{"$(touch pwned).aether", "a'; touch pwned; '.aether", "-oProxyCommand=id.aether", "*.aether", "run.other.aether"} {
		for name, err := range map[string]error{
			"ssh --stdio": runSSH([]string{"--stdio", host}),
			"ssh":         runSSH([]string{"--", host, "true"}),
		} {
			if err == nil || errors.Is(err, cli.ErrNotLinked) || !strings.Contains(err.Error(), "does not name a run") {
				t.Errorf("aether %s %q = %v, want the host refused as no run", name, host, err)
			}
		}
	}
}
