package edgeproto

import (
	"net/netip"
	"time"
)

// Relay limits. No idle timeout applies to a spliced stream: an idle
// terminal is legitimate.
const (
	// MaxConnsPerServer stays below sshd's relayed handshake budget.
	MaxConnsPerServer      = 48
	MaxConnsPerDevice      = 16
	MaxUnclaimedPerAddress = 3
	// UnclaimedTTL is how long an unclaimed registration is kept.
	UnclaimedTTL = 30 * time.Minute
	// AttachDeadline is how long the edge waits for a data socket after
	// sending open.
	AttachDeadline      = 10 * time.Second
	ClientHelloTimeout  = 5 * time.Second
	MaxClientHelloSize  = 16 << 10
	PingInterval        = 20 * time.Second
	ControlIdleTimeout  = 60 * time.Second
	ReconnectMinBackoff = 1 * time.Second
	ReconnectMaxBackoff = 60 * time.Second
)

// Credential lifetimes.
const (
	InvitationTTL = 7 * 24 * time.Hour
	SessionIdle   = 30 * 24 * time.Hour
)

// RateLimitKey is the address block sign-in and claim rate limits count
// against: the address for IPv4, its /64 for IPv6. It is the zero Prefix
// for the zero Addr.
func RateLimitKey(addr netip.Addr) netip.Prefix {
	addr = addr.Unmap()
	bits := 64
	if addr.Is4() {
		bits = 32
	}
	p, _ := addr.Prefix(bits) // bits never exceeds a valid addr.BitLen()
	return p
}
