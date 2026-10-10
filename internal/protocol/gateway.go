package protocol

// Gateway-facing methods: reads the dashboard renders that have no SSH
// client of their own. They ride the same control channel and error
// contract as every other method.
const (
	// MethodRunPatch renders a run checkout's diff against its fork point.
	MethodRunPatch = "run.patch"
	// MethodServerDisk reads the data directory's disk usage.
	MethodServerDisk = "server.disk"
)

// RunPatchParams asks for one run's patch. From and To are snapshot
// trees from run.diff events; both empty asks for the run's current
// diff against its fork point.
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
	// Base is what the diff is taken against: the fork-point commit for a
	// cumulative render, the requested from tree for an interval render.
	Base string `json:"base"`
	// Patch is the unified diff text, empty when nothing changed.
	Patch string `json:"patch"`
	// Truncated reports that an explicitly bounded render ended early, at the
	// last whole line that fit. Dashboard renders are complete.
	Truncated bool `json:"truncated"`
	Recorded  bool `json:"recorded,omitempty"`
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
