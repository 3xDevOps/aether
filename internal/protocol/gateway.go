package protocol

// Gateway-facing methods: reads the dashboard renders that have no SSH
// client of their own. They ride the same control channel and error
// contract as every other method.
const (
	// MethodRunPatch renders a run checkout's diff against its fork point.
	MethodRunPatch = "run.patch"
	// MethodServerDisk reads the data directory's disk usage.
	MethodServerDisk = "server.disk"
	// MethodSystemStats reads live host and per-container resource usage.
	MethodSystemStats = "system.stats"
)

// RunPatchParams asks for one run's patch. From and To are snapshot
// trees from run.diff events. Both empty asks for the run's current diff
// against its effective base; From alone for everything that changed since
// that snapshot; both for what that one interval changed. To alone is invalid.
type RunPatchParams struct {
	RunID string `json:"run_id"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// RunPatchResult is the reply to run.patch: the run's unified diff against
// the fork-point commit recorded at checkout creation, or against the from
// tree when a snapshot range was asked for.
type RunPatchResult struct {
	RunID string `json:"run_id"`
	// Base is what the diff is taken against: the effective base commit for a
	// cumulative render, the requested from tree otherwise.
	Base string `json:"base"`
	// Patch is the unified diff text, empty when nothing changed.
	Patch string `json:"patch"`
	// Truncated reports that an explicitly bounded render ended early, at the
	// last whole line that fit. Dashboard renders are complete.
	Truncated bool `json:"truncated"`
	Recorded  bool `json:"recorded,omitempty"`
	// ForkTree is the tree of the commit the run forked from, on renders
	// without From. Passed as From it diffs the run's current state against
	// its starting point.
	ForkTree string `json:"fork_tree,omitempty"`
}

// ServerDiskResult keeps whole-filesystem headroom separate from Aether's
// de-duplicated file sizes. SnapshotBytes is a subset of WorktreeBytes.
type ServerDiskResult struct {
	UsedBytes       uint64            `json:"used_bytes"`
	TotalBytes      uint64            `json:"total_bytes"`
	FreeBytes       uint64            `json:"free_bytes"`
	WorktreeBytes   uint64            `json:"worktree_bytes"`
	TranscriptBytes uint64            `json:"transcript_bytes"`
	DatabaseBytes   uint64            `json:"database_bytes"`
	RepoBytes       uint64            `json:"repo_bytes"`
	HomeBytes       uint64            `json:"home_bytes"`
	CacheBytes      uint64            `json:"cache_bytes"`
	EvidenceBytes   uint64            `json:"evidence_bytes"`
	ScratchBytes    uint64            `json:"scratch_bytes"`
	OtherBytes      uint64            `json:"other_bytes"`
	SnapshotBytes   uint64            `json:"snapshot_bytes"`
	Warnings        []string          `json:"warnings,omitempty"`
	Docker          *ServerDockerDisk `json:"docker,omitempty"`
	Entries         []ServerDiskEntry `json:"entries,omitempty"`
	Truncated       bool              `json:"truncated,omitempty"`
	// Containers lists every Aether container for an admin, and the caller's
	// own runs and environment otherwise. The bytes are outside the data
	// directory and are not part of any total above.
	Containers []ServerDiskContainer `json:"containers,omitempty"`
	// ContainersMeasuredAt is empty until the first measurement completes.
	ContainersMeasuredAt string `json:"containers_measured_at,omitempty"`
	ContainersError      string `json:"containers_error,omitempty"`
}

// ServerDiskContainer is the files a container wrote outside its mounted
// home, checkout and cache, such as /tmp. They are deleted with the container.
// OwnerKind is "run", or "member" for a member's environment.
type ServerDiskContainer struct {
	OwnerKind string `json:"owner_kind"`
	OwnerID   string `json:"owner_id"`
	Bytes     uint64 `json:"bytes"`
}

// ServerDockerDisk is daemon-wide. Nil numbers are unknown, not zero.
// Its filesystem totals must never be added to ServerDiskResult totals.
type ServerDockerDisk struct {
	UsedBytes        *uint64 `json:"used_bytes,omitempty"`
	TotalBytes       *uint64 `json:"total_bytes,omitempty"`
	FreeBytes        *uint64 `json:"free_bytes,omitempty"`
	ImagesBytes      *uint64 `json:"images_bytes,omitempty"`
	ContainersBytes  *uint64 `json:"containers_bytes,omitempty"`
	VolumesBytes     *uint64 `json:"volumes_bytes,omitempty"`
	BuildCacheBytes  *uint64 `json:"build_cache_bytes,omitempty"`
	ReclaimableBytes *uint64 `json:"reclaimable_bytes,omitempty"`
	SharedFilesystem *bool   `json:"shared_filesystem,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// ServerDiskEntry is admin-only attribution, overlapping component totals.
type ServerDiskEntry struct {
	Kind             string  `json:"kind"`
	OwnerKind        string  `json:"owner_kind"`
	OwnerID          string  `json:"owner_id,omitempty"`
	Pool             string  `json:"pool,omitempty"`
	Bytes            uint64  `json:"bytes"`
	ReclaimableBytes *uint64 `json:"reclaimable_bytes,omitempty"`
	RetainedUntil    string  `json:"retained_until,omitempty"`
	Reason           string  `json:"reason"`
	Error            string  `json:"error,omitempty"`
}

// SystemStatsParams asks for host samples taken after SinceMS (Unix
// milliseconds); 0 returns the whole retained window.
type SystemStatsParams struct {
	SinceMS int64 `json:"since_ms,omitempty"`
}

// SystemStatsResult is the host's recent history and the latest container
// snapshot. Pointer fields are omitted when unknown, never reported as zero.
type SystemStatsResult struct {
	IntervalMS      int64                  `json:"interval_ms"`
	RetentionMS     int64                  `json:"retention_ms"`
	Host            SystemHost             `json:"host"`
	HostError       string                 `json:"host_error"`
	Samples         []SystemHostSample     `json:"samples"`
	ContainersAtMS  int64                  `json:"containers_at_ms"`
	ContainersError string                 `json:"containers_error"`
	Containers      []SystemContainer      `json:"containers"`
	Others          *SystemOtherContainers `json:"others,omitempty"`
}

type SystemHost struct {
	CPUCores         int     `json:"cpu_cores"`
	MemoryTotalBytes uint64  `json:"memory_total_bytes"`
	SwapTotalBytes   uint64  `json:"swap_total_bytes"`
	UptimeSeconds    float64 `json:"uptime_seconds"`
}

// SystemHostSample is one interval. CPU fields are percent of all cores.
type SystemHostSample struct {
	TMS                  int64    `json:"t_ms"`
	CPUUser              float64  `json:"cpu_user"`
	CPUSystem            float64  `json:"cpu_system"`
	CPUIOWait            float64  `json:"cpu_iowait"`
	CPUSteal             float64  `json:"cpu_steal"`
	MemoryUsedBytes      uint64   `json:"memory_used_bytes"`
	MemoryAvailableBytes uint64   `json:"memory_available_bytes"`
	MemoryFreeBytes      uint64   `json:"memory_free_bytes"`
	SwapUsedBytes        uint64   `json:"swap_used_bytes"`
	Load1                float64  `json:"load1"`
	Load5                float64  `json:"load5"`
	Load15               float64  `json:"load15"`
	NetRxBps             float64  `json:"net_rx_bps"`
	NetTxBps             float64  `json:"net_tx_bps"`
	DiskReadBps          float64  `json:"disk_read_bps"`
	DiskWriteBps         float64  `json:"disk_write_bps"`
	PressureCPU          *float64 `json:"pressure_cpu,omitempty"`
	PressureMemory       *float64 `json:"pressure_memory,omitempty"`
	PressureIO           *float64 `json:"pressure_io,omitempty"`
}

// SystemContainer is a running container the caller may see. OwnerKind is
// "run", "browser" (a run's browser sidecar; OwnerID is the run) or "member"
// (a member's environment). Rate fields are omitted until the container has
// two samples; CPULimitCores is omitted when unlimited.
type SystemContainer struct {
	OwnerKind           string   `json:"owner_kind"`
	OwnerID             string   `json:"owner_id"`
	MemberID            string   `json:"member_id"`
	WorkspaceID         string   `json:"workspace_id,omitempty"`
	Title               string   `json:"title,omitempty"`
	Status              string   `json:"status,omitempty"`
	CPUCores            *float64 `json:"cpu_cores,omitempty"`
	CPULimitCores       float64  `json:"cpu_limit_cores,omitempty"`
	CPUThrottledPercent *float64 `json:"cpu_throttled_percent,omitempty"`
	MemoryBytes         uint64   `json:"memory_bytes"`
	MemoryLimitBytes    uint64   `json:"memory_limit_bytes"`
	Pids                uint64   `json:"pids"`
	PidsLimit           uint64   `json:"pids_limit"`
	NetRxBps            *float64 `json:"net_rx_bps,omitempty"`
	NetTxBps            *float64 `json:"net_tx_bps,omitempty"`
	DiskReadBps         *float64 `json:"disk_read_bps,omitempty"`
	DiskWriteBps        *float64 `json:"disk_write_bps,omitempty"`
}

// SystemOtherContainers sums the containers the caller may not see and the
// Aether containers with no run or member owner.
type SystemOtherContainers struct {
	Containers  int     `json:"containers"`
	CPUCores    float64 `json:"cpu_cores"`
	MemoryBytes uint64  `json:"memory_bytes"`
}

// GatewayCapabilities describes what one gateway deployment can do, so a
// client probes instead of hard-coding which surface it is talking to.
type GatewayCapabilities struct {
	// Gateway names the serving transport (e.g. "remote").
	Gateway string `json:"gateway"`
	// Methods is the sorted list of control-channel methods this gateway
	// forwards.
	Methods []string `json:"methods"`
	// WS lists the WebSocket surfaces (e.g. "events", "attach").
	WS []string `json:"ws"`
	// Local lists verbs only a local gateway offers, absent remotely.
	Local []string `json:"local,omitempty"`
	// Version is the CLI build serving this gateway ("dev" for a local
	// build), so a client can tell a stale shell from a stale gateway.
	Version string `json:"version,omitempty"`
	// Commit is that build's short git commit.
	Commit string `json:"commit,omitempty"`
}
