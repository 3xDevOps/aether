package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// LinkOptions controls one server link operation.
type LinkOptions struct {
	Addr   string
	Invite string
	Name   string
	Key    string
	// EdgeURL and ServerID link a server through an edge; Addr is then
	// optional. Claim, a claim code, claims the server it names through
	// EdgeURL and links it: ServerID is then the code's. Join, an
	// invitation's join link, joins the server it names through the edge
	// it names and links it: EdgeURL and ServerID are then the link's.
	EdgeURL  string
	ServerID string
	Claim    string
	Join     string
}

// LinkResult is an unsaved link and its live server connection.
type LinkResult struct {
	Config       Config
	Conn         *Conn
	Info         protocol.ServerInfoResult
	KeyGenerated string
}

const defaultSSHPort = "2222"

func normalizeAddr(addr string) string {
	if addr == "" {
		return addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if port != "" {
			return addr
		}
		return net.JoinHostPort(host, defaultSSHPort)
	}
	host := addr
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return net.JoinHostPort(host, defaultSSHPort)
}

func savedKey(prev Config, name string) string {
	if name == "" {
		return prev.Key
	}
	named, ok := prev.Named(name)
	if ok {
		return named.Key
	}
	return prev.Key
}

// AutoKey is the --key value that ignores a saved key and lets the dial
// discover ~/.ssh/id_ed25519 or an agent key.
const AutoKey = "auto"

func linkKey(choice string, prev Config, name string) (string, error) {
	switch choice {
	case "":
		return savedKey(prev, name), nil
	case AutoKey:
		return "", nil
	}
	path, err := filepath.Abs(choice)
	if err != nil {
		return "", err
	}
	if err := CheckKey(path); err != nil {
		return "", err
	}
	return path, nil
}

func linkConfig(cfg, prev Config, name string) Config {
	if cfg.ServerID != "" && cfg.ServerID == prev.ServerID {
		if cfg.Addr != "" {
			prev.Addr = cfg.Addr
		}
		prev.EdgeURL = cfg.EdgeURL
		cfg = prev
	}
	cfg.Links = prev.Links
	// Git remotes of an edge link name only its server id, and edge-ssh
	// finds the link by that id: a replaced edge default stays, named by
	// its server id.
	if prev.ServerID != "" && prev.ServerID != cfg.ServerID &&
		!slices.ContainsFunc(prev.Links, func(l NamedLink) bool { return l.ServerID == prev.ServerID }) {
		cfg = UpsertLink(cfg, namedLink(prev.ServerID, prev))
	}
	if name == "" {
		return cfg
	}
	return UpsertLink(cfg, namedLink(name, cfg))
}

// NamedLinkFor is the saved link a named profile keeps for cfg.
func NamedLinkFor(name string, cfg Config) NamedLink { return namedLink(name, cfg) }

func namedLink(name string, cfg Config) NamedLink {
	return NamedLink{
		Name:       name,
		Addr:       cfg.Addr,
		User:       cfg.User,
		Key:        cfg.Key,
		AutoKey:    cfg.Key == "",
		Repo:       cfg.Repo,
		KnownHosts: cfg.KnownHosts,
		EdgeURL:    cfg.EdgeURL,
		ServerID:   cfg.ServerID,
	}
}

// Link dials and verifies a server, returning the config to save and the
// live connection. The caller owns the connection and must close it or adopt it.
// With a server id the link goes through DialLinked: no SSH key is
// resolved or generated, and Addr, when set, is the direct address tried
// before the edge. A claim goes through DialClaim, a join through
// DialJoin. ctx ends the dial and the server.info call that follows it.
func Link(ctx context.Context, opts LinkOptions, prev Config) (LinkResult, error) {
	cfg := Config{Addr: normalizeAddr(opts.Addr), User: "aether", EdgeURL: opts.EdgeURL, ServerID: opts.ServerID}
	var (
		conn         *Conn
		keyGenerated string
		err          error
	)
	serverID := opts.ServerID
	switch {
	case opts.Claim != "":
		_, serverID, err = edgeproto.ParseClaimCode(opts.Claim)
	case opts.Join != "":
		_, serverID, _, _, err = edgeproto.ParseJoinLink(opts.Join)
	}
	if err != nil {
		return LinkResult{}, fmt.Errorf("cli: %w", err)
	}
	// Checked before the dial: a claim dial spends its code, so nothing
	// after it may fail on local state.
	samePrev := false
	if serverID != "" && prev.ServerID == "" {
		if samePrev, err = prev.pins(serverID); err != nil {
			return LinkResult{}, err
		}
	}
	switch {
	case opts.Claim != "":
		conn, err = DialClaim(ctx, cfg, opts.Claim)
		if err == nil {
			cfg.ServerID = conn.cfg.ServerID
		}
	case opts.Join != "":
		conn, err = DialJoin(ctx, cfg, opts.Join)
		if err == nil {
			cfg.EdgeURL, cfg.ServerID = conn.cfg.EdgeURL, conn.cfg.ServerID
		}
	case cfg.ServerID != "":
		conn, err = DialContext(ctx, cfg)
	default:
		conn, keyGenerated, err = linkDial(ctx, opts, prev, &cfg)
	}
	if err != nil {
		return LinkResult{}, err
	}
	info, err := serverInfo(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return LinkResult{}, err
	}
	if info.ProtocolVersion != protocol.Version {
		_ = conn.Close()
		return LinkResult{}, fmt.Errorf("protocol version %q is not %q", info.ProtocolVersion, protocol.Version)
	}
	if samePrev {
		prev.ServerID = cfg.ServerID
	}
	return LinkResult{Config: linkConfig(cfg, prev, opts.Name), Conn: conn, Info: info, KeyGenerated: keyGenerated}, nil
}

// UpgradeRequired is the refusal a server whose release is newer than this
// client gives every call but server.info, nil when there is none. A link
// calls only server.info, so it asks here to report the refusal at once.
func UpgradeRequired(info protocol.ServerInfoResult) *protocol.Error {
	if !selfupdate.Behind(version.Version, info.MinClientVersion) {
		return nil
	}
	return protocol.UpgradeRequired(version.Version, info.ServerVersion, info.MinClientVersion)
}

// serverInfoTimeout bounds the server.info call that ends a link. Past
// the handshake, nothing else ends a call on a connection whose network
// went away.
const serverInfoTimeout = 30 * time.Second

// serverInfo calls server.info on conn, closing conn when ctx ends or the
// server takes longer than serverInfoTimeout to answer.
func serverInfo(ctx context.Context, conn *Conn) (protocol.ServerInfoResult, error) {
	ctx, cancel := context.WithTimeout(ctx, serverInfoTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	var info protocol.ServerInfoResult
	control, err := conn.Control()
	if err == nil {
		err = control.Call(protocol.MethodServerInfo, struct{}{}, &info)
		_ = control.Close()
	}
	if !stop() {
		return info, fmt.Errorf("cli: %s: %w", protocol.MethodServerInfo, ctx.Err())
	}
	return info, err
}

// pins reports whether the direct dial to the address of c would accept
// a host key that derives serverID from known_hosts.
func (c Config) pins(serverID string) (bool, error) {
	if c.Addr == "" {
		return false, nil
	}
	path := c.knownHostsPath()
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cli: known_hosts: %w", err)
	}
	var keys []ssh.PublicKey
	for {
		marker, _, key, _, rest, parseErr := ssh.ParseKnownHosts(body)
		if errors.Is(parseErr, io.EOF) {
			break
		}
		if parseErr != nil {
			return false, fmt.Errorf("cli: known_hosts %s: %w", path, parseErr)
		}
		if marker == "" && edgeproto.HostKeyMatches(key, serverID) {
			keys = append(keys, key)
		}
		body = rest
	}
	if len(keys) == 0 {
		return false, nil
	}
	// The callback matches hashed and wildcard hosts and refuses revoked
	// keys; it prefers the address over the remote, which goes unused.
	accepts, err := knownhosts.New(path)
	if err != nil {
		return false, fmt.Errorf("cli: known_hosts %s: %w", path, err)
	}
	for _, key := range keys {
		if accepts(c.Addr, &net.TCPAddr{}, key) == nil {
			return true, nil
		}
	}
	return false, nil
}

// linkDial dials a server linked by address, choosing its key and
// generating ~/.ssh/id_ed25519 when nothing else can authenticate.
func linkDial(ctx context.Context, opts LinkOptions, prev Config, cfg *Config) (*Conn, string, error) {
	key, err := linkKey(opts.Key, prev, opts.Name)
	if err != nil {
		return nil, "", err
	}
	cfg.Key = key

	auth := ResolveAuth(*cfg)
	offered := auth.Offered()
	auth.Close()
	keyGenerated := ""
	ensure := func() error {
		path, created, ensureErr := EnsureIdentity()
		if ensureErr != nil {
			return ensureErr
		}
		if created {
			keyGenerated = path
		}
		return nil
	}
	// Only automatic key discovery (cfg.Key == "") can pick up a generated
	// key. Invite redemption needs it before the dial; a plain link only
	// retries after an auth failure so a keyless tailnet server never gets
	// one generated.
	generate := !offered && cfg.Key == ""
	if generate && opts.Invite != "" {
		if err = ensure(); err != nil {
			return nil, "", err
		}
	}
	var conn *Conn
	if opts.Invite != "" {
		conn, err = dialInvite(ctx, *cfg, opts.Invite, opts.Name)
	} else {
		conn, err = DialContext(ctx, *cfg)
	}
	if err != nil && opts.Invite == "" && generate && isAuthFailure(err) {
		if err = ensure(); err != nil {
			return nil, "", err
		}
		conn, err = DialContext(ctx, *cfg)
	}
	return conn, keyGenerated, err
}
