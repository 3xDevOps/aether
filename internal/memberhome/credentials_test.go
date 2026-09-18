package memberhome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCredentialIsRootConfinedAndBounded(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	home, err := manager.Path("member-1")
	if err != nil {
		t.Fatal(err)
	}
	if mkdirErr := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	want := []byte(`{"claudeAiOauth":{"accessToken":"secret"}}`)
	if writeErr := os.WriteFile(filepath.Join(home, claudeCredentialPath), want, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	got, err := manager.ReadCredential("member-1", claudeCredentialPath, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("credential = %q, want %q", got, want)
	}
	if _, err := manager.ReadCredential("member-1", claudeCredentialPath, 8); err == nil {
		t.Fatal("oversized credential was accepted")
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, claudeCredentialPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, claudeCredentialPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReadCredential("member-1", claudeCredentialPath, 1024); err == nil {
		t.Fatal("symlink credential was accepted")
	}
}

func TestReadCredentialMissingAndUnsupported(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.ReadCredential("member-1", codexCredentialPath, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("missing credential = %q, want nil", got)
	}
	if _, err := manager.ReadCredential("member-1", ".ssh/id_rsa", 1024); err == nil {
		t.Fatal("unsupported credential path accepted")
	}
}
