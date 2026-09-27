package edgeclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

const (
	// defaultInterval and slowDownStep are RFC 8628's.
	defaultInterval = 5
	slowDownStep    = 5
	// maxLoginWait bounds a sign-in whatever lifetime the edge announces.
	maxLoginWait = 30 * time.Minute
)

// pollUnit is one second of the edge's polling interval; tests shorten it.
var pollUnit = time.Second

// Login is a device sign-in waiting for the person to confirm UserCode at
// VerificationURI.
type Login struct {
	UserCode        string
	VerificationURI string
	deviceCode      string
	key             string
	interval        time.Duration
	deadline        time.Time
}

// StartLogin creates the device key if needed and registers this device
// with the edge under label.
func (c *Client) StartLogin(ctx context.Context, label string) (*Login, error) {
	signer, err := EnsureDeviceKey(c.dir)
	if err != nil {
		return nil, err
	}
	req := edgeproto.DeviceStartRequest{Label: label, Key: edgeproto.DeviceKeyLine(signer.PublicKey())}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("sign in: device label %q: %w", label, err)
	}
	var resp edgeproto.DeviceStartResponse
	if err := c.call(ctx, "sign in", http.MethodPost, edgeproto.PathDeviceStart, "", req, &resp); err != nil {
		return nil, err
	}
	// Both values are printed and the URI is opened in a browser, so an
	// edge may only send the person to its own pages.
	if !c.onEdge(resp.VerificationURI) || !printable(resp.VerificationURI) {
		return nil, fmt.Errorf("sign in: %s sent verification address %q, which is not on %s",
			c.host, clean(resp.VerificationURI), c.origin)
	}
	if resp.DeviceCode == "" || resp.UserCode == "" || !printable(resp.UserCode) || !printable(resp.DeviceCode) {
		return nil, fmt.Errorf("sign in: %s sent a malformed device authorization", c.host)
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	wait := time.Duration(resp.ExpiresIn) * time.Second
	if wait <= 0 || wait > maxLoginWait {
		wait = maxLoginWait
	}
	return &Login{
		UserCode:        resp.UserCode,
		VerificationURI: resp.VerificationURI,
		deviceCode:      resp.DeviceCode,
		key:             req.Key,
		interval:        time.Duration(interval) * pollUnit,
		deadline:        time.Now().Add(wait),
	}, nil
}

// onEdge reports whether raw is an address on this client's edge.
func (c *Client) onEdge(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Scheme+"://"+strings.ToLower(u.Host) == c.origin
}

// Wait polls the edge until the person confirms or refuses the sign-in,
// or its code expires, and stores the device token it receives.
func (c *Client) Wait(ctx context.Context, l *Login) (Session, error) {
	ctx, cancel := context.WithDeadline(ctx, l.deadline)
	defer cancel()
	interval := l.interval
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Session{}, fmt.Errorf("sign in: code %s expired before it was confirmed; run aether login again", l.UserCode)
			}
			return Session{}, ctx.Err()
		case <-timer.C:
		}
		var resp edgeproto.DeviceTokenResponse
		err := c.call(ctx, "sign in", http.MethodPost, edgeproto.PathDeviceToken, "",
			edgeproto.DeviceTokenRequest{DeviceCode: l.deviceCode}, &resp)
		var refused *RefusedError
		switch {
		case err == nil:
			return c.store(l, resp)
		case !errors.As(err, &refused) || refused.Status != http.StatusBadRequest:
			return Session{}, err
		case refused.Message == edgeproto.DevicePending:
		case refused.Message == edgeproto.DeviceSlowDown:
			interval += slowDownStep * pollUnit
		case refused.Message == edgeproto.DeviceDenied:
			return Session{}, fmt.Errorf("sign in: code %s was refused at %s", l.UserCode, c.host)
		case refused.Message == edgeproto.DeviceExpired:
			return Session{}, fmt.Errorf("sign in: code %s expired before it was confirmed; run aether login again", l.UserCode)
		default:
			return Session{}, err
		}
		timer.Reset(interval)
	}
}

// store checks an approved sign-in and saves its token.
func (c *Client) store(l *Login, resp edgeproto.DeviceTokenResponse) (Session, error) {
	if !edgeproto.ValidToken(resp.Token) {
		return Session{}, fmt.Errorf("sign in: %s sent a malformed device token", c.host)
	}
	if err := resp.Account.Validate(); err != nil {
		return Session{}, fmt.Errorf("sign in: %s sent a malformed account: %w", c.host, err)
	}
	// The edge binds the token to the key this device registered; any
	// other key means the answer is not for this device.
	if resp.Device.Key != l.key || resp.Device.ID == "" || !printable(resp.Device.ID) || !printable(resp.Device.Label) {
		return Session{}, fmt.Errorf("sign in: %s answered for a different device", c.host)
	}
	s := stored{Token: resp.Token, Session: Session{Device: resp.Device, Account: resp.Account}}
	if err := updateTokens(c.dir, func(f tokensFile) { f.Edges[c.origin] = s }); err != nil {
		return Session{}, err
	}
	return s.Session, nil
}

// Logout revokes this machine's device token at the edge and forgets it.
// A token the edge already refuses is forgotten too. When the edge cannot
// be reached the token is kept, so the revocation can be retried.
func (c *Client) Logout(ctx context.Context) error {
	s, err := c.session()
	if err != nil {
		return err
	}
	err = c.call(ctx, "sign out", http.MethodPost, edgeproto.PathLogout, s.Token, nil, nil)
	var refused *RefusedError
	if err != nil && (!errors.As(err, &refused) || refused.Status != http.StatusUnauthorized) {
		return fmt.Errorf("%w; the token is still valid and kept in %s so you can retry", err, tokensPath(c.dir))
	}
	return updateTokens(c.dir, func(f tokensFile) { delete(f.Edges, c.origin) })
}
