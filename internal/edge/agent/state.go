package edgeagent

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// StateDir is where the agent keeps its state under a server data
// directory. Every directory is 0700 and every file 0600, and a root
// process gives what it creates to the data directory's owner.
func StateDir(dataDir string) string { return filepath.Join(dataDir, "edge") }

const (
	pinFile    = "edge_key.json"
	ownerFile  = "owner.json"
	claimFile  = "claim.json"
	statusFile = "status.json"
	lockFile   = "lock"
	// keysDir holds one directory per edge key the server has pinned,
	// with the owner it has at the edge holding that key.
	keysDir = "keys"
)

// State is the agent's persistent state: the pinned edge key, the owner,
// the claim code and the agent's last status. The serve process and the
// aether-server edge commands share it, so every method reads the files
// afresh.
//
// Trust in an edge rests on its signing key, not on a host name, so none
// of it depends on edge-url: a server moved to another host name of the
// same edge keeps its pin and owner. The owner is kept per edge key: an
// edge with another key, once `aether-server edge trust` pins it, starts
// without one, and pinning the first edge's key again finds its owner.
type State struct{ dir string }

// OpenState returns the state under dataDir. Nothing is created until a
// method writes.
func OpenState(dataDir string) *State { return &State{dir: StateDir(dataDir)} }

// Status is what the agent last reported about its control connection.
type Status struct {
	Edge      string    `json:"edge"`
	Connected bool      `json:"connected"`
	Error     string    `json:"error,omitempty"`
	Since     time.Time `json:"since"`
}

// ClaimCode describes the current claim code. The code itself is shown
// only when it is issued: the state keeps its hash. Admin, when set, is
// the existing admin member the claim binds the claiming account to.
type ClaimCode struct {
	Hash         string    `json:"hash"`
	ExpiresAt    time.Time `json:"expires_at"`
	AttemptsLeft int       `json:"attempts_left"`
	Admin        string    `json:"admin,omitempty"`
}

// Usable reports whether the code can still claim the server at now.
func (c ClaimCode) Usable(now time.Time) bool {
	return c.Hash != "" && c.AttemptsLeft > 0 && !now.After(c.ExpiresAt)
}

type pin struct {
	Key ed25519.PublicKey `json:"key"`
}

// PinnedKey returns the pinned edge key, nil when none is pinned.
func (s *State) PinnedKey() (ed25519.PublicKey, error) {
	var p pin
	ok, err := s.read(pinFile, &p)
	if err != nil || !ok {
		return nil, err
	}
	if len(p.Key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("edgeagent: %s holds a %d-byte key, want %d", s.path(pinFile), len(p.Key), ed25519.PublicKeySize)
	}
	return p.Key, nil
}

// Pin replaces the pinned edge key.
func (s *State) Pin(key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("edgeagent: edge key is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	return s.write(pinFile, pin{Key: key})
}

// Owner returns the account that owns this server at the edge holding
// the pinned key, by a claim or a transfer, nil when it has none or no
// key is pinned.
func (s *State) Owner() (*edgeproto.Account, error) {
	name, err := s.ownerName()
	if err != nil || name == "" {
		return nil, err
	}
	var a edgeproto.Account
	ok, err := s.read(name, &a)
	if err != nil || !ok {
		return nil, err
	}
	return &a, nil
}

// ClearOwner forgets the owner, which leaves the server unclaimed at the
// edge. Members keep their access to the server itself.
func (s *State) ClearOwner() error {
	name, err := s.ownerName()
	if err != nil || name == "" {
		return err
	}
	return s.remove(name)
}

// ownerName is the owner file of the pinned key, empty when none is
// pinned.
func (s *State) ownerName() (string, error) {
	key, err := s.PinnedKey()
	if err != nil || key == nil {
		return "", err
	}
	id := strings.NewReplacer("+", "-", "/", "_").Replace(strings.TrimPrefix(edgeproto.EdgeKeyFingerprint(key), "SHA256:"))
	return filepath.Join(keysDir, "sha256-"+id, ownerFile), nil
}

// writeOwner records owner for the pinned key.
func (s *State) writeOwner(owner edgeproto.Account) error {
	name, err := s.ownerName()
	if err != nil {
		return err
	}
	if name == "" {
		return errors.New("edgeagent: no edge key is pinned, so no owner can be recorded")
	}
	return s.write(name, owner)
}

// ClaimCode returns the current claim code's record, false when there is
// none.
func (s *State) ClaimCode() (ClaimCode, bool, error) {
	var c ClaimCode
	ok, err := s.read(claimFile, &c)
	return c, ok, err
}

// IssueClaimCode replaces any claim code with a fresh one for serverID,
// valid from now for ClaimCodeTTL and ClaimCodeAttempts attempts. admin,
// when not empty, names the existing admin member the claim binds the
// claiming account to.
func (s *State) IssueClaimCode(serverID, admin string, now time.Time) (string, time.Time, error) {
	code, err := edgeproto.NewClaimCode(serverID)
	if err != nil {
		return "", time.Time{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return "", time.Time{}, err
	}
	defer unlock()
	c := ClaimCode{Hash: edgeproto.HashToken(code), ExpiresAt: now.Add(edgeproto.ClaimCodeTTL), AttemptsLeft: edgeproto.ClaimCodeAttempts,
		Admin: admin}
	if err := s.write(claimFile, c); err != nil {
		return "", time.Time{}, err
	}
	return code, c.ExpiresAt, nil
}

// RemoveClaimCode destroys the claim code.
func (s *State) RemoveClaimCode() error { return s.remove(claimFile) }

// attemptClaim spends one attempt of the claim code on presented. On a
// match it calls claim with the admin member the code names, and on
// claim's success destroys the code and records owner. A code is destroyed when it matches, and made unusable
// when it expires or runs out of attempts. The refusals are the
// edgeproto claim refusals.
func (s *State) attemptClaim(presented string, owner edgeproto.Account, now time.Time, claim func(admin string) error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	o, err := s.Owner()
	if err != nil {
		return err
	}
	if o != nil {
		return edgeproto.RefusalClaimed
	}
	c, ok, err := s.ClaimCode()
	switch {
	case err != nil:
		return err
	case !ok:
		return edgeproto.RefusalClaimWrong
	case now.After(c.ExpiresAt):
		if c.Hash != "" {
			c.Hash = ""
			if err = s.write(claimFile, c); err != nil {
				return err
			}
		}
		return edgeproto.RefusalClaimExpired
	case c.Hash == "" || c.AttemptsLeft <= 0:
		return edgeproto.RefusalClaimExhausted
	}
	normalized, _, err := edgeproto.ParseClaimCode(presented)
	match := err == nil && subtle.ConstantTimeCompare([]byte(edgeproto.HashToken(normalized)), []byte(c.Hash)) == 1
	c.AttemptsLeft--
	if !match {
		refusal := edgeproto.RefusalClaimWrong
		if c.AttemptsLeft == 0 {
			c.Hash = ""
			refusal = edgeproto.RefusalClaimExhausted
		}
		if err := s.write(claimFile, c); err != nil {
			return err
		}
		return refusal
	}
	if err := claim(c.Admin); err != nil {
		if werr := s.write(claimFile, c); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	// Either write alone keeps the used code from claiming again.
	return errors.Join(s.writeOwner(owner), s.remove(claimFile))
}

// transferOwner replaces the owner with owner and runs report; when
// report fails the previous owner is restored. A server without an owner
// is refused: it stays ownerless until a claim code gives it one.
func (s *State) transferOwner(origin string, owner edgeproto.Account, report func() error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	prev, err := s.Owner()
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("this server has no owner at %s; its administrator gives it one with a claim code from `aether-server edge claim-code`", origin)
	}
	if err := s.writeOwner(owner); err != nil {
		return err
	}
	if err := report(); err != nil {
		return errors.Join(fmt.Errorf("report the new owner to %s: %w", origin, err), s.writeOwner(*prev))
	}
	return nil
}

// clearOwnerIf forgets the owner when it still is owner, and reports
// whether it did.
func (s *State) clearOwnerIf(owner edgeproto.Account) (bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	cur, err := s.Owner()
	if err != nil || cur == nil || cur.Provider != owner.Provider || cur.Subject != owner.Subject {
		return false, err
	}
	return true, s.ClearOwner()
}

// policyFile, directly under StateDir, records the access policy the
// server last started with, whichever edge it used.
const policyFile = "access_policy.json"

type policyRecord struct {
	Policy edgeproto.AccessPolicy `json:"policy"`
}

// swapPolicy records p as the policy the server runs with and returns the
// one it recorded before, empty on the first start with an edge.
func swapPolicy(dataDir string, p edgeproto.AccessPolicy) (edgeproto.AccessPolicy, error) {
	dir := StateDir(dataDir)
	path := filepath.Join(dir, policyFile)
	var prev policyRecord
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("edgeagent: %w", err)
	default:
		if err = json.Unmarshal(data, &prev); err != nil {
			return "", fmt.Errorf("edgeagent: parse %s: %w", path, err)
		}
	}
	if prev.Policy == p {
		return prev.Policy, nil
	}
	if data, err = json.Marshal(policyRecord{Policy: p}); err != nil {
		return "", fmt.Errorf("edgeagent: encode %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	return prev.Policy, nil
}

// Status returns the agent's last status, false when it never ran.
func (s *State) Status() (Status, bool, error) {
	var st Status
	ok, err := s.read(statusFile, &st)
	return st, ok, err
}

// writeStatus records st.
func (s *State) writeStatus(st Status) {
	if err := s.write(statusFile, st); err != nil {
		slog.Warn("edge: write status", "error", err)
	}
}

func (s *State) path(name string) string { return filepath.Join(s.dir, name) }

func (s *State) read(name string, v any) (bool, error) {
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("edgeagent: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("edgeagent: parse %s: %w", s.path(name), err)
	}
	return true, nil
}

// write replaces name atomically, so a reader in another process sees the
// old file or the new one.
func (s *State) write(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("edgeagent: encode %s: %w", name, err)
	}
	dir := filepath.Dir(s.path(name))
	if err = s.mkdir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(name)+".*")
	if err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck // gone after a successful rename
	if err := s.chown(f.Name()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("edgeagent: write %s: %w", f.Name(), err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("edgeagent: sync %s: %w", f.Name(), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("edgeagent: close %s: %w", f.Name(), err)
	}
	if err := os.Rename(f.Name(), s.path(name)); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}

func (s *State) remove(name string) error {
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}

// mkdir creates dir, within the state directory, and every directory
// between it and the state directory 0700, and narrows existing ones:
// they hold the pin that authenticates the edge.
func (s *State) mkdir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("edgeagent: %w", err)
		}
		if err := s.chown(d); err != nil {
			return err
		}
		if d == s.dir {
			return nil
		}
	}
}

// lock serializes claim-code and owner changes between the serve process
// and the aether-server edge commands.
func (s *State) lock() (func(), error) {
	if err := s.mkdir(s.dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	if err := s.chown(f.Name()); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("edgeagent: lock %s: %w", f.Name(), err)
	}
	return func() { _ = f.Close() }, nil
}

// chown gives path to the owner of the data directory when this process
// runs as root: `sudo aether-server edge claim-code` and `edge trust` must
// leave files a server running as that owner can read and lock.
func (s *State) chown(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(filepath.Dir(s.dir))
	if err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Lchown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}
