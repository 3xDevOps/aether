package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestEnsureIdentityCreatesDefaultKeyPair(t *testing.T) {
	home := testhome.Isolate(t)
	path, created, err := EnsureIdentity()
	if err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	if !created {
		t.Fatal("created = false on a fresh home")
	}
	want := filepath.Join(home, ".ssh", "id_ed25519")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	publicPath := path + ".pub"
	// Windows has no POSIX permission bits to check.
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, publicPath} {
			info, statErr := os.Stat(p)
			if statErr != nil {
				t.Fatalf("stat %s: %v", p, statErr)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %o, want 600", p, info.Mode().Perm())
			}
		}
	}
	private, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.ParseRawPrivateKey(private)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(mustReadFile(t, publicPath))
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	if string(signer.PublicKey().Marshal()) != string(pub.Marshal()) {
		t.Fatal("public key does not match private key")
	}
	privateBefore := string(private)
	publicBefore := string(mustReadFile(t, publicPath))
	got, created, err := EnsureIdentity()
	if err != nil {
		t.Fatalf("EnsureIdentity existing: %v", err)
	}
	if created {
		t.Fatal("created = true for an existing key")
	}
	if got != path {
		t.Fatalf("existing path = %q, want %q", got, path)
	}
	if current := string(mustReadFile(t, path)); current != privateBefore {
		t.Fatal("EnsureIdentity rewrote existing private key")
	}
	if current := string(mustReadFile(t, publicPath)); current != publicBefore {
		t.Fatal("EnsureIdentity rewrote existing public key")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
