package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// WhoIsIdentity is the tailnet identity behind a connection's remote
// address, as reported by tailscaled.
type WhoIsIdentity struct {
	// Login is the owner's tailnet login name (e.g. "alice@example.com");
	// empty when the node is tagged.
	Login string
	// NodeID is the stable Tailscale node ID, recorded in audit logs.
	NodeID string
	// Tagged marks a tagged node (CI machines, shared boxes): no person
	// stands behind it, so it never maps to a member through WhoIs.
	Tagged bool
}

// WhoIsResolver resolves a connection's remote address to a tailnet
// identity. Any failure (no tailscaled, daemon down, address not on the
// tailnet) must surface as an error; sshd then falls back to key auth.
type WhoIsResolver interface {
	WhoIs(ctx context.Context, remoteAddr string) (WhoIsIdentity, error)
}

// DefaultTailscaledSocket is where tailscaled's LocalAPI listens on Linux.
const DefaultTailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// LocalWhoIs resolves identities via the local tailscaled's LocalAPI
// WhoIs endpoint over its unix socket - the same mechanism Tailscale SSH
// uses. It is a minimal internal client (one GET, four response fields)
// instead of the tailscale.com/client/local module, whose dependency
// graph is disproportionate to this single call (wave 1 contract §9.8).
type LocalWhoIs struct {
	client *http.Client
}

// NewLocalWhoIs builds a resolver talking to the tailscaled unix socket
// at socketPath (empty = DefaultTailscaledSocket).
func NewLocalWhoIs(socketPath string) *LocalWhoIs {
	if socketPath == "" {
		socketPath = DefaultTailscaledSocket
	}
	return &LocalWhoIs{client: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

// WhoIs asks tailscaled who owns remoteAddr ("ip:port"). proto=tcp pins
// the lookup to the TCP proxy mapper so a UDP mapping for the same
// address can never collide with an SSH connection.
func (l *LocalWhoIs) WhoIs(ctx context.Context, remoteAddr string) (WhoIsIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://local-tailscaled.sock/localapi/v0/whois?proto=tcp&addr="+url.QueryEscape(remoteAddr), nil)
	if err != nil {
		return WhoIsIdentity{}, fmt.Errorf("sshd: whois request: %w", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return WhoIsIdentity{}, fmt.Errorf("sshd: whois %s: %w", remoteAddr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return WhoIsIdentity{}, fmt.Errorf("sshd: whois %s: status %d: %s",
			remoteAddr, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wr struct {
		Node struct {
			StableID string
			Tags     []string
		}
		UserProfile struct {
			LoginName string
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wr); err != nil {
		return WhoIsIdentity{}, fmt.Errorf("sshd: whois %s: decode: %w", remoteAddr, err)
	}
	id := WhoIsIdentity{NodeID: wr.Node.StableID}
	if len(wr.Node.Tags) > 0 || wr.UserProfile.LoginName == "tagged-devices" {
		id.Tagged = true
	} else {
		id.Login = wr.UserProfile.LoginName
	}
	return id, nil
}

// memberColorsOf collects the attribution colors already assigned, so
// attribution.NextColor can pick the least-used one.
func memberColorsOf(members []*domain.Member) []string {
	colors := make([]string, 0, len(members))
	for _, m := range members {
		colors = append(colors, m.Color)
	}
	return colors
}

// authenticateTailnet is the "none" auth callback used when a WhoIs
// resolver is configured: resolve the remote address to a tailnet login
// and map it to a member, auto-registering unknown logins. WhoIs runs at
// most once per connection ("none" is the first auth attempt every
// client sends). Every failure falls back to the publickey path; tagged
// nodes, resolver failures, and TailnetRequireKey carry a banner
// explaining why a key is required.
func (s *Server) authenticateTailnet(cm ssh.ConnMetadata) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(s.authCtx(), authTimeout)
	defer cancel()
	id, err := s.tailnetIdentity(ctx, cm.RemoteAddr().String())
	if errors.Is(err, ErrTaggedNode) {
		return nil, &ssh.BannerError{
			Err:     err,
			Message: "tagged tailnet node; key authentication required\n",
		}
	}
	if err != nil {
		slog.Warn("sshd: tailnet whois failed; falling back to key auth", "error", errors.Unwrap(err))
		return nil, &ssh.BannerError{
			Err:     err,
			Message: "tailnet identity unavailable; key authentication required\n",
		}
	}
	if s.cfg.TailnetRequireKey {
		return nil, &ssh.BannerError{
			Err:     errors.New("sshd: tailnet identity requires a key"),
			Message: "tailnet login " + id.Login + " must also present a registered SSH key\n",
		}
	}
	m, err := s.tailnetMember(ctx, id)
	if err != nil {
		return nil, err
	}
	slog.Info("sshd: tailnet auth",
		"member", m.ID, "login", id.Login, "node", id.NodeID,
		"user", cm.User(), "method", "tailnet", "pending", m.Pending)
	return &ssh.Permissions{Extensions: map[string]string{memberIDExtension: string(m.ID)}}, nil
}

// ErrTaggedNode is a tailnet connection from a tagged node: no person
// stands behind it, so it never maps to a member through WhoIs.
var ErrTaggedNode = errors.New("sshd: tagged tailnet node")

// tailnetIdentity resolves remoteAddr through WhoIs and refuses tagged
// nodes. A resolver failure comes back wrapped, so callers can tell it
// from the tagged-node refusal.
func (s *Server) tailnetIdentity(ctx context.Context, remoteAddr string) (WhoIsIdentity, error) {
	id, err := s.cfg.WhoIs.WhoIs(ctx, remoteAddr)
	if err != nil {
		return WhoIsIdentity{}, fmt.Errorf("sshd: tailnet whois: %w", err)
	}
	if id.Tagged || id.Login == "" {
		return WhoIsIdentity{}, ErrTaggedNode
	}
	return id, nil
}

// tailnetMember maps a resolved tailnet login to its member, registering
// an unknown login.
func (s *Server) tailnetMember(ctx context.Context, id WhoIsIdentity) (*domain.Member, error) {
	m, err := s.cfg.Store.GetMemberByTailnetLogin(ctx, id.Login)
	if errors.Is(err, store.ErrNotFound) {
		m, err = s.registerTailnetMember(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("sshd: resolve tailnet login %s: %w", id.Login, err)
	}
	return m, nil
}

// TailnetMember identifies the tailnet node behind remoteAddr ("ip:port")
// as a member, exactly as the SSH "none" auth does: WhoIs on the address,
// tagged nodes refused with ErrTaggedNode, unknown logins registered (the
// first as admin, later ones pending unless TailnetAutoJoin). It is the
// identity of every request on the server-hosted dashboard, which cannot
// present a key; WebIdentity says whether this server allows that.
func (s *Server) TailnetMember(ctx context.Context, remoteAddr string) (*domain.Member, error) {
	id, err := s.tailnetIdentity(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	return s.tailnetMember(ctx, id)
}

// WebIdentity reports whether this server can identify a dashboard
// request by its tailnet address alone; the error names why not, and
// what an operator changes.
func (s *Server) WebIdentity() error {
	if s.cfg.WhoIs == nil {
		return errors.New("the dashboard identifies members by tailnet WhoIs and tailscaled was not running when the server started (no " + DefaultTailscaledSocket + "); start Tailscale before the server, or set web-port to 0")
	}
	if s.cfg.TailnetRequireKey {
		return errors.New("tailnet-require-key demands an SSH key on every tailnet connection and the dashboard cannot present one; set one of tailnet-require-key and web-port off")
	}
	return nil
}

// registerTailnetMember auto-registers an unknown tailnet login. The
// first identity to contact a fresh server bootstraps as the admin;
// later identities join pending unless TailnetAutoJoin is set.
func (s *Server) registerTailnetMember(ctx context.Context, id WhoIsIdentity) (*domain.Member, error) {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	m := &domain.Member{
		DisplayName:  displayNameFromLogin(id.Login),
		TailnetLogin: id.Login,
		Color:        attribution.NextColor(memberColorsOf(members)),
		Role:         domain.RoleCollaborator,
		Pending:      !s.cfg.TailnetAutoJoin,
	}
	if len(members) == 0 {
		m.Role = domain.RoleAdmin
		m.Pending = false
	}
	if err := s.cfg.Store.CreateMember(ctx, m); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Lost a concurrent registration race for the same login.
			return s.cfg.Store.GetMemberByTailnetLogin(ctx, id.Login)
		}
		return nil, err
	}
	if m.Role == domain.RoleAdmin {
		slog.Info("sshd: bootstrap: first member registered as admin",
			"member", m.ID, "login", id.Login, "node", id.NodeID, "method", "tailnet")
	} else {
		slog.Info("sshd: tailnet member auto-registered",
			"member", m.ID, "login", id.Login, "node", id.NodeID, "pending", m.Pending)
	}
	return m, nil
}

// displayNameFromLogin derives a human-friendly default display name from
// a tailnet login ("alice@example.com" -> "alice").
func displayNameFromLogin(login string) string {
	if at := strings.IndexByte(login, '@'); at > 0 {
		return login[:at]
	}
	return login
}
