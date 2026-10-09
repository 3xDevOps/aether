package memberhome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCredentialIsRootConfinedAndBounded(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
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
	manager, err := New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
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

func TestReadGitHubCredentialIsRootConfinedAndBounded(t *testing.T) {
	for _, attack := range []string{"regular", "oversized", "file symlink", "parent symlink", "hardlink"} {
		t.Run(attack, func(t *testing.T) {
			manager, err := New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			home, err := manager.Path("member-1")
			if err != nil {
				t.Fatal(err)
			}
			got, err := manager.ReadCredential("member-1", githubCredentialPath, 1024)
			if err != nil || got != nil {
				t.Fatalf("missing GitHub credential: %q, %v", got, err)
			}
			dir := filepath.Join(home, ".config", "gh")
			if err = os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			outsideDir := t.TempDir()
			outside := filepath.Join(outsideDir, "hosts.yml")
			want := []byte("github.com:\n  user: octocat\n  oauth_token: test-only\n")
			if err = os.WriteFile(outside, want, 0o600); err != nil {
				t.Fatal(err)
			}
			credential := filepath.Join(home, githubCredentialPath)
			limit := int64(1024)
			switch attack {
			case "file symlink":
				err = os.Symlink(outside, credential)
			case "parent symlink":
				if err = os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outsideDir, dir)
			case "hardlink":
				err = os.Link(outside, credential)
			default:
				err = os.WriteFile(credential, want, 0o600)
				if attack == "oversized" {
					limit = 8
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err = manager.ReadCredential("member-1", githubCredentialPath, limit)
			if attack == "regular" {
				if err != nil || string(got) != string(want) {
					t.Fatalf("GitHub credential: %q, %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("%s credential accepted", attack)
			}
		})
	}
}
