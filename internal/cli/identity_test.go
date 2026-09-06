package cli

import (
	"os"
	"path/filepath"
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
	privateInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Errorf("private mode = %o, want 600", privateInfo.Mode().Perm())
	}
	publicPath := path + ".pub"
	publicInfo, err := os.Stat(publicPath)
	if err != nil {
		t.Fatalf("stat public key: %v", err)
	}
	if publicInfo.Mode().Perm() != 0o600 {
		t.Errorf("public mode = %o, want 600", publicInfo.Mode().Perm())
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
