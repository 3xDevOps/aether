// Package ompbroker holds oh-my-pi's logins for session VMs: one omp auth
// broker per account, run by the server, and the session's way to it.
// docs/harnesses.md#oh-my-pi-in-session-vms describes both.
package ompbroker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Units runs each broker in a transient systemd unit, outside the server's
// process tree.
type Units interface {
	Start(ctx context.Context, name string, argv, properties []string) error
	Stop(ctx context.Context, name string) error
}

// Config is where brokers run from.
type Config struct {
	// Binary is the pinned omp release, serversetup.OMPBroker.Path.
	Binary string
	// Dir is a root-only directory holding each account's broker home.
	Dir string
	// DataDir is the server's data directory, which a broker never sees.
	DataDir string
	Units   Units
	// Bind binds subpath of base at target, replacing what target held,
	// without leaving base, showing what onDiskUID:onDiskGID owns there as
	// the broker user's; Unbind undoes it: vm.BindBeneath and vm.Unbind.
	Bind   func(target, base, subpath string, onDiskUID, onDiskGID int) error
	Unbind func(target string) error
	// UID and GID are the broker's own unprivileged user, never root.
	UID, GID int
	// Ready bounds a broker's start.
	Ready time.Duration
}

// Account is whose logins a broker serves: their home on the host.
type Account struct {
	ID   string
	Home string
	// Shared marks a session on an account its launcher does not own.
	Shared bool
	// Prepare runs before the broker starts. It returns the uid:gid the
	// account's own sessions run as, which owns their files on the host and
	// which the broker acts as on disk, or why the broker may not start.
	Prepare func(context.Context) (uid, gid int, err error)
}

// unitAgentDir is where a broker's unit sees the account's ~/.omp/agent.
// systemd resolves a bind's source on the host, where a symlink in the home
// could name any directory, so the server binds ~/.omp/agent, resolved
// beneath the home, at a staging path of its own first, and the unit binds
// that.
const unitAgentDir = "/run/aether-omp/agent"

type broker struct {
	url   *url.URL
	token string
}

// account is one account's broker; mu serializes its start and stop, so a
// broken account never holds up another.
type account struct {
	mu sync.Mutex
	b  *broker
	// started is set once this server process started the broker, so it
	// runs the omp this server installed.
	started bool
}

// Manager starts each account's broker on first use and keeps it.
type Manager struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	accounts map[string]*account
}

func NewManager(cfg Config) *Manager {
	if cfg.Ready == 0 {
		cfg.Ready = 30 * time.Second
	}
	return &Manager{cfg: cfg, client: &http.Client{Timeout: 2 * time.Second}, accounts: map[string]*account{}}
}

func unitName(id string) string { return "aether-omp-broker-" + id + ".service" }

func (m *Manager) account(id string) *account {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accounts[id]
	if a == nil {
		a = &account{}
		m.accounts[id] = a
	}
	return a
}

// ensure returns a's broker. It starts the broker on first use, which
// replaces one an earlier server left with this server's omp, and starts it
// again after a request failed only once the broker no longer answers.
func (m *Manager) ensure(ctx context.Context, a Account) (*broker, error) {
	if m.cfg.UID == 0 {
		return nil, errors.New("omp broker: refusing to run as root; the server could not find or add the broker's own user, see its log")
	}
	st := m.account(a.ID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.b != nil {
		return st.b, nil
	}
	home := filepath.Join(m.cfg.Dir, a.ID)
	b, err := m.prepare(home)
	if err != nil {
		return nil, fmt.Errorf("omp broker for %s: %w", a.ID, err)
	}
	if st.started && m.healthy(ctx, b) {
		st.b = b
		return b, nil
	}
	if err = m.start(ctx, home, a, b); err != nil {
		// The saved port may belong to another program now.
		if rmErr := os.Remove(filepath.Join(home, "port")); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("omp broker for %s: %w", a.ID, rmErr)
		}
		if b, err = m.prepare(home); err == nil {
			err = m.start(ctx, home, a, b)
		}
		if err != nil {
			return nil, fmt.Errorf("omp broker for %s: %w", a.ID, err)
		}
	}
	st.b, st.started = b, true
	return b, nil
}

// forget drops id's broker after a request to it failed, so the next
// request checks it and starts it again if it no longer answers.
func (m *Manager) forget(id string) {
	st := m.account(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.b = nil
}

// Stop ends id's broker and removes what it kept: its unit, the bind of
// the account's ~/.omp/agent and its home.
func (m *Manager) Stop(ctx context.Context, id string) error {
	st := m.account(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := m.cfg.Units.Stop(ctx, unitName(id)); err != nil {
		return fmt.Errorf("omp broker for %s: %w", id, err)
	}
	if err := m.cfg.Unbind(m.staging(id)); err != nil {
		return fmt.Errorf("omp broker for %s: %w", id, err)
	}
	if err := os.RemoveAll(filepath.Join(m.cfg.Dir, id)); err != nil {
		return fmt.Errorf("omp broker for %s: %w", id, err)
	}
	m.mu.Lock()
	delete(m.accounts, id)
	m.mu.Unlock()
	return nil
}

func (m *Manager) staging(id string) string { return filepath.Join(m.cfg.Dir, id+".agent") }

// prepare makes the broker's home with its token and port.
func (m *Manager) prepare(home string) (*broker, error) {
	tokenPath := filepath.Join(home, ".omp", "auth-broker.token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return nil, err
	}
	for _, dir := range []string{home, filepath.Dir(tokenPath)} {
		if err := os.Lchown(dir, m.cfg.UID, m.cfg.GID); err != nil {
			return nil, err
		}
	}
	token, err := m.readOrCreate(tokenPath, func() (string, error) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw), nil
	})
	if err != nil {
		return nil, err
	}
	port, err := m.readOrCreate(filepath.Join(home, "port"), freePort)
	if err != nil {
		return nil, err
	}
	return &broker{url: &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", port)}, token: token}, nil
}

func (m *Manager) readOrCreate(path string, create func() (string, error)) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return strings.TrimSpace(string(data)), nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	value, err := create()
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(path, []byte(value), 0o600); err != nil {
		return "", err
	}
	return value, os.Lchown(path, m.cfg.UID, m.cfg.GID)
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// start runs the broker as its own user in a unit that sees the account's
// ~/.omp/agent and its own home writable, the rest of the host read-only
// and none of the server's data. Through the bind the broker sees the
// files the account's sessions own as its own and creates its files as
// theirs, so their owner on the host never changes for Docker sessions.
func (m *Manager) start(ctx context.Context, home string, a Account, b *broker) error {
	if _, err := os.Stat(m.cfg.Binary); err != nil {
		return fmt.Errorf("the pinned omp is not installed (aether-server vm install puts it there): %w", err)
	}
	name := unitName(a.ID)
	if err := m.cfg.Units.Stop(ctx, name); err != nil {
		return err
	}
	uid, gid, err := a.Prepare(ctx)
	if err != nil {
		return err
	}
	if err = adopt(a.Home, uid, gid); err != nil {
		return err
	}
	staging := m.staging(a.ID)
	if err = m.cfg.Bind(staging, a.Home, ".omp/agent", uid, gid); err != nil {
		return err
	}
	argv := []string{m.cfg.Binary, "auth-broker", "serve", "--bind=" + b.url.Host}
	props := []string{
		"User=" + strconv.Itoa(m.cfg.UID), "Group=" + strconv.Itoa(m.cfg.GID),
		"Environment=HOME=" + home, "Environment=PI_CODING_AGENT_DIR=" + unitAgentDir,
		"NoNewPrivileges=yes", "CapabilityBoundingSet=", "AmbientCapabilities=",
		"ProtectSystem=strict", "PrivateTmp=yes", "PrivateDevices=yes",
		"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes",
		"ProtectControlGroups=yes", "ProtectClock=yes", "ProtectHostname=yes",
		"RestrictNamespaces=yes", "RestrictSUIDSGID=yes", "LockPersonality=yes",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK",
		"TemporaryFileSystem=" + m.cfg.DataDir + ":ro",
		// systemd reads a quoted source:destination as one source path.
		`BindPaths="` + home + `"`, "BindPaths=" + staging + ":" + unitAgentDir,
	}
	// ProtectHome hides its directories past every bind, so it is left off
	// when anything the broker needs lives in one.
	if !slices.ContainsFunc([]string{m.cfg.DataDir, m.cfg.Binary, home, staging}, underHome) {
		props = append(props, "ProtectHome=yes")
	}
	if err := m.cfg.Units.Start(ctx, name, argv, props); err != nil {
		return err
	}
	deadline := time.Now().Add(m.cfg.Ready)
	for !m.healthy(ctx, b) {
		if time.Now().After(deadline) {
			return fmt.Errorf("unit %s did not answer on %s within %s; see journalctl -u %s", name, b.url.Host, m.cfg.Ready, name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

// adopt gives what root owns in the home's ~/.omp/agent, such as the
// directory the server made, to the account's session user, as a Docker
// session's launch does with the whole home, so the broker can write it.
// It resolves names beneath the home and follows no symlink.
func adopt(home string, uid, gid int) error {
	if uid == 0 {
		return nil
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	agent, err := root.OpenRoot(".omp/agent")
	if err != nil {
		return err
	}
	defer func() { _ = agent.Close() }()
	return fs.WalkDir(agent.FS(), ".", func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := agent.Lstat(name)
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == 0 {
			return agent.Lchown(name, uid, gid)
		}
		return nil
	})
}

func underHome(path string) bool {
	for _, hidden := range []string{"/home", "/root", "/run/user"} {
		if path == hidden || strings.HasPrefix(path, hidden+"/") {
			return true
		}
	}
	return false
}

// healthy reports whether b's port is the broker's: the listening socket
// belongs to the broker user, so no other program receives the token, and
// omp's unauthenticated /v1/healthz answers ok.
func (m *Manager) healthy(ctx context.Context, b *broker) bool {
	port, err := strconv.Atoi(b.url.Port())
	if err != nil || !listenedBy(port, m.cfg.UID) {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url.String()+"/v1/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var health struct {
		OK bool `json:"ok"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&health) == nil && health.OK
}
