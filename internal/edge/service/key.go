package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// keyFile holds the edge signing key in OpenSSH format, so that
// `ssh-keygen -lf` prints the fingerprint servers pin.
const keyFile = "edge_key"

const maxKeyFileSize = 16 << 10

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return createKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("edge: open signing key: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("edge: stat signing key: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("edge: signing key %s has mode %04o; run chmod 600 %s", path, mode, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize))
	if err != nil {
		return nil, fmt.Errorf("edge: read signing key: %w", err)
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("edge: parse signing key %s: %w", path, err)
	}
	key, ok := raw.(*ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("edge: signing key %s is %T, want an Ed25519 key", path, raw)
	}
	return *key, nil
}

func createKey(path string) (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("edge: generate signing key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(key, "aether edge")
	if err != nil {
		return nil, fmt.Errorf("edge: encode signing key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("edge: create signing key: %w", err)
	}
	_, err = f.Write(pem.EncodeToMemory(block))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path) //nolint:errcheck // the write error takes precedence
		return nil, fmt.Errorf("edge: write signing key: %w", err)
	}
	return key, nil
}

// EdgeKey is the public key grants are signed with.
func (s *Service) EdgeKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// IssueGrant signs g, valid from now for edgeproto.GrantTTL. The caller
// sets every other field.
func (s *Service) IssueGrant(g edgeproto.Grant) (string, error) {
	now := s.now()
	g.IssuedAt, g.ExpiresAt = now, now.Add(edgeproto.GrantTTL)
	return edgeproto.SignGrant(s.key, g)
}
