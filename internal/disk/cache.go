package disk

import (
	"sync"
	"time"
)

// DefaultCacheTTL is how long a Cache reuses a directory walk. The gauge is
// refreshed on every event batch the dashboard receives, and the numbers it
// shows move over minutes, not seconds.
const DefaultCacheTTL = 30 * time.Second

// Cache serves gauge readings without re-walking the data directory on
// every request.
//
// The two halves of a reading cost very different things. The filesystem
// headroom is one statfs and is taken fresh every time. Component walks and
// optional read-only enrichment (Docker and durable ownership) are reused for
// TTL so dashboard refreshes do not repeatedly scan persistent storage.
type Cache struct {
	dataDir string
	ttl     time.Duration
	now     func() time.Time
	enrich  func(*Usage)

	mu     sync.Mutex
	at     time.Time
	walked bool
	sizes  Usage
}

// NewCache returns a Cache over dataDir. A non-positive ttl applies
// DefaultCacheTTL.
func NewCache(dataDir string, ttl time.Duration, enrich func(*Usage)) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{dataDir: dataDir, ttl: ttl, now: time.Now, enrich: enrich}
}

// Usage returns a reading with fresh filesystem headroom and component
// sizes no older than the TTL.
func (c *Cache) Usage() (Usage, error) {
	u, err := filesystem(c.dataDir)
	if err != nil {
		return Usage{}, err
	}
	return u.withComponents(c.sizesNow()), nil
}

// sizesNow returns the cached walk, refreshing it when it has expired. The
// walk runs under the lock, so concurrent callers arriving on an expired
// entry wait for one answer instead of each starting their own walk - which
// is the whole point of not walking per request.
func (c *Cache) sizesNow() Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.now(); !c.walked || now.Sub(c.at) >= c.ttl {
		c.sizes = components(c.dataDir)
		if c.enrich != nil {
			c.enrich(&c.sizes)
		}
		c.at, c.walked = now, true
	}
	return c.sizes
}
