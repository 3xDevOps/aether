package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --key is validated and resolved to an absolute path while parsing, then
// passed to cli.Link as the key choice so saved-key inheritance stays there.
func TestParseLinkArgsKey(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("deploy_key", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	opts, err := parseLinkArgs([]string{"my-server", "--key", "deploy_key", "--name", "prod"})
	if err != nil {
		t.Fatalf("parseLinkArgs: %v", err)
	}
	want := filepath.Join(cwd, "deploy_key")
	if opts.key != want {
		t.Errorf("key = %q, want %q", opts.key, want)
	}

	// Without --key the choice stays empty, so cli.Link falls back to the
	// key a previous link saved or to ~/.ssh/id_ed25519.
	opts, err = parseLinkArgs([]string{"my-server"})
	if err != nil {
		t.Fatalf("parseLinkArgs without --key: %v", err)
	}
	if opts.key != "" {
		t.Errorf("key = %q, want empty without --key", opts.key)
	}
}

// A --key path that is not there must fail on the path the user typed,
// before the dial turns it into an authentication failure naming no file.
func TestParseLinkArgsMissingKey(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here")

	_, err := parseLinkArgs([]string{"my-server", "--key", missing})
	if err == nil {
		t.Fatal("parseLinkArgs accepted a --key path that does not exist")
	}
	for _, want := range []string{"link --key", missing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}
