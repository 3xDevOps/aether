package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// LoadOrCreateHostKey loads the server host key from path, generating an
// ed25519 key (OpenSSH format via ssh.MarshalPrivateKey, 0600, directory
// 0700) on first use.
func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		signer, perr := ssh.ParsePrivateKey(raw)
		if perr != nil {
			return nil, fmt.Errorf("sshd: parse host key %s: %w", path, perr)
		}
		return signer, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("sshd: read host key %s: %w", path, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sshd: generate host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("sshd: marshal host key: %w", err)
	}
	if merr := os.MkdirAll(filepath.Dir(path), 0o700); merr != nil {
		return nil, fmt.Errorf("sshd: create host key dir: %w", merr)
	}
	if werr := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); werr != nil {
		return nil, fmt.Errorf("sshd: write host key %s: %w", path, werr)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("sshd: host key signer: %w", err)
	}
	return signer, nil
}
