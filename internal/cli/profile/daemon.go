package profile

import (
	"context"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const keepaliveInterval = 30 * time.Second

// DaemonConfig is the SSH transport the profile watcher shares with
// `aether daemon run` (not the git fetch/push loop).
type DaemonConfig struct {
	Server         string
	KeyPath        string
	KnownHostsPath string
	User           string
	NoProfileSync  bool
}

// RunDaemon starts the profile watcher until ctx is done. When
// NoProfileSync is set it returns immediately so the git daemon can run
// alone; manual `aether profile push` is unaffected.
func RunDaemon(ctx context.Context, cfg DaemonConfig) error {
	w := &Watcher{
		Disabled: cfg.NoProfileSync,
		Dial:     cfg.dial,
	}
	return w.Run(ctx)
}

func (cfg DaemonConfig) dial(ctx context.Context) (Conn, error) {
	conn, err := cli.Dial(cli.Config{
		Addr:       cfg.Server,
		User:       cfg.User,
		Key:        cfg.KeyPath,
		KnownHosts: cfg.KnownHostsPath,
	})
	if err != nil {
		return nil, err
	}
	client, err := conn.Control()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	sc := &sshConn{client: client, ssh: conn.SSH(), closer: conn, done: make(chan struct{})}
	go sc.keepalive(ctx)
	return sc, nil
}

type sshConn struct {
	client *protocol.Client
	ssh    *ssh.Client
	closer interface{ Close() error }
	done   chan struct{}
	once   sync.Once
}

func (c *sshConn) Client() *protocol.Client { return c.client }
func (c *sshConn) Done() <-chan struct{}    { return c.done }
func (c *sshConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.closer.Close()
}

func (c *sshConn) keepalive(ctx context.Context) {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.Close()
			return
		case <-c.done:
			return
		case <-t.C:
			if _, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = c.Close()
				return
			}
		}
	}
}
