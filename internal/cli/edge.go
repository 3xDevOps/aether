package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/edgeclient"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// edgeDomain is the domain of the logical host names git remotes use for
// edge links. RFC 2606 reserves .invalid, so the name never resolves and
// never collides with a real host.
const edgeDomain = "edge.aether.invalid"

const (
	// directTimeout is how long a link with an edge waits for its direct
	// address before it tries the edge.
	directTimeout    = 3 * time.Second
	handshakeTimeout = 20 * time.Second
)

// EdgeHost is the logical host name of serverID in git remote URLs.
func EdgeHost(serverID string) string { return serverID + "." + edgeDomain }

// ServerIDFromEdgeHost returns the server id of a logical edge host name.
func ServerIDFromEdgeHost(host string) (string, bool) {
	return edgeproto.ServerIDFromHostname(host, edgeDomain)
}

// DialLinked opens an SSH client connection to the server of cfg, a link
// with a server id. Syncd and Dial share it. The host key must derive the
// server id on every path; nothing is trusted on first use and nothing is
// written to known_hosts. The device key authenticates. With an address
// the server is dialed there first, and with an edge as well the edge is
// the fallback; when both fail the error carries both causes.
func DialLinked(ctx context.Context, cfg Config, user string) (*ssh.Client, error) {
	if !edgeproto.ValidServerID(cfg.ServerID) {
		return nil, fmt.Errorf("cli: %q is not a server id", cfg.ServerID)
	}
	if cfg.Addr == "" && cfg.EdgeURL == "" {
		return nil, fmt.Errorf("cli: the link to server %s has neither an address nor an edge", cfg.ServerID)
	}
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	signer, err := edgeclient.DeviceSigner(dir)
	if err != nil {
		return nil, fmt.Errorf("cli: %w", err)
	}
	var directErr error
	if cfg.Addr != "" {
		client, err := dialDirect(ctx, cfg, user, signer)
		if err == nil || cfg.EdgeURL == "" {
			return client, err
		}
		directErr = err
	}
	client, edgeErr := dialEdge(ctx, dir, cfg, user, signer)
	if edgeErr == nil || directErr == nil {
		return client, edgeErr
	}
	return nil, fmt.Errorf("cli: server %s is unreachable\n  direct: %w\n  edge: %w", cfg.ServerID, directErr, edgeErr)
}

func dialDirect(ctx context.Context, cfg Config, user string, signer ssh.Signer) (*ssh.Client, error) {
	nc, err := (&net.Dialer{Timeout: directTimeout}).DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", cfg.Addr, err)
	}
	return pinnedHandshake(nc, cfg.Addr, cfg.ServerID, user, signer)
}

func dialEdge(ctx context.Context, dir string, cfg Config, user string, signer ssh.Signer) (*ssh.Client, error) {
	edge, err := edgeclient.New(dir, cfg.EdgeURL)
	if err != nil {
		return nil, err
	}
	nc, err := edge.Dial(ctx, cfg.ServerID)
	if err != nil {
		return nil, err
	}
	return pinnedHandshake(nc, EdgeHost(cfg.ServerID), cfg.ServerID, user, signer)
}

// pinnedHandshake runs the SSH handshake over nc, accepting only a host
// key that derives serverID, and closes nc when it fails.
func pinnedHandshake(nc net.Conn, where, serverID, user string, signer ssh.Signer) (*ssh.Client, error) {
	var said []string
	conf := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: pinnedHostKey(serverID),
		BannerCallback: func(message string) error {
			if msg := strings.Join(strings.Fields(message), " "); msg != "" {
				said = append(said, msg)
			}
			return nil
		},
	}
	if err := nc.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", where, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, where, conf)
	if err != nil {
		_ = nc.Close()
		err = fmt.Errorf("ssh handshake with %s: %w", where, err)
		for _, msg := range said {
			err = fmt.Errorf("%w\n  server said: %s", err, msg)
		}
		return nil, err
	}
	if err := nc.SetDeadline(time.Time{}); err != nil {
		_ = cc.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", where, err)
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

func pinnedHostKey(serverID string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if edgeproto.HostKeyMatches(key, serverID) {
			return nil
		}
		return fmt.Errorf("host key %s is server %s, not the linked server %s",
			ssh.FingerprintSHA256(key), edgeproto.ServerID(key), serverID)
	}
}
