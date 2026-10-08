package edgeclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

// Files in the Aether config directory.
const (
	// deviceKeyFile is this machine's device key: an OpenSSH Ed25519
	// private key, mode 0600, used for nothing but edge links.
	deviceKeyFile = "edge-device-key"
	// TokensFile holds the device token of every edge this machine is
	// signed in to, mode 0600.
	TokensFile = "edge-tokens.json"
	// lockFileName serializes reads and changes to the device key and
	// TokensFile between aether processes, such as status during sign-in.
	lockFileName = "edge.lock"
)

// maxFileSize bounds what is read of the device key and TokensFile.
const maxFileSize = 1 << 20

// Session is what a sign-in left on this machine, without the token.
// SigninOrigin is the sign-in origin that issued the token.
type Session struct {
	SigninOrigin string                `json:"signin_origin"`
	Device       edgeproto.Device      `json:"device"`
	Account      edgeproto.AccountInfo `json:"account"`
}

// stored is one edge's entry in TokensFile, keyed by its relay origin.
// Token goes to that relay origin and to SigninOrigin, never to an origin
// the edge's metadata names later: a sign-in origin changes only with a
// new sign-in.
type stored struct {
	Token string `json:"token"`
	Session
}

type tokensFile struct {
	Edges map[string]stored `json:"edges"`
}

func deviceKeyPath(dir string) string { return filepath.Join(dir, deviceKeyFile) }

// DeviceSigner loads this machine's device key.
func DeviceSigner(dir string) (ssh.Signer, error) {
	unlock, err := lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return deviceSignerLocked(dir)
}

// deviceSignerLocked requires the caller to hold dir's lock.
func deviceSignerLocked(dir string) (ssh.Signer, error) {
	path := deviceKeyPath(dir)
	raw, err := readPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no device key at %s; sign in with: aether login", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read device key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse device key %s: %w", path, err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("device key %s is %s, want %s", path, signer.PublicKey().Type(), ssh.KeyAlgoED25519)
	}
	return signer, nil
}

// EnsureDeviceKey returns this machine's device key, creating it on first
// use. An existing key is never rewritten: the server knows the device by
// it.
func EnsureDeviceKey(dir string) (ssh.Signer, error) {
	// Under the lock a concurrent sign-in waits for this key to be
	// written instead of reading it half written.
	unlock, err := lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	file, err := os.OpenFile(deviceKeyPath(dir), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return deviceSignerLocked(dir)
	}
	if err != nil {
		return nil, fmt.Errorf("create device key: %w", err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(private, "aether device key")
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("marshal device key: %w", err)
	}
	if _, err = file.Write(pem.EncodeToMemory(block)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write device key: %w", err)
	}
	if err = file.Close(); err != nil {
		return nil, fmt.Errorf("write device key: %w", err)
	}
	return ssh.NewSignerFromKey(private)
}

func tokensPath(dir string) string { return filepath.Join(dir, TokensFile) }

func readTokens(dir string) (tokensFile, error) {
	unlock, err := lock(dir)
	if err != nil {
		return tokensFile{}, err
	}
	defer unlock()
	return readTokensLocked(dir)
}

// readTokensLocked requires the caller to hold dir's lock. Readers must
// not hold TokensFile open while a writer replaces it on Windows.
func readTokensLocked(dir string) (tokensFile, error) {
	var f tokensFile
	raw, err := readPrivate(tokensPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return tokensFile{Edges: map[string]stored{}}, nil
	}
	if err != nil {
		return f, fmt.Errorf("read device tokens: %w", err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", tokensPath(dir), err)
	}
	if f.Edges == nil {
		f.Edges = map[string]stored{}
	}
	return f, nil
}

// updateTokens applies change to TokensFile under the lock, so that two
// processes changing it at once both keep their change.
func updateTokens(dir string, change func(tokensFile)) error {
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := readTokensLocked(dir)
	if err != nil {
		return err
	}
	change(f)
	return writeTokens(dir, f)
}

// writeTokens replaces TokensFile atomically: a crash leaves the old file
// or the new one, never a torn token. The caller must hold dir's lock.
func writeTokens(dir string, f tokensFile) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	// CreateTemp opens the file 0600 before anything is written to it.
	tmp, err := os.CreateTemp(dir, TokensFile+".*")
	if err != nil {
		return fmt.Errorf("write device tokens: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write device tokens: %w", err)
	}
	if err := os.Rename(tmp.Name(), tokensPath(dir)); err != nil {
		return fmt.Errorf("write device tokens: %w", err)
	}
	return nil
}

// session returns the stored sign-in for this client's edge.
func (c *Client) session() (stored, error) {
	f, err := readTokens(c.dir)
	if err != nil {
		return stored{}, err
	}
	s, ok := f.Edges[c.origin]
	if !ok {
		return stored{}, fmt.Errorf("%w to %s; run: aether login --edge %s", ErrNotSignedIn, c.host, c.origin)
	}
	if origin, err := edgeproto.Origin(s.SigninOrigin); !edgeproto.ValidToken(s.Token) || err != nil || origin != s.SigninOrigin {
		return stored{}, fmt.Errorf("the device token for %s in %s is malformed; run: aether login --edge %s",
			c.host, tokensPath(c.dir), c.origin)
	}
	return s, nil
}

// Session returns this machine's sign-in to the client's edge, without
// its token.
func (c *Client) Session() (Session, error) {
	s, err := c.session()
	return s.Session, err
}

// SignedIn lists the origins of the edges this machine holds a device
// token for, sorted.
func SignedIn(dir string) ([]string, error) {
	f, err := readTokens(dir)
	if err != nil {
		return nil, err
	}
	origins := make([]string, 0, len(f.Edges))
	for origin := range f.Edges {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	return origins, nil
}

// readPrivate reads a file that holds a credential. Like OpenSSH, it
// refuses one that other users can open, and names the command that fixes
// it; Windows has no such mode bits.
func readPrivate(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if runtime.GOOS != "windows" {
		info, serr := f.Stat()
		if serr != nil {
			return nil, serr
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return nil, fmt.Errorf("%s has mode %04o, open to other users; run: chmod 600 %s",
				path, mode, shellquote.Quote(path))
		}
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxFileSize)
	}
	return raw, nil
}

// lock serializes reads and changes to the files in dir and returns its
// release. The operating system drops it if the process dies.
func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	path := filepath.Join(dir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	unlock, err := lockFile(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		unlock()
		_ = f.Close()
	}, nil
}
