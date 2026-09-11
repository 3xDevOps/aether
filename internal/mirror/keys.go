package mirror

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/crypto/ssh"
)

const (
	privateKeyFile = "private_key."
	publicKeyFile  = "public_key."
	knownHostsFile = "known_hosts."
)

type keyPaths struct {
	Dir        string
	Private    string
	Public     string
	KnownHosts string
}

type keyMaterial struct {
	Paths       keyPaths
	PublicKey   string
	Fingerprint string
}
type keyMove struct {
	source string
	target string
}

func generationPaths(root, workspace string, generation int64) (keyPaths, error) {
	if root == "" || workspace == "" || generation <= 0 {
		return keyPaths{}, errors.New("mirror key path is invalid")
	}
	if filepath.Base(workspace) != workspace || workspace == "." || workspace == ".." {
		return keyPaths{}, errors.New("mirror workspace id is invalid")
	}
	dir := filepath.Join(root, workspace)
	gen := strconv.FormatInt(generation, 10)
	return keyPaths{
		Dir:        dir,
		Private:    filepath.Join(dir, privateKeyFile+gen),
		Public:     filepath.Join(dir, publicKeyFile+gen),
		KnownHosts: filepath.Join(dir, knownHostsFile+gen),
	}, nil
}

func generateKeyMaterial(root, workspace string, generation int64, knownHosts string) (keyMaterial, error) {
	paths, err := generationPaths(root, workspace, generation)
	if err != nil {
		return keyMaterial{}, err
	}
	if mkdirErr := os.MkdirAll(paths.Dir, 0o700); mkdirErr != nil {
		return keyMaterial{}, fmt.Errorf("mirror: create key directory: %w", mkdirErr)
	}
	if chmodErr := os.Chmod(paths.Dir, 0o700); chmodErr != nil {
		return keyMaterial{}, fmt.Errorf("mirror: protect key directory: %w", chmodErr)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return keyMaterial{}, fmt.Errorf("mirror: generate deploy key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return keyMaterial{}, fmt.Errorf("mirror: encode deploy key: %w", err)
	}
	publicText := string(ssh.MarshalAuthorizedKey(sshPub))
	privateBytes, err := ssh.MarshalPrivateKey(priv, "aether mirror deploy key")
	if err != nil {
		return keyMaterial{}, fmt.Errorf("mirror: encode deploy key: %w", err)
	}
	privateText := pem.EncodeToMemory(privateBytes)

	cleanup := func(cause error) error {
		if cleanupErr := retireGeneration(root, workspace, generation); cleanupErr != nil {
			return errors.Join(cause, fmt.Errorf("mirror: retire incomplete key material: %w", cleanupErr))
		}
		return cause
	}
	if err := atomicWrite(paths.Private, privateText, 0o600); err != nil {
		return keyMaterial{}, cleanup(fmt.Errorf("mirror: write private key: %w", err))
	}
	if err := atomicWrite(paths.Public, []byte(publicText), 0o644); err != nil {
		return keyMaterial{}, cleanup(fmt.Errorf("mirror: write public key: %w", err))
	}
	if knownHosts != "" {
		if err := atomicWrite(paths.KnownHosts, []byte(knownHosts+"\n"), 0o644); err != nil {
			return keyMaterial{}, cleanup(fmt.Errorf("mirror: write known_hosts: %w", err))
		}
	}
	return keyMaterial{Paths: paths, PublicKey: publicText, Fingerprint: ssh.FingerprintSHA256(sshPub)}, nil
}

func readPublicKey(paths keyPaths) (string, error) {
	data, err := os.ReadFile(paths.Public)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func retireGeneration(root, workspace string, generation int64) error {
	paths, err := generationPaths(root, workspace, generation)
	if err != nil {
		return err
	}
	return retireKeyPaths(root, workspace, []string{paths.Private, paths.Public, paths.KnownHosts})
}

func retireWorkspaceSecrets(root, workspace string) error {
	paths, err := generationPaths(root, workspace, 1)
	if err != nil {
		return err
	}
	var matches []string
	for _, pattern := range []string{privateKeyFile + "*", publicKeyFile + "*", knownHostsFile + "*"} {
		found, globErr := filepath.Glob(filepath.Join(paths.Dir, pattern))
		if globErr != nil {
			return fmt.Errorf("mirror: find key material: %w", globErr)
		}
		matches = append(matches, found...)
	}
	if err := retireKeyPaths(root, workspace, matches); err != nil {
		return err
	}
	if err := os.Remove(paths.Dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mirror: remove key directory: %w", err)
	}
	return nil
}

// retireKeyPaths moves key files into a root-owned quarantine in one
// same-filesystem operation per file. After all moves succeed, the quarantine
// is removed immediately. If a move or removal fails, the named quarantine is
// left in place for the next startup sweep.
func retireKeyPaths(root, workspace string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	existing := make([]string, 0, len(paths))
	for _, path := range paths {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("mirror: inspect key material: %w", err)
		}
		existing = append(existing, path)
	}
	if len(existing) == 0 {
		return nil
	}

	quarantineRoot := filepath.Join(root, ".quarantine")
	if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
		return fmt.Errorf("mirror: create key quarantine: %w", err)
	}
	if err := os.Chmod(quarantineRoot, 0o700); err != nil {
		return fmt.Errorf("mirror: protect key quarantine: %w", err)
	}
	quarantineDir, err := os.MkdirTemp(quarantineRoot, workspace+"-")
	if err != nil {
		return fmt.Errorf("mirror: create key quarantine: %w", err)
	}

	moved := make([]keyMove, 0, len(existing))
	for _, source := range existing {
		target := filepath.Join(quarantineDir, filepath.Base(source))
		if err := os.Rename(source, target); err != nil {
			rollbackErr := rollbackKeyMoves(moved)
			if rollbackErr != nil {
				return fmt.Errorf("mirror: retire key material: %w", errors.Join(err, rollbackErr))
			}
			return fmt.Errorf("mirror: retire key material: %w", err)
		}
		moved = append(moved, keyMove{source: source, target: target})
	}
	if err := os.RemoveAll(quarantineDir); err != nil {
		return fmt.Errorf("mirror: remove key quarantine: %w", err)
	}
	return nil
}

func rollbackKeyMoves(moved []keyMove) error {
	var rollbackErr error
	for i := len(moved) - 1; i >= 0; i-- {
		if err := os.Rename(moved[i].target, moved[i].source); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	return rollbackErr
}

// sweepKeyQuarantines retries removal of retired key material left by a
// prior lifecycle operation. A failed sweep leaves the artifacts in place.
func sweepKeyQuarantines(root string) error {
	quarantineRoot := filepath.Join(root, ".quarantine")
	entries, err := os.ReadDir(quarantineRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mirror: inspect key quarantine: %w", err)
	}
	var sweepErr error
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(quarantineRoot, entry.Name())); err != nil {
			sweepErr = errors.Join(sweepErr, err)
		}
	}
	if sweepErr != nil {
		return fmt.Errorf("mirror: sweep key quarantine: %w", sweepErr)
	}
	if err := os.Remove(quarantineRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mirror: sweep key quarantine: %w", err)
	}
	return nil
}

func atomicWrite(dst string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mirror-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if chmodErr := tmp.Chmod(mode); chmodErr != nil {
		return chmodErr
	}
	if _, writeErr := tmp.Write(data); writeErr != nil {
		return writeErr
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		return syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	if renameErr := os.Rename(tmpName, dst); renameErr != nil {
		return renameErr
	}
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	ok = true
	return nil
}
