package acphost

import (
	"fmt"
	"os"
	"sync"
)

const readOnlyLogCacheEntries = 8

// ReadOnlyLogCache reuses immutable indexes for recently read, unchanged logs.
// It holds neither item payloads nor open files. Its zero value is ready to use.
// Each Open returns an independently owned read-only Log that must be closed.
type ReadOnlyLogCache struct {
	mu      sync.Mutex
	entries [readOnlyLogCacheEntries]*cachedLogIndex
}

type cachedLogIndex struct {
	path  string
	mu    sync.Mutex
	info  os.FileInfo
	index logIndex
}

// Open validates the current file before reusing an index. Indexing the same
// path is serialized, while reads of other cached paths remain independent.
func (c *ReadOnlyLogCache) Open(path string) (*Log, error) {
	entry := c.entry(path)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		entry.info, entry.index = nil, logIndex{}
		return nil, fmt.Errorf("acphost: open item log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acphost: stat item log: %w", err)
	}
	l := &Log{path: path, f: f, readOnly: true}
	if sameLogFile(entry.info, info) {
		l.logIndex = entry.index
		return l, nil
	}

	entry.info, entry.index = nil, logIndex{}
	if indexErr := l.indexSize(info.Size()); indexErr != nil {
		_ = f.Close()
		return nil, indexErr
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acphost: stat indexed item log: %w", err)
	}
	// A concurrent append may leave a valid snapshot, but it must not be
	// published as an index of a different file size or modification time.
	if sameLogFile(info, after) {
		entry.info, entry.index = info, l.logIndex
	}
	return l, nil
}

func sameLogFile(a, b os.FileInfo) bool {
	return a != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (c *ReadOnlyLogCache) entry(path string) *cachedLogIndex {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, entry := range c.entries {
		if entry != nil && entry.path == path {
			copy(c.entries[1:i+1], c.entries[:i])
			c.entries[0] = entry
			return entry
		}
	}
	entry := &cachedLogIndex{path: path}
	copy(c.entries[1:], c.entries[:len(c.entries)-1])
	c.entries[0] = entry
	return entry
}
