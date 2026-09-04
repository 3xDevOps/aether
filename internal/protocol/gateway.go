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
	// Truncated reports that the diff outgrew the byte limit and Patch
	// ends early, at the last whole line that fit.
	Truncated bool `json:"truncated"`
}

// ServerDiskResult is the reply to server.disk: the headroom on the
// filesystem holding the server's data directory, plus the directories
// that grow without bound.
type ServerDiskResult struct {
	UsedBytes       uint64 `json:"used_bytes"`
	TotalBytes      uint64 `json:"total_bytes"`
	FreeBytes       uint64 `json:"free_bytes"`
	WorktreeBytes   uint64 `json:"worktree_bytes"`
	TranscriptBytes uint64 `json:"transcript_bytes"`
	DatabaseBytes   uint64 `json:"database_bytes"`
	RepoBytes       uint64 `json:"repo_bytes"`
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
