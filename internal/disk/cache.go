package disk

import (
	"path/filepath"
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
// headroom is one statfs and is taken fresh every time, so the gauge never
// lags the disk actually filling. The component sizes are a full walk of
// the checkouts and transcripts trees - a run checkout is a whole clone -
// and are reused for TTL. A dashboard refreshing every couple of seconds
// must not turn into a couple of tree walks per second.
type Cache struct {
	dataDir string
	ttl     time.Duration
	now     func() time.Time

	mu     sync.Mutex
	at     time.Time
	walked bool
	sizes  Usage
}

// NewCache returns a Cache over dataDir. A non-positive ttl applies
// DefaultCacheTTL.
func NewCache(dataDir string, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{dataDir: dataDir, ttl: ttl, now: time.Now}
}

// Usage returns a reading with fresh filesystem headroom and component
// sizes no older than the TTL.
func (c *Cache) Usage() (Usage, error) {
	u, err := filesystem(c.dataDir)
	if err != nil {
		return Usage{}, err
	}
	sizes := c.sizesNow()
	u.WorktreeBytes = sizes.WorktreeBytes
	u.TranscriptBytes = sizes.TranscriptBytes
	u.DatabaseBytes = sizes.DatabaseBytes
	return u, nil
}

// sizesNow returns the cached walk, refreshing it when it has expired. The
// walk runs under the lock, so concurrent callers arriving on an expired
// entry wait for one answer instead of each starting their own walk - which
// is the whole point of not walking per request.
func (c *Cache) sizesNow() Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.now(); !c.walked || now.Sub(c.at) >= c.ttl {
		c.sizes = Usage{
			WorktreeBytes:   treeBytes(filepath.Join(c.dataDir, checkoutsDir)),
			TranscriptBytes: treeBytes(filepath.Join(c.dataDir, transcriptsDir)),
			DatabaseBytes:   databaseBytes(filepath.Join(c.dataDir, databaseFile)),
		}
		c.at, c.walked = now, true
	}
	return c.sizes
}
