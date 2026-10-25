// Package slack is the server's Slack bot (docs/slack.md). It holds a
// Socket Mode connection, an outbound websocket, so it needs no public
// address. It direct-messages a run's owner what web push announces, as a
// second push.Sink, and acts on replies and buttons as the member's linked
// Slack account, through the connector path (sshd.Server.ConnectorLocal).
package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// Client names the bot on the timeline: "<member> via Slack".
	Client = "Slack"
	// A link makes up to writeBurst replies and button presses at once,
	// then one more every writeRefill, as a connector does.
	writeBurst  = 30
	writeRefill = 2 * time.Second
)

type Store interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	store.SlackStore
}

type Caller interface {
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error)
}

// Config wires the service. Store, the tokens and Local are required.
type Config struct {
	Store Store
	// AppToken (xapp-) opens the Socket Mode connection; BotToken (xoxb-)
	// calls the Web API.
	AppToken string
	BotToken string
	// Name is the server's host name, which the bot names when it links
	// an account.
	Name string
	// Local re-checks grant as each connector request does and returns
	// the client that acts for it as Slack connector id.
	Local func(ctx context.Context, grant sshd.ConnectorGrant, id string) (Caller, error)
	// API replaces Slack's Web API URL, and HTTP the client that reaches
	// it and the Socket Mode URL it returns; both are for tests.
	API  string
	HTTP *http.Client
}

type Service struct {
	cfg Config
	api api
	now func() time.Time

	wg    sync.WaitGroup
	stop  context.CancelFunc
	inbox chan envelope

	mu        sync.Mutex
	closed    bool
	self      identity
	connected bool
	err       error
	codes     map[string]code
	writes    map[domain.MemberID]*rate.Limiter
}

// code is a live link code, kept as the SHA-256 of its normalized form.
type code struct {
	grant   sshd.ConnectorGrant
	expires time.Time
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.AppToken == "" || cfg.BotToken == "" || cfg.Local == nil {
		return nil, errors.New("slack: Store, AppToken, BotToken and Local are required")
	}
	if cfg.API == "" {
		cfg.API = API
	}
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	return &Service{cfg: cfg, api: api{base: cfg.API, client: cfg.HTTP}, now: time.Now,
		err: errors.New("not connected yet"), inbox: make(chan envelope, inboxSize),
		codes: map[string]code{}, writes: map[domain.MemberID]*rate.Limiter{}}, nil
}

// Start connects to Slack in the background and keeps reconnecting until
// Close; ctx bounds only the setup.
func (s *Service) Start(ctx context.Context) error {
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	s.stop = cancel
	s.mu.Unlock()
	s.wg.Go(func() { s.run(loopCtx) })
	s.wg.Go(func() { s.work(loopCtx) })
	return nil
}

// Close disconnects, abandons the work in flight and waits for it.
// Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	s.wg.Wait()
	return nil
}

func (s *Service) state(connected bool, err error) {
	s.mu.Lock()
	s.connected, s.err = connected, err
	s.mu.Unlock()
}

func (s *Service) identity() identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.self
}

func (s *Service) Status(ctx context.Context, member domain.MemberID) (protocol.SlackStatusResult, error) {
	s.mu.Lock()
	out := protocol.SlackStatusResult{Configured: true, Connected: s.connected, Bot: s.self.User, Team: s.self.Team}
	if s.err != nil {
		out.Error = s.err.Error()
	}
	s.mu.Unlock()
	link, err := s.cfg.Store.GetSlackLink(ctx, member)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	wire := linkToWire(link)
	out.Link = &wire
	return out, nil
}

// Code mints grant's member a link code, replacing any of theirs not yet
// spent, so a member holds at most one.
func (s *Service) Code(grant sshd.ConnectorGrant) (protocol.SlackLinkResult, error) {
	c, err := store.RandomApprovalCode()
	if err != nil {
		return protocol.SlackLinkResult{}, err
	}
	now := s.now()
	expires := now.Add(sshd.ConnectorCodeTTL)
	s.mu.Lock()
	maps.DeleteFunc(s.codes, func(_ string, c code) bool { return c.grant.Member == grant.Member || !now.Before(c.expires) })
	s.codes[codeHash(c)] = code{grant: grant, expires: expires}
	self := s.self
	s.mu.Unlock()
	return protocol.SlackLinkResult{Code: c, ExpiresAt: expires.UTC().Format(time.RFC3339), Bot: self.User, Team: self.Team}, nil
}

// redeem spends c, as a person typed it, if it is live at now.
func (s *Service) redeem(c string, now time.Time) (sshd.ConnectorGrant, bool) {
	hash := codeHash(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.codes[hash]
	delete(s.codes, hash)
	return live.grant, ok && now.Before(live.expires)
}

func codeHash(c string) string {
	sum := sha256.Sum256([]byte(store.NormalizeApprovalCode(c)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) Unlink(ctx context.Context, member domain.MemberID) (protocol.SlackLink, error) {
	link, err := s.cfg.Store.DeleteSlackLink(ctx, member)
	if err != nil {
		return protocol.SlackLink{}, err
	}
	return linkToWire(link), nil
}

func linkToWire(l *store.SlackLink) protocol.SlackLink {
	return protocol.SlackLink{UserID: l.UserID, TeamID: l.TeamID, LinkedAt: l.CreatedAt.UTC().Format(time.RFC3339)}
}

// local is the client link acts through, or why it no longer may.
func (s *Service) local(ctx context.Context, link *store.SlackLink) (Caller, error) {
	c, err := s.cfg.Local(ctx, sshd.ConnectorGrant{Member: link.Member, Device: link.Device}, link.UserID)
	if err != nil {
		return nil, fmt.Errorf("your Slack link no longer works (%v); run aether slack link again", err)
	}
	return c, nil
}

// allowWrite takes one of member's actions or says when the next is
// allowed.
func (s *Service) allowWrite(link *store.SlackLink, now time.Time) error {
	s.mu.Lock()
	lim, ok := s.writes[link.Member]
	if !ok {
		lim = rate.NewLimiter(rate.Every(writeRefill), writeBurst)
		s.writes[link.Member] = lim
	}
	res := lim.ReserveN(now, 1)
	delay := res.DelayFrom(now)
	if delay > 0 {
		res.CancelAt(now)
	}
	s.mu.Unlock()
	if delay == 0 {
		return nil
	}
	return fmt.Errorf("your Slack link %s used its %d actions and gets one more every %s; retry in %s",
		link.UserID, writeBurst, writeRefill, delay.Round(time.Second))
}

func logged(what string, err error, args ...any) {
	if err != nil {
		slog.Warn("slack: "+what, append(args, "error", err)...)
	}
}
