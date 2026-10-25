package signing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	socketName = "agent.sock"
	// sun_path holds 108 bytes including the terminator, and the kernel
	// reports a longer path as EINVAL.
	maxSocketPath = 107
	// The agent's clients are semi-trusted runs: a run that loops connect()
	// must not walk the server to its file descriptor limit.
	maxConns    = 16
	idleTimeout = time.Minute
)

var (
	errClosed   = errors.New("signing: the agent service is closed")
	errReadOnly = errors.New("signing: the agent is read-only; Aether holds this key")
)

// Serve starts the member's agent unless it is running and returns the host
// directory holding its socket, to mount read-only at
// coordtransport.SigningMountDir. The key is read on every connection, so a
// key created later is served without a restart.
func (s *Service) Serve(member domain.MemberID) (string, error) {
	if _, err := s.keyPath(member); err != nil {
		return "", err
	}
	dir := s.socketDir(member)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errClosed
	}
	if s.listeners[member] != nil {
		return dir, nil
	}
	path := filepath.Join(dir, socketName)
	if len(path) > maxSocketPath {
		return "", fmt.Errorf("signing: socket path %s is %d bytes, over the %d-byte limit: use a shorter data directory",
			path, len(path), maxSocketPath)
	}
	// The container user need not be root, and reaches the socket only
	// through this member's mount, so the modes carry no authorization.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("signing: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", fmt.Errorf("signing: set mode on %s: %w", dir, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("signing: remove the stale socket %s: %w", path, err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return "", fmt.Errorf("signing: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		_ = l.Close()
		return "", fmt.Errorf("signing: set mode on %s: %w", path, err)
	}
	s.listeners[member] = l
	s.wg.Add(1)
	go s.accept(l, member)
	return dir, nil
}

// socketDir names the member's directory by a hash: older member IDs are 26
// characters, and a fixed 12 keeps every socket path shorter than the
// coordination socket's in the same data directory.
func (s *Service) socketDir(member domain.MemberID) string {
	sum := sha256.Sum256([]byte(member))
	return filepath.Join(s.sockets, hex.EncodeToString(sum[:6]))
}

// StopAgent closes the member's listener and deletes their socket
// directory. It holds s.mu throughout, as Serve does, so a Serve cannot start
// a listener whose directory this then deletes.
func (s *Service) StopAgent(member domain.MemberID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.listeners[member]; l != nil {
		_ = l.Close()
		delete(s.listeners, member)
	}
	dir := s.socketDir(member)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("signing: remove the agent socket %s: %w", dir, err)
	}
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, l := range s.listeners {
		_ = l.Close()
	}
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *Service) accept(l *net.UnixListener, member domain.MemberID) {
	defer s.wg.Done()
	slots := make(chan struct{}, maxConns)
	for {
		conn, err := l.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Warn("signing: accept failed", "member", member, "error", err)
			}
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				<-slots
				s.wg.Done()
			}()
			s.serve(conn, member)
		}()
	}
}

func (s *Service) serve(conn net.Conn, member domain.MemberID) {
	defer func() { _ = conn.Close() }()
	_ = agent.ServeAgent(memberAgent{s: s, member: member}, idleConn{conn})
}

// memberAgent serves one member's key. It reads the key for every request,
// so removing it takes effect on connections already open, and it refuses
// every request that would change or lock what it holds.
type memberAgent struct {
	s      *Service
	member domain.MemberID
}

func (a memberAgent) keyring() (agent.ExtendedAgent, error) {
	keyring := agent.NewKeyring().(agent.ExtendedAgent)
	key, err := a.s.Key(a.member)
	if err != nil || key == nil {
		return keyring, err
	}
	raw, err := ssh.ParseRawPrivateKey(key)
	if err == nil {
		err = keyring.Add(agent.AddedKey{PrivateKey: raw, Comment: comment(a.member)})
	}
	if err != nil {
		return nil, fmt.Errorf("signing: load the key for %q: %w", a.member, err)
	}
	return keyring, nil
}

func (a memberAgent) List() ([]*agent.Key, error) {
	keyring, err := a.keyring()
	if err != nil {
		return nil, err
	}
	return keyring.List()
}

func (a memberAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return a.SignWithFlags(key, data, 0)
}

func (a memberAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	keyring, err := a.keyring()
	if err != nil {
		return nil, err
	}
	return keyring.SignWithFlags(key, data, flags)
}

func (a memberAgent) Signers() ([]ssh.Signer, error) {
	keyring, err := a.keyring()
	if err != nil {
		return nil, err
	}
	return keyring.Signers()
}

func (memberAgent) Extension(string, []byte) ([]byte, error) {
	return nil, agent.ErrExtensionUnsupported
}

func (memberAgent) Add(agent.AddedKey) error   { return errReadOnly }
func (memberAgent) Remove(ssh.PublicKey) error { return errReadOnly }
func (memberAgent) RemoveAll() error           { return errReadOnly }
func (memberAgent) Lock([]byte) error          { return errReadOnly }
func (memberAgent) Unlock([]byte) error        { return errReadOnly }

// idleConn drops a client that sends nothing, or reads no reply, for
// idleTimeout, so a stuck one does not hold a connection slot forever.
type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c idleConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(idleTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
