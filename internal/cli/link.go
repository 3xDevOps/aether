package cli

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// LinkOptions controls one server link operation.
type LinkOptions struct {
	Addr   string
	Invite string
	Name   string
	Key    string
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
	cfg.Links = prev.Links
	if name == "" {
		return cfg
	}
	return UpsertLink(cfg, NamedLink{
		Name:       name,
		Addr:       cfg.Addr,
		User:       cfg.User,
		Key:        cfg.Key,
		AutoKey:    cfg.Key == "",
		Repo:       cfg.Repo,
		KnownHosts: cfg.KnownHosts,
	})
}

// Link dials and verifies a server, returning the config to save and the
// live connection. The caller owns the connection and must close it or adopt it.
func Link(opts LinkOptions, prev Config) (LinkResult, error) {
	cfg := Config{Addr: normalizeAddr(opts.Addr), User: "aether"}
	key, err := linkKey(opts.Key, prev, opts.Name)
	if err != nil {
		return LinkResult{}, err
	}
	cfg.Key = key

	auth := ResolveAuth(cfg)
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
			return LinkResult{}, err
		}
	}
	var conn *Conn
	if opts.Invite != "" {
		conn, err = DialInvite(cfg, opts.Invite, opts.Name)
	} else {
		conn, err = Dial(cfg)
	}
	if err != nil && opts.Invite == "" && generate && isAuthFailure(err) {
		if err = ensure(); err != nil {
			return LinkResult{}, err
		}
		conn, err = Dial(cfg)
	}
	if err != nil {
		return LinkResult{}, err
	}
	control, err := conn.Control()
	if err != nil {
		_ = conn.Close()
		return LinkResult{}, err
	}
	defer func() { _ = control.Close() }()
	var info protocol.ServerInfoResult
	if err := control.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		_ = conn.Close()
		return LinkResult{}, err
	}
	if info.ProtocolVersion != protocol.Version {
		_ = conn.Close()
		return LinkResult{}, fmt.Errorf("protocol version %q is not %q", info.ProtocolVersion, protocol.Version)
	}
	return LinkResult{Config: linkConfig(cfg, prev, opts.Name), Conn: conn, Info: info, KeyGenerated: keyGenerated}, nil
}
