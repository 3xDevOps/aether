package edge

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// maxTracked bounds the keys a limiter remembers. When it is full of keys
// still being limited, new keys are refused: the limiter fails closed
// instead of growing.
const maxTracked = 1 << 16

// limiter is a token bucket per key: burst requests at once, then one
// more every interval.
type limiter[K comparable] struct {
	burst    float64
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	buckets map[K]bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter[K comparable](burst int, interval time.Duration, now func() time.Time) *limiter[K] {
	return &limiter[K]{burst: float64(burst), interval: interval, now: now, buckets: map[K]bucket{}}
}

// clientAddr is the connection's own peer address; no forwarded-for
// header is read. It is the zero Addr when RemoteAddr does not parse.
func clientAddr(r *http.Request) netip.Addr {
	ap, _ := netip.ParseAddrPort(r.RemoteAddr)
	return ap.Addr().Unmap()
}

// addrKey is the edgeproto.RateLimitKey block r's peer address counts
// against.
func addrKey(r *http.Request) netip.Prefix {
	return edgeproto.RateLimitKey(clientAddr(r))
}

// allow spends one request of key and reports whether one was left.
func (l *limiter[K]) allow(key K) bool {
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

func (l *limiter[K]) refill(b bucket, now time.Time) bucket {
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+float64(elapsed)/float64(l.interval))
		b.at = now
	}
	return b
}

// prune forgets keys whose bucket has refilled: they are as if unseen.
func (l *limiter[K]) prune(now time.Time) {
	for key, b := range l.buckets {
		if l.refill(b, now).tokens >= l.burst {
			delete(l.buckets, key)
		}
	}
}
