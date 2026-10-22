// Package sysstats samples host and per-container resource usage for the
// dashboard's System view.
package sysstats

import (
	"context"
	"fmt"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	Interval  = 5 * time.Second
	Retention = time.Hour

	watchTTL  = time.Minute
	wakeDelay = time.Second
)

type ContainerSource interface {
	ContainerStats(ctx context.Context) ([]runtime.ContainerStat, error)
}

// Options configures a Sampler. Zero values read the real /proc and /sys.
type Options struct {
	ProcRoot string
	SysRoot  string
	// Source is nil when the runtime cannot report container stats.
	Source ContainerSource
	Now    func() time.Time
	GOOS   string
}

// Owner.Kind is "run", "browser" (a run's browser sidecar), "member" (a
// member's environment) or empty for a container with no owner.
type Owner struct {
	Kind        string
	ID          string
	MemberID    string
	WorkspaceID string
	Title       string
	Status      string
}

type Rates struct {
	CPUCores         float64
	ThrottledPercent *float64
	NetRxBps         float64
	NetTxBps         float64
	DiskReadBps      float64
	DiskWriteBps     float64
}

// ContainerUsage is one container in the latest snapshot. Rates is nil until
// a second sample exists.
type ContainerUsage struct {
	Owner               Owner
	ID                  string
	CreationKey         string
	BrowserRunContainer string
	CPULimitCores       float64
	MemoryBytes         uint64
	MemoryLimitBytes    uint64
	Pids                uint64
	PidsLimit           uint64
	Rates               *Rates
}

// Snapshot is what one Read returns. Containers is empty and ContainersAt zero
// when no container sample is fresh.
type Snapshot struct {
	Host            HostInfo
	HostError       string
	Samples         []HostSample
	ContainersAt    time.Time
	ContainersError string
	Containers      []ContainerUsage
}

// Sampler keeps an hour of host samples and, while someone is watching, the
// latest container sample.
type Sampler struct {
	opts Options

	interval time.Duration
	delay    time.Duration
	ttl      time.Duration
	capacity int

	wake chan struct{}

	mu        sync.Mutex
	prevHost  *hostReading
	info      HostInfo
	hostErr   string
	samples   []HostSample
	watchedAt time.Time

	containersAt  time.Time
	containersErr string
	containers    []ContainerUsage
	prevStats     map[string]runtime.ContainerStat
	running       bool
}

func New(opts Options) *Sampler {
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.SysRoot == "" {
		opts.SysRoot = "/sys"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.GOOS == "" {
		opts.GOOS = goruntime.GOOS
	}
	return &Sampler{
		opts:     opts,
		interval: Interval,
		delay:    wakeDelay,
		ttl:      watchTTL,
		capacity: int(Retention / Interval),
		wake:     make(chan struct{}, 1),
	}
}

func (s *Sampler) Run(ctx context.Context) {
	s.sampleHost()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	var second <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleHost()
			if s.watching() {
				s.startContainers(ctx)
			}
		case <-s.wake:
			s.startContainers(ctx)
			second = time.After(s.delay)
		case <-second:
			second = nil
			s.startContainers(ctx)
		}
	}
}

func (s *Sampler) watching() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Now().Sub(s.watchedAt) < s.ttl
}

// Read returns the host samples newer than since and the latest fresh
// container sample. It never waits on a sample in progress.
func (s *Sampler) Read(since time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	if now.Sub(s.watchedAt) >= s.ttl {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	s.watchedAt = now
	snap := Snapshot{Host: s.info, HostError: s.hostErr, ContainersError: s.containersErr}
	for _, h := range s.samples {
		if h.At.After(since) {
			snap.Samples = append(snap.Samples, h)
		}
	}
	if s.opts.Source == nil {
		snap.ContainersError = "container stats need the Docker runtime; this server's runtime does not report them"
	}
	if !s.containersAt.IsZero() && now.Sub(s.containersAt) <= 2*s.interval {
		snap.ContainersAt = s.containersAt
		snap.Containers = append([]ContainerUsage(nil), s.containers...)
	}
	return snap
}

func (s *Sampler) sampleHost() {
	if s.opts.GOOS != "linux" {
		s.mu.Lock()
		s.hostErr = fmt.Sprintf("system stats need a Linux host; this server runs on %s", s.opts.GOOS)
		s.mu.Unlock()
		return
	}
	cur, err := readHost(s.opts.ProcRoot, s.opts.SysRoot, s.opts.Now())
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.hostErr = err.Error()
		return
	}
	s.hostErr, s.info = "", cur.info
	if s.prevHost != nil && cur.at.After(s.prevHost.at) {
		s.samples = append(s.samples, cur.sampleSince(*s.prevHost))
		if extra := len(s.samples) - s.capacity; extra > 0 {
			s.samples = append([]HostSample(nil), s.samples[extra:]...)
		}
	}
	s.prevHost = &cur
}

func (s *Sampler) startContainers(ctx context.Context) {
	if s.opts.Source == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go func() {
		stats, err := s.opts.Source.ContainerStats(ctx)
		s.finishContainers(stats, err)
	}()
}

func (s *Sampler) finishContainers(stats []runtime.ContainerStat, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	if err != nil {
		s.containersErr = err.Error()
	} else {
		s.containersErr = ""
	}
	if stats == nil {
		return
	}
	now := s.opts.Now()
	// Counters from before a watch gap would average the whole gap into one rate.
	if now.Sub(s.containersAt) > 3*s.interval {
		s.prevStats = nil
	}
	s.containersAt = now
	usage := make([]ContainerUsage, 0, len(stats))
	next := make(map[string]runtime.ContainerStat, len(stats))
	for _, st := range stats {
		usage = append(usage, containerUsage(st, s.prevStats[st.ID]))
		next[st.ID] = st
	}
	s.containers, s.prevStats = usage, next
}

func containerUsage(cur, prev runtime.ContainerStat) ContainerUsage {
	u := ContainerUsage{
		ID:                  cur.ID,
		CreationKey:         cur.CreationKey,
		BrowserRunContainer: cur.BrowserRunContainer,
		CPULimitCores:       cur.CPULimitCores,
		MemoryBytes:         cur.MemoryBytes,
		MemoryLimitBytes:    cur.MemoryLimitBytes,
		Pids:                cur.Pids,
		PidsLimit:           cur.PidsLimit,
	}
	secs := cur.Read.Sub(prev.Read).Seconds()
	if prev.ID == "" || secs <= 0 {
		return u
	}
	r := &Rates{
		CPUCores:     float64(delta(prev.CPUUsageNanos, cur.CPUUsageNanos)) / (secs * 1e9),
		NetRxBps:     float64(delta(prev.NetRxBytes, cur.NetRxBytes)) / secs,
		NetTxBps:     float64(delta(prev.NetTxBytes, cur.NetTxBytes)) / secs,
		DiskReadBps:  float64(delta(prev.DiskReadBytes, cur.DiskReadBytes)) / secs,
		DiskWriteBps: float64(delta(prev.DiskWriteBytes, cur.DiskWriteBytes)) / secs,
	}
	if periods := delta(prev.CPUPeriods, cur.CPUPeriods); periods > 0 {
		pct := float64(delta(prev.CPUThrottledPeriods, cur.CPUThrottledPeriods)) / float64(periods) * 100
		r.ThrottledPercent = &pct
	}
	u.Rates = r
	return u
}
