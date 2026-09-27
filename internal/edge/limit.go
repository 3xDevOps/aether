package edge

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// maxTracked bounds the address blocks a limiter remembers. When it is
// full of blocks still being limited, new blocks are refused: the limiter
// fails closed instead of growing.
const maxTracked = 1 << 16

// limiter is a token bucket per edgeproto.RateLimitKey block: burst
// requests at once, then one more every interval.
type limiter struct {
	burst    float64
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	buckets map[netip.Prefix]bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(burst int, interval time.Duration, now func() time.Time) *limiter {
	return &limiter{burst: float64(burst), interval: interval, now: now, buckets: map[netip.Prefix]bucket{}}
}

// allow spends one request of r's address block and reports whether one
// was left. The address is the connection's own peer address; no
// forwarded-for header is read.
func (l *limiter) allow(r *http.Request) bool {
	var addr netip.Addr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		addr = ap.Addr()
	}
	key := edgeproto.RateLimitKey(addr)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxTracked {
			l.prune(now)
			if len(l.buckets) >= maxTracked {
				return false
			}
		}
		b = bucket{tokens: l.burst, at: now}
	}
	b = l.refill(b, now)
	if b.tokens < 1 {
		l.buckets[key] = b
		return false
	}
	b.tokens--
	l.buckets[key] = b
	return true
}

func (l *limiter) refill(b bucket, now time.Time) bucket {
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+float64(elapsed)/float64(l.interval))
		b.at = now
	}
	return b
}

// prune forgets blocks whose bucket has refilled: they are as if unseen.
func (l *limiter) prune(now time.Time) {
	for key, b := range l.buckets {
		if l.refill(b, now).tokens >= l.burst {
			delete(l.buckets, key)
		}
	}
}
