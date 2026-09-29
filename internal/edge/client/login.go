package edgeclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
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
// VerificationURI, a page on SigninOrigin, the sign-in origin the edge's
// metadata named.
type Login struct {
	UserCode        string
	VerificationURI string
	SigninOrigin    string
	deviceCode      string
	key             string
	interval        time.Duration
	deadline        time.Time
}

// StartLogin creates the device key if needed, reads the sign-in origin
// from the edge's metadata, and registers this device there under label.
func (c *Client) StartLogin(ctx context.Context, label string) (*Login, error) {
	signer, err := EnsureDeviceKey(c.dir)
	if err != nil {
		return nil, err
	}
	req := edgeproto.DeviceStartRequest{Label: label, Key: edgeproto.DeviceKeyLine(signer.PublicKey())}
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("sign in: device label %q: %w", label, err)
	}
	info, err := c.metadata(ctx)
	if err != nil {
		return nil, err
	}
	signin, host := info.SigninOrigin, HostOf(info.SigninOrigin)
	var resp edgeproto.DeviceStartResponse
	if err = c.call(ctx, "sign in", http.MethodPost, signin, edgeproto.PathDeviceStart, "", req, &resp); err != nil {
		return nil, err
	}
	// Both values are printed and the URI is opened in a browser, so an
	// edge may only send the person to its own sign-in pages.
	if !onOrigin(resp.VerificationURI, signin) || !printable(resp.VerificationURI) {
		return nil, fmt.Errorf("sign in: %s sent verification address %q, which is not on %s",
			host, clean(resp.VerificationURI), signin)
	}
	if resp.DeviceCode == "" || resp.UserCode == "" || !printable(resp.UserCode) || !printable(resp.DeviceCode) {
		return nil, fmt.Errorf("sign in: %s sent a malformed device authorization", host)
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
		SigninOrigin:    signin,
		deviceCode:      resp.DeviceCode,
		key:             req.Key,
		interval:        time.Duration(interval) * pollUnit,
		deadline:        time.Now().Add(wait),
	}, nil
}

// onOrigin reports whether raw is an address on origin.
func onOrigin(raw, origin string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Scheme+"://"+strings.ToLower(u.Host) == origin
}

// Wait polls the edge until the person confirms or refuses the sign-in,
// or its code expires, and stores the device token it receives. A poll
// that does not reach the edge, or that a 5xx answers, is retried: the
// edge keeps the pending sign-in, and the person may already have
// confirmed it.
func (c *Client) Wait(ctx context.Context, l *Login) (Session, error) {
	ctx, cancel := context.WithDeadline(ctx, l.deadline)
	defer cancel()
	interval := l.interval
	timer := time.NewTimer(interval)
	defer timer.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Session{}, ctx.Err()
			}
			if lastErr != nil {
				return Session{}, fmt.Errorf("sign in: code %s expired before it was confirmed; run aether login again; the last poll failed: %w", l.UserCode, lastErr)
			}
			return Session{}, fmt.Errorf("sign in: code %s expired before it was confirmed; run aether login again", l.UserCode)
		case <-timer.C:
		}
		var resp edgeproto.DeviceTokenResponse
		err := c.call(ctx, "sign in", http.MethodPost, l.SigninOrigin, edgeproto.PathDeviceToken, "",
			edgeproto.DeviceTokenRequest{DeviceCode: l.deviceCode}, &resp)
		var (
			refused   *RefusedError
			transport *url.Error
		)
		switch {
		case err == nil:
			return c.store(l, resp)
		case ctx.Err() != nil:
			// The poll failed because the wait ended; the select says why.
		case errors.As(err, &transport) || (errors.As(err, &refused) && refused.Status >= http.StatusInternalServerError):
			lastErr = err
		case !errors.As(err, &refused) || refused.Status != http.StatusBadRequest:
			return Session{}, err
		case refused.Message == edgeproto.DevicePending:
			lastErr = nil
		case refused.Message == edgeproto.DeviceSlowDown:
			lastErr = nil
			interval += slowDownStep * pollUnit
		case refused.Message == edgeproto.DeviceDenied:
			return Session{}, fmt.Errorf("sign in: code %s was refused at %s", l.UserCode, HostOf(l.SigninOrigin))
		case refused.Message == edgeproto.DeviceExpired:
			return Session{}, fmt.Errorf("sign in: code %s expired before it was confirmed; run aether login again", l.UserCode)
		default:
			return Session{}, err
		}
		timer.Reset(interval)
	}
}

// store checks an approved sign-in and saves its token with the sign-in
// origin that issued it.
func (c *Client) store(l *Login, resp edgeproto.DeviceTokenResponse) (Session, error) {
	host := HostOf(l.SigninOrigin)
	if !edgeproto.ValidToken(resp.Token) {
		return Session{}, fmt.Errorf("sign in: %s sent a malformed device token", host)
	}
	if err := resp.Account.Validate(); err != nil {
		return Session{}, fmt.Errorf("sign in: %s sent a malformed account: %w", host, err)
	}
	// The edge binds the token to the key this device registered; any
	// other key means the answer is not for this device.
	if resp.Device.Key != l.key || resp.Device.ID == "" || !printable(resp.Device.ID) || !printable(resp.Device.Label) {
		return Session{}, fmt.Errorf("sign in: %s answered for a different device", host)
	}
	s := stored{Token: resp.Token, Session: Session{SigninOrigin: l.SigninOrigin, Device: resp.Device, Account: resp.Account}}
	if err := updateTokens(c.dir, func(f tokensFile) { f.Edges[c.origin] = s }); err != nil {
		return Session{}, err
	}
	return s.Session, nil
}

// Logout revokes this machine's device token at the sign-in origin that
// issued it and deletes it from TokensFile. A token the edge already
// refuses is deleted too. When the edge cannot be reached the token is
// kept, so the revocation can be retried. The device key stays: servers
// know this device by it.
func (c *Client) Logout(ctx context.Context) error {
	s, err := c.session()
	if err != nil {
		return err
	}
	err = c.call(ctx, "sign out", http.MethodPost, s.SigninOrigin, edgeproto.PathLogout, s.Token, nil, nil)
	var refused *RefusedError
	if err != nil && (!errors.As(err, &refused) || refused.Status != http.StatusUnauthorized) {
		return fmt.Errorf("%w; the token is still valid and kept in %s so you can retry", err, tokensPath(c.dir))
	}
	return updateTokens(c.dir, func(f tokensFile) { delete(f.Edges, c.origin) })
}

// Account reads the signed-in account and the servers deleting it would
// touch, from the sign-in origin that issued the token.
func (c *Client) Account(ctx context.Context) (edgeproto.AccountSummary, error) {
	s, err := c.session()
	if err != nil {
		return edgeproto.AccountSummary{}, err
	}
	var sum edgeproto.AccountSummary
	if err := c.call(ctx, "read account", http.MethodGet, s.SigninOrigin, edgeproto.PathAccount, s.Token, nil, &sum); err != nil {
		return edgeproto.AccountSummary{}, err
	}
	if err := sum.Account.Validate(); err != nil {
		return edgeproto.AccountSummary{}, fmt.Errorf("read account: %s sent a malformed account: %w", HostOf(s.SigninOrigin), err)
	}
	for _, info := range append(sum.Owned, sum.Member...) {
		if !edgeproto.ValidServerID(info.ID) || !printable(info.Name) || !printable(info.Role) {
			return edgeproto.AccountSummary{}, fmt.Errorf("read account: %s sent a malformed server entry", HostOf(s.SigninOrigin))
		}
	}
	if !printable(sum.Confirm) {
		return edgeproto.AccountSummary{}, fmt.Errorf("read account: %s sent a malformed confirmation text", HostOf(s.SigninOrigin))
	}
	return sum, nil
}

// DeleteAccount deletes the signed-in account at the edge once confirm,
// as the person typed it, names it, then deletes this machine's token.
// The edge refuses unless the account signed in with its provider in a
// browser within the last few minutes, and its refusal says where.
func (c *Client) DeleteAccount(ctx context.Context, confirm string) error {
	s, err := c.session()
	if err != nil {
		return err
	}
	req := edgeproto.AccountDeleteRequest{Confirm: confirm}
	if err := c.call(ctx, "delete account", http.MethodPost, s.SigninOrigin, edgeproto.PathAccountDelete, s.Token, req, nil); err != nil {
		return err
	}
	return updateTokens(c.dir, func(f tokensFile) { delete(f.Edges, c.origin) })
}
