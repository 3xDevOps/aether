package edgeproto

import (
	"net/netip"
	"time"
)

// Relay limits. No idle timeout applies to a spliced stream: an idle
// terminal is legitimate.
const (
	// MaxSSHConnsPerServer stays below sshd's relayed handshake budget.
	MaxSSHConnsPerServer   = 48
	MaxConnsPerDevice      = 16
	MaxUnclaimedPerAddress = 3
	// UnclaimedTTL is how long an unclaimed registration is kept.
	UnclaimedTTL = 30 * time.Minute
	// AttachDeadline is how long the edge waits for a data socket after
	// sending open.
	AttachDeadline      = 10 * time.Second
	PingInterval        = 20 * time.Second
	ControlIdleTimeout  = 60 * time.Second
	ReconnectMinBackoff = 1 * time.Second
	ReconnectMaxBackoff = 60 * time.Second
)

// Credential lifetimes.
const (
	InvitationTTL = 7 * 24 * time.Hour
	SessionIdle   = 30 * 24 * time.Hour
	// IdentityMaxAge is how long after the provider last reported an
	// account's login and email they match invitations. A login or email
	// can move to another person, and the edge learns so only when one
	// of them signs in.
	IdentityMaxAge = 24 * time.Hour
)

// RateLimitKey is the narrowest address block a limit counts against:
// the address for IPv4, its /64 for IPv6. It is the zero Prefix for the
// zero Addr.
func RateLimitKey(addr netip.Addr) netip.Prefix {
	addr = addr.Unmap()
	bits := 64
	if addr.Is4() {
		bits = 32
	}
	p, _ := addr.Prefix(bits) // bits never exceeds a valid addr.BitLen()
	return p
}

// RateLimitKeys returns every block a limit counts addr against:
// RateLimitKey(addr), and for IPv6 also its /56 and /48. One site
// commonly holds a whole /56 or /48, and one free tunnel broker account
// holds a /48: counted by /64 alone, each of its 65536 /64s would get a
// budget of its own.
func RateLimitKeys(addr netip.Addr) []netip.Prefix {
	key := RateLimitKey(addr)
	if !key.Addr().Is6() {
		return []netip.Prefix{key}
	}
	p56, _ := key.Addr().Prefix(56)
	p48, _ := key.Addr().Prefix(48)
	return []netip.Prefix{key, p56, p48}
}

// RateLimitScale is how many times the budget of one RateLimitKey block
// the block p gets: 4 for an IPv6 /56, 16 for an IPv6 /48, and 1
// otherwise.
func RateLimitScale(p netip.Prefix) int {
	if !p.Addr().Is6() {
		return 1
	}
	switch p.Bits() {
	case 56:
		return 4
	case 48:
		return 16
	}
	return 1
}
