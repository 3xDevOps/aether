// Package signing keeps each member's commit signing key in the server's
// data directory, which no container mounts, and lends it to the member's
// runs and environment through a read-only SSH agent.
package signing

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
)

// migratedName marks that the home keys of earlier releases were moved. A
// member ID never starts with a dot.
const migratedName = ".migrated"

type Service struct {
	keys    string
	sockets string
	homes   *memberhome.Manager

	keyMu sync.Mutex

	mu        sync.Mutex
	closed    bool
	listeners map[domain.MemberID]*net.UnixListener
	conns     map[net.Conn]struct{}
	wg        sync.WaitGroup
}

// New keeps one private key per member in keys, which nothing mounts, and
// one agent socket directory per member in sockets.
func New(keys, sockets string, homes *memberhome.Manager) (*Service, error) {
	s := &Service{
		keys: keys, sockets: sockets, homes: homes,
		listeners: make(map[domain.MemberID]*net.UnixListener), conns: make(map[net.Conn]struct{}),
	}
	for _, d := range []string{keys, sockets} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("signing: create %s: %w", d, err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return nil, fmt.Errorf("signing: set mode on %s: %w", d, err)
		}
	}
	return s, nil
}

// Key returns the member's private signing key, or nil when they have none.
func (s *Service) Key(member domain.MemberID) ([]byte, error) {
	path, err := s.keyPath(member)
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("signing: read the key for %q: %w", member, err)
	}
	return key, nil
}

// Ensure returns the member's public key line, generating the key on first
// call. It writes the public half to ~/.ssh/aether_signing.pub and points
// the home's git at it, so commits in the member's containers sign through
// the agent.
func (s *Service) Ensure(ctx context.Context, member domain.MemberID, identity domain.GitIdentity) (string, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	key, err := s.Key(member)
	if err != nil {
		return "", err
	}
	if key == nil {
		if key, err = generate(member); err != nil {
			return "", err
		}
		if err = s.store(member, key); err != nil {
			return "", err
		}
	}
	line, err := publicLine(key, member)
	if err != nil {
		return "", err
	}
	if err := s.homes.WriteSigningPublicKey(member, line); err != nil {
		return "", err
	}
	if err := s.homes.ConfigureGit(ctx, member, identity, line); err != nil {
		return "", err
	}
	return line, nil
}

// Remove deletes the member's private key and stops their agent. Removing a
// key that does not exist is a successful no-op.
func (s *Service) Remove(member domain.MemberID) error {
	path, err := s.keyPath(member)
	if err != nil {
		return err
	}
	if err := s.StopAgent(member); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("signing: remove the key for %q: %w", member, err)
	}
	return nil
}

// Archive moves the member's key to dest and reports whether they had one.
// Their agent keeps its socket, so a container already planned with it
// still starts if the key comes back; StopAgent ends it for good.
func (s *Service) Archive(member domain.MemberID, dest string) (bool, error) {
	path, err := s.keyPath(member)
	if err != nil {
		return false, err
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	err = os.Rename(path, dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("signing: archive the key for %q: %w", member, err)
	}
	return true, nil
}

// Unarchive moves a key Archive moved to src back.
func (s *Service) Unarchive(member domain.MemberID, src string) error {
	path, err := s.keyPath(member)
	if err != nil {
		return err
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if err := os.Rename(src, path); err != nil {
		return fmt.Errorf("signing: restore the key for %q from %s: %w", member, src, err)
	}
	return nil
}

// ConfigureGit rewrites the identity in the home's .gitconfig of a member
// who has a key. Without one there are no signing settings to keep in step,
// so the home is left alone.
func (s *Service) ConfigureGit(ctx context.Context, member domain.MemberID, identity domain.GitIdentity) error {
	key, err := s.Key(member)
	if err != nil || key == nil {
		return err
	}
	line, err := publicLine(key, member)
	if err != nil {
		return err
	}
	return s.homes.ConfigureGit(ctx, member, identity, line)
}

// Migrate moves each member's key from ~/.ssh/aether_signing, where earlier
// releases kept it, into the data directory, deletes it from the home and
// points the home's git at the agent. It runs once: a key planted in a home
// afterwards is never adopted, so a run cannot choose the key Aether signs
// with or registers on GitHub.
func (s *Service) Migrate(ctx context.Context, members []*domain.Member) error {
	marker := filepath.Join(s.keys, migratedName)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("signing: check %s: %w", marker, err)
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	for _, m := range members {
		if err := s.migrate(ctx, m); err != nil {
			return err
		}
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return fmt.Errorf("signing: write %s: %w", marker, err)
	}
	return syncDir(s.keys)
}

// migrate leaves a home key it cannot read or parse where it is: the home
// is container-written, and nothing unusable is worth stopping the server
// for or deleting.
func (s *Service) migrate(ctx context.Context, m *domain.Member) error {
	legacy, err := s.homes.LegacySigningKey(m.ID)
	if err != nil {
		slog.Warn("signing: the home signing key cannot be read, so it stays in the home; aether github connect makes a new one",
			"member", m.ID, "error", err)
		return nil
	}
	if legacy == nil {
		return nil
	}
	if _, err = ssh.ParsePrivateKey(legacy); err != nil {
		slog.Warn("signing: the home signing key cannot be parsed, so it stays in the home; aether github connect makes a new one",
			"member", m.ID, "error", err)
		return nil
	}
	key, err := s.Key(m.ID)
	if err != nil {
		return err
	}
	if key == nil {
		if err = s.store(m.ID, legacy); err != nil {
			return err
		}
		key = legacy
	}
	line, err := publicLine(key, m.ID)
	if err != nil {
		return err
	}
	// Neither home write may stop the server: the home is container-written.
	// A .gitconfig still naming ~/.ssh/aether_signing keeps signing, because
	// ssh-keygen falls back to the .pub beside a missing key and finds that
	// key in the agent.
	if err = s.homes.WriteSigningPublicKey(m.ID, line); err != nil {
		slog.Warn("signing: rewrite the home's signing public key", "member", m.ID, "error", err)
	}
	if err = s.homes.ConfigureGit(ctx, m.ID, m.GitIdentity(), line); err != nil {
		slog.Warn("signing: point the home's git at the signing agent", "member", m.ID, "error", err)
	}
	return s.homes.RemoveLegacySigningKey(m.ID)
}

// store is durable on return: migration deletes the home's copy next.
func (s *Service) store(member domain.MemberID, key []byte) error {
	path, err := s.keyPath(member)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.keys, ".tmp-")
	if err != nil {
		return fmt.Errorf("signing: store the key for %q: %w", member, err)
	}
	_, err = f.Write(key)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("signing: store the key for %q: %w", member, err)
	}
	return syncDir(s.keys)
}

func (s *Service) keyPath(member domain.MemberID) (string, error) {
	name := string(member)
	if name == "" || strings.HasPrefix(name, ".") || filepath.Base(name) != name {
		return "", fmt.Errorf("signing: invalid member ID %q", member)
	}
	return filepath.Join(s.keys, name), nil
}

func generate(member domain.MemberID) ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("signing: generate the key for %q: %w", member, err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment(member))
	if err != nil {
		return nil, fmt.Errorf("signing: encode the key for %q: %w", member, err)
	}
	return pem.EncodeToMemory(block), nil
}

func publicLine(key []byte, member domain.MemberID) (string, error) {
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("signing: parse the key for %q: %w", member, err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + comment(member), nil
}

func comment(member domain.MemberID) string { return "aether " + string(member) }

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("signing: sync %s: %w", dir, err)
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("signing: sync %s: %w", dir, err)
	}
	return nil
}
