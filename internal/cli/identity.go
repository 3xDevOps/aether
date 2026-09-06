package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// EnsureIdentity returns ~/.ssh/id_ed25519 and whether it was created by this
// call. An existing private key is never rewritten.
func EnsureIdentity() (string, bool, error) {
	path := defaultPath(".ssh", "id_ed25519")
	if path == "" {
		return "", false, errors.New("cli: home directory required for SSH identity")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, fmt.Errorf("cli: create SSH directory: %w", err)
	}
	// O_EXCL makes a concurrent `aether link` and dashboard link.apply
	// agree on one key instead of the second silently replacing the first.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return path, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cli: create SSH identity: %w", err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = file.Close()
		return "", false, fmt.Errorf("cli: generate SSH identity: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		_ = file.Close()
		return "", false, fmt.Errorf("cli: marshal SSH identity: %w", err)
	}
	if _, err = file.Write(pem.EncodeToMemory(block)); err != nil {
		_ = file.Close()
		return "", false, fmt.Errorf("cli: write SSH identity: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", false, fmt.Errorf("cli: write SSH identity: %w", err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		return "", false, fmt.Errorf("cli: marshal SSH public key: %w", err)
	}
	if err = os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(public), 0o600); err != nil {
		return "", false, fmt.Errorf("cli: write SSH public key: %w", err)
	}
	return path, true, nil
}
