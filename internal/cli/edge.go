package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
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
// the fallback; when both fail the error carries both causes, and when
// only the address fails its cause goes to stderr, because a host key
// that is not the server's there means the address reaches another host.
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
	if directErr == nil {
		return client, edgeErr
	}
	if edgeErr == nil {
		fmt.Fprintf(os.Stderr, "aether: %v\naether: reached server %s through %s instead\n", directErr, cfg.ServerID, cfg.EdgeURL)
		return client, nil
	}
	return nil, fmt.Errorf("cli: server %s is unreachable\n  direct: %w\n  edge: %w", cfg.ServerID, directErr, edgeErr)
}

func dialDirect(ctx context.Context, cfg Config, user string, signer ssh.Signer) (*ssh.Client, error) {
	nc, err := (&net.Dialer{Timeout: directTimeout}).DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", cfg.Addr, err)
	}
	return pinnedHandshake(nc, cfg.Addr, user, signer, pinnedHostKey(cfg.ServerID, "the linked server"))
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
	return pinnedHandshake(nc, EdgeHost(cfg.ServerID), user, signer, pinnedHostKey(cfg.ServerID, "the linked server"))
}

// DialClaim claims the server that code, a claim code as aether-server
// setup printed it, names, through the edge of cfg, and returns the
// connection the server then serves as its new admin. The code travels
// only in the SSH user name, which x/crypto/ssh sends after the key
// exchange has verified the host key against the server id in the code;
// a host key that does not derive that id ends the handshake before the
// code is sent. The user name also names the account this device signed
// in as, and the server refuses a claim whose grant names another. A
// claim never uses cfg.Addr: only the edge vouches for the account a
// claim makes the owner.
func DialClaim(cfg Config, code string) (*Conn, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	edge, err := edgeclient.New(dir, cfg.EdgeURL)
	if err != nil {
		return nil, err
	}
	session, err := edge.Session()
	if err != nil {
		return nil, fmt.Errorf("cli: claim through %s: %w", edge.Host(), err)
	}
	user, serverID, err := edgeproto.ClaimUser(code, edgeproto.AccountPrincipal(session.Account.Account))
	if err != nil {
		return nil, fmt.Errorf("cli: %w", err)
	}
	cfg.ServerID = serverID
	signer, err := edgeclient.DeviceSigner(dir)
	if err != nil {
		return nil, fmt.Errorf("cli: %w", err)
	}
	nc, err := edge.DialClaim(context.Background(), serverID)
	if err != nil {
		return nil, err
	}
	client, err := pinnedHandshake(nc, EdgeHost(serverID), user, signer, pinnedHostKey(serverID, "the server the claim code names"))
	if err != nil {
		return nil, err
	}
	return &Conn{client: client, cfg: cfg}, nil
}

// errHostKeyRead ends a handshake once HostKeyFingerprint has the key.
var errHostKeyRead = errors.New("host key read")

// HostKeyFingerprint returns the SHA256 fingerprint of the host key
// serverID presents through the edge at edgeURL, after checking that the
// key derives serverID. It ends the connection after the key exchange,
// before this device authenticates.
func HostKeyFingerprint(ctx context.Context, edgeURL, serverID string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	edge, err := edgeclient.New(dir, edgeURL)
	if err != nil {
		return "", err
	}
	nc, err := edge.Dial(ctx, serverID)
	if err != nil {
		return "", err
	}
	defer func() { _ = nc.Close() }()
	pin := pinnedHostKey(serverID, "the listed server")
	var fingerprint string
	conf := &ssh.ClientConfig{
		User: "aether",
		HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
			if err := pin(host, remote, key); err != nil {
				return err
			}
			fingerprint = ssh.FingerprintSHA256(key)
			return errHostKeyRead
		},
	}
	if err := nc.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return "", fmt.Errorf("read host key of server %s: %w", serverID, err)
	}
	if _, _, _, err := ssh.NewClientConn(nc, EdgeHost(serverID), conf); !errors.Is(err, errHostKeyRead) {
		return "", fmt.Errorf("read host key of server %s: %w", serverID, err)
	}
	return fingerprint, nil
}

// pinnedHandshake runs the SSH handshake over nc, accepting only a host
// key pin accepts, and closes nc when it fails. What the server said in
// authentication banners is part of the error, line by line, because it
// holds the commands that fix a refusal, such as approving a device.
func pinnedHandshake(nc net.Conn, where, user string, signer ssh.Signer, pin ssh.HostKeyCallback) (*ssh.Client, error) {
	var said []string
	conf := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: pin,
		BannerCallback: func(message string) error {
			said = append(said, bannerLines(message)...)
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
		if len(said) > 0 {
			err = fmt.Errorf("%w\n  server said:\n    %s", err, strings.Join(said, "\n    "))
		}
		return nil, err
	}
	if err := nc.SetDeadline(time.Time{}); err != nil {
		_ = cc.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", where, err)
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

// bannerLines splits a banner into its non-empty lines, with control
// characters replaced so the text cannot drive the terminal it is
// printed to.
func bannerLines(message string) []string {
	var lines []string
	for line := range strings.SplitSeq(strings.ToValidUTF8(message, "?"), "\n") {
		line = strings.TrimRight(strings.Map(func(r rune) rune {
			switch {
			case r == '\t':
				return ' '
			case unicode.IsControl(r):
				return '?'
			}
			return r
		}, strings.TrimSuffix(line, "\r")), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// pinnedHostKey accepts only a host key that derives serverID; what names
// the server in the refusal.
func pinnedHostKey(serverID, what string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if edgeproto.HostKeyMatches(key, serverID) {
			return nil
		}
		return fmt.Errorf("host key %s is server %s, not %s %s",
			ssh.FingerprintSHA256(key), edgeproto.ServerID(key), what, serverID)
	}
}
