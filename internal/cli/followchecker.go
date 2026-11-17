package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

// ReleaseChecker is what the update paths ask of a release checker.
type ReleaseChecker interface {
	Check(ctx context.Context) (selfupdate.Check, error)
	CheckFresh(ctx context.Context) (selfupdate.Check, error)
	BaseURL() string
}

// channelTTL is how long the channel read from the server is reused.
const channelTTL = 10 * time.Minute

// FollowedChecker checks the releases of the channel the default linked
// server follows, so the client never looks on a channel its server is
// not on. Without a channel to follow it checks stable, and beta until
// the first stable exists.
type FollowedChecker struct {
	baseURL string
	channel func(ctx context.Context) string
	build   func(channel string) ReleaseChecker
	now     func() time.Time

	mu    sync.Mutex
	made  map[string]ReleaseChecker
	known string
	at    time.Time
	// reading is closed when the read in flight ends.
	reading chan struct{}
}

// NewFollowedChecker follows the channel that channel returns, "" when
// there is none to follow. Each channel keeps its high-water mark in the
// client's update.json beside config.json.
func NewFollowedChecker(baseURL string, ttl time.Duration, channel func(ctx context.Context) string) (*FollowedChecker, error) {
	dir, err := Dir()
	if err != nil {
		return nil, fmt.Errorf("update state: %w", err)
	}
	state := filepath.Join(dir, "update.json")
	return &FollowedChecker{
		baseURL: baseURL,
		channel: channel,
		now:     time.Now,
		build: func(channel string) ReleaseChecker {
			return selfupdate.NewChannelChecker(baseURL, ttl, channel, state)
		},
	}, nil
}

func (f *FollowedChecker) BaseURL() string { return f.baseURL }

func (f *FollowedChecker) Check(ctx context.Context) (selfupdate.Check, error) {
	return f.run(ctx, false)
}

func (f *FollowedChecker) CheckFresh(ctx context.Context) (selfupdate.Check, error) {
	return f.run(ctx, true)
}

func (f *FollowedChecker) run(ctx context.Context, fresh bool) (selfupdate.Check, error) {
	ask := ReleaseChecker.Check
	if fresh {
		ask = ReleaseChecker.CheckFresh
	}
	channel := f.followed(ctx, fresh)
	if channel != "" {
		return ask(f.checker(channel), ctx)
	}
	// Until stable.json exists the stable checker finds nothing, and the
	// beta channel is what an install with no server to follow gets.
	got, err := ask(f.checker(selfupdate.ChannelStable), ctx)
	if err != nil || got.Latest != "" {
		return got, err
	}
	return ask(f.checker(selfupdate.ChannelBeta), ctx)
}

// followed is the channel of the default server: the last one read when
// the server does not answer, "" when none was ever read. Every attempt,
// answered or not, is reused for channelTTL, so a server that is down or
// predates channels is dialed once per period and not once per check. One
// read runs at a time and the others wait for it, outside the lock.
func (f *FollowedChecker) followed(ctx context.Context, fresh bool) string {
	f.mu.Lock()
	if !fresh && !f.at.IsZero() && f.now().Sub(f.at) < channelTTL {
		defer f.mu.Unlock()
		return f.known
	}
	if reading := f.reading; reading != nil {
		f.mu.Unlock()
		select {
		case <-reading:
		case <-ctx.Done():
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.known
	}
	reading := make(chan struct{})
	f.reading = reading
	f.mu.Unlock()

	channel := f.channel(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()
	if channel != "" {
		f.known = channel
	}
	f.at = f.now()
	f.reading = nil
	close(reading)
	return f.known
}

func (f *FollowedChecker) checker(channel string) ReleaseChecker {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.made == nil {
		f.made = map[string]ReleaseChecker{}
	}
	if f.made[channel] == nil {
		f.made[channel] = f.build(channel)
	}
	return f.made[channel]
}

// DefaultLinkChannel is the release channel the default linked server
// follows, read from the top-level fields of config.json: gui_server and
// a named profile do not decide it. It is "" when no server is linked,
// the server does not answer, or it predates channels.
func DefaultLinkChannel(ctx context.Context) string {
	cfg, err := Load()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := DialContext(ctx, cfg)
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	// Calls take no context; closing the connection ends one stalled
	// past the timeout.
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	c, err := conn.Control()
	if err != nil {
		return ""
	}
	defer func() { _ = c.Close() }()
	var status protocol.ServerUpdateStatusResult
	if err := c.Call(protocol.MethodServerUpdateStatus, struct{}{}, &status); err != nil {
		return ""
	}
	return status.Channel
}
