package disk

import (
	"context"
	"errors"
	"sync"
	"time"
)

// DefaultCacheTTL is how old the last finished walk may get before a reading
// starts the next one. On a busy server one walk is millions of stat calls
// and gigabytes of kernel inode cache, while the breakdown it feeds moves over
// minutes.
const DefaultCacheTTL = 5 * time.Minute

// firstWalkWait bounds how long a reading waits for the first walk. It stays
// under the dashboard's 10 s abort and the local gateway's 60 s call watchdog,
// so the caller gets ErrMeasuring instead of its own timeout; over SSH the
// context ends only when the caller closes the channel.
const firstWalkWait = 5 * time.Second

// ErrMeasuring is returned until the first walk finishes. It names no path,
// so it is safe to show to clients.
var ErrMeasuring = errors.New("disk usage is still being measured: the server's first scan of its data directory has not finished")

// Cache serves gauge readings without making a request wait on a walk of
// the data directory.
//
// The filesystem headroom is one statfs and is taken fresh every time.
// Component sizes and optional read-only enrichment (Docker and durable
// ownership) come from the last finished walk. A reading that finds that walk
// older than the TTL starts the next one in the background and still answers
// from the old one. One walk runs at a time.
type Cache struct {
	dataDir string
	ttl     time.Duration
	now     func() time.Time
	enrich  func(*Usage)

	mu     sync.Mutex
	at     time.Time
	walked bool
	sizes  Usage
	// walking is closed when the running walk finishes, and nil when none runs.
	walking chan struct{}
}

// NewCache returns a Cache over dataDir. A non-positive ttl applies
// DefaultCacheTTL.
func NewCache(dataDir string, ttl time.Duration, enrich func(*Usage)) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{dataDir: dataDir, ttl: ttl, now: time.Now, enrich: enrich}
}

// Usage returns a reading with fresh filesystem headroom and the component
// sizes of the last finished walk. Before the first walk finishes it waits
// for it until ctx ends or firstWalkWait passes, then returns ErrMeasuring.
func (c *Cache) Usage(ctx context.Context) (Usage, error) {
	u, err := filesystem(c.dataDir)
	if err != nil {
		return Usage{}, err
	}
	sizes, err := c.latest(ctx)
	if err != nil {
		return Usage{}, err
	}
	return u.withComponents(sizes), nil
}

func (c *Cache) latest(ctx context.Context) (Usage, error) {
	c.mu.Lock()
	if c.walking == nil && (!c.walked || c.now().Sub(c.at) >= c.ttl) {
		c.walking = make(chan struct{})
		go c.walk(c.walking)
	}
	sizes, walked, walking := c.sizes, c.walked, c.walking
	c.mu.Unlock()
	if walked {
		return sizes, nil
	}
	ctx, cancel := context.WithTimeout(ctx, firstWalkWait)
	defer cancel()
	select {
	case <-walking:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.sizes, nil
	case <-ctx.Done():
		return Usage{}, ErrMeasuring
	}
}

// walk stamps its result when it finishes, so a walk longer than the TTL is
// not stale on arrival.
func (c *Cache) walk(done chan struct{}) {
	sizes := components(c.dataDir)
	if c.enrich != nil {
		c.enrich(&sizes)
	}
	c.mu.Lock()
	c.sizes, c.at, c.walked, c.walking = sizes, c.now(), true, nil
	c.mu.Unlock()
	close(done)
}
