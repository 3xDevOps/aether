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

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// StateDir is where the agent keeps its state under a server data
// directory: one directory per edge origin, beside the dashboard
// certificates. Every directory is 0700 and every file 0600.
func StateDir(dataDir string) string { return filepath.Join(dataDir, "edge") }

const (
	pinFile    = "edge_key.json"
	ownerFile  = "owner.json"
	claimFile  = "claim.json"
	statusFile = "status.json"
	lockFile   = "lock"
)

// State is the agent's persistent state with one edge: the pinned edge
// key, the owner, the claim code and the agent's last status. The serve
// process and the aether-server edge commands share it, so every method
// reads the files afresh.
type State struct{ dir string }

// OpenState returns the state for the edge at edgeURL under dataDir. Each
// edge origin keeps its own, so a server moved to another edge enrolls
// there unclaimed and pins that edge's key, and moving back finds the
// first edge's pin and owner again. Nothing is created until a method
// writes.
func OpenState(dataDir, edgeURL string) (*State, error) {
	origin, err := edgeproto.Origin(edgeURL)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	// A scheme holds no "_" and a host no "://", so the name is unique
	// per origin.
	name := strings.Replace(origin, "://", "_", 1)
	return &State{dir: filepath.Join(StateDir(dataDir), name)}, nil
}

// Status is what the agent last reported about its control connection.
// ServerDomain is the domain the server's dashboard is served under, as
// <server id>.<ServerDomain>, empty while unknown or when the edge passes
// no dashboard through.
type Status struct {
	Edge         string    `json:"edge"`
	Connected    bool      `json:"connected"`
	Error        string    `json:"error,omitempty"`
	Since        time.Time `json:"since"`
	ServerDomain string    `json:"server_domain,omitempty"`
}

// ClaimCode describes the current claim code. The code itself is shown
// only when it is issued: the state keeps its hash.
type ClaimCode struct {
	Hash         string    `json:"hash"`
	ExpiresAt    time.Time `json:"expires_at"`
	AttemptsLeft int       `json:"attempts_left"`
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

// Owner returns the account that claimed this server, nil when it is
// unclaimed.
func (s *State) Owner() (*edgeproto.Account, error) {
	var a edgeproto.Account
	ok, err := s.read(ownerFile, &a)
	if err != nil || !ok {
		return nil, err
	}
	return &a, nil
}

// ClearOwner forgets the owner, which leaves the server unclaimed at the
// edge. Members keep their access to the server itself.
func (s *State) ClearOwner() error { return s.remove(ownerFile) }

// ClaimCode returns the current claim code's record, false when there is
// none.
func (s *State) ClaimCode() (ClaimCode, bool, error) {
	var c ClaimCode
	ok, err := s.read(claimFile, &c)
	return c, ok, err
}

// IssueClaimCode replaces any claim code with a fresh one for serverID,
// valid from now for ClaimCodeTTL and ClaimCodeAttempts attempts.
func (s *State) IssueClaimCode(serverID string, now time.Time) (string, time.Time, error) {
	code, err := edgeproto.NewClaimCode(serverID)
	if err != nil {
		return "", time.Time{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return "", time.Time{}, err
	}
	defer unlock()
	c := ClaimCode{Hash: edgeproto.HashToken(code), ExpiresAt: now.Add(edgeproto.ClaimCodeTTL), AttemptsLeft: edgeproto.ClaimCodeAttempts}
	if err := s.write(claimFile, c); err != nil {
		return "", time.Time{}, err
	}
	return code, c.ExpiresAt, nil
}

// RemoveClaimCode destroys the claim code.
func (s *State) RemoveClaimCode() error { return s.remove(claimFile) }

// attemptClaim spends one attempt of the claim code on presented. On a
// match it calls claim, and on claim's success destroys the code and
// records owner. A code is destroyed when it matches, and made unusable
// when it expires or runs out of attempts. The refusals are the
// edgeproto claim refusals.
func (s *State) attemptClaim(presented string, owner edgeproto.Account, now time.Time, claim func() error) error {
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
	if err := claim(); err != nil {
		if werr := s.write(claimFile, c); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	// Either write alone keeps the used code from claiming again.
	return errors.Join(s.write(ownerFile, owner), s.remove(claimFile))
}

// Status returns the agent's last status, false when it never ran.
func (s *State) Status() (Status, bool, error) {
	var st Status
	ok, err := s.read(statusFile, &st)
	return st, ok, err
}

// writeStatus records st. Only an enrollment learns the server domain, so
// a status written while disconnected keeps the last one.
func (s *State) writeStatus(st Status) {
	if !st.Connected {
		if last, ok, err := s.Status(); err == nil && ok {
			st.ServerDomain = last.ServerDomain
		}
	}
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
	if err = s.mkdir(); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "."+name+".*")
	if err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck // gone after a successful rename
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

// mkdir creates the state directory and its parent 0700, and narrows
// existing ones: they hold the pin that authenticates the edge.
func (s *State) mkdir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	for _, dir := range []string{filepath.Dir(s.dir), s.dir} {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("edgeagent: %w", err)
		}
	}
	return nil
}

// lock serializes claim-code changes between the serve process and the
// aether-server edge commands.
func (s *State) lock() (func(), error) {
	if err := s.mkdir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("edgeagent: lock %s: %w", f.Name(), err)
	}
	return func() { _ = f.Close() }, nil
}
