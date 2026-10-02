package edge

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// maxTracked bounds the keys a limiter remembers. When it is full, a new
// key takes the place of the fullest of evictSample tracked keys. A key
// whose bucket has refilled is as if unseen, so this forgets the least
// possible; refusing new keys instead would let anyone holding 65536
// address blocks lock everyone else out.
const (
	maxTracked  = 1 << 16
	evictSample = 64
)

// limiter is a token bucket per key: burst requests at once, then one
// more every interval. scale, when set, multiplies both for a key.
type limiter[K comparable] struct {
	burst    float64
	interval time.Duration
	scale    func(K) int
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

// newAddrLimiter limits client addresses by the blocks addrKeys returns,
// each with its edgeproto.RateLimitScale times the budget.
func newAddrLimiter(burst int, interval time.Duration, now func() time.Time) *limiter[netip.Prefix] {
	l := newLimiter[netip.Prefix](burst, interval, now)
	l.scale = edgeproto.RateLimitScale
	return l
}

// clientAddr is r's client address: the connection's peer, or behind a
// reverse proxy the address relay.Forwarded put in RemoteAddr. It is the
// zero Addr when RemoteAddr does not parse.
func clientAddr(r *http.Request) netip.Addr {
	ap, _ := netip.ParseAddrPort(r.RemoteAddr)
	return ap.Addr().Unmap()
}

// addrKeys are the edgeproto.RateLimitKeys blocks r's peer address counts
// against.
func addrKeys(r *http.Request) []netip.Prefix {
	return edgeproto.RateLimitKeys(clientAddr(r))
}

// allow spends one request of every key and reports whether each had one
// left. A refused request spends nothing.
func (l *limiter[K]) allow(keys ...K) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		if l.bucket(key, now).tokens < 1 {
			return false
		}
	}
	for _, key := range keys {
		b := l.bucket(key, now)
		b.tokens--
		if _, ok := l.buckets[key]; !ok && len(l.buckets) >= maxTracked {
			l.evict(now)
		}
		l.buckets[key] = b
	}
	return true
}

func (l *limiter[K]) scaleOf(key K) float64 {
	if l.scale == nil {
		return 1
	}
	return float64(l.scale(key))
}

// bucket returns key's bucket refilled to now; an unseen key's is full.
func (l *limiter[K]) bucket(key K, now time.Time) bucket {
	scale := l.scaleOf(key)
	b, ok := l.buckets[key]
	if !ok {
		return bucket{tokens: l.burst * scale, at: now}
	}
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens = min(l.burst*scale, b.tokens+scale*float64(elapsed)/float64(l.interval))
		b.at = now
	}
	return b
}

// evict forgets the fullest of evictSample keys. Map iteration starts at
// a random key, so the sample differs from call to call.
func (l *limiter[K]) evict(now time.Time) {
	var victim K
	fullest, n := -1.0, 0
	for key := range l.buckets {
		if full := l.bucket(key, now).tokens / l.scaleOf(key); full > fullest {
			victim, fullest = key, full
		}
		if n++; n == evictSample {
			break
		}
	}
	delete(l.buckets, victim)
}
