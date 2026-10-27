package protocol

// MethodServerRunLimits reads the server-wide run limits, or with any field
// set changes them; changing them is admin only. A limit applies to runs
// launched afterwards, and a run's own RunLaunchParams.Limits overrides it.
const MethodServerRunLimits = "server.run_limits"

// RunLimits caps a run's container. Zero is unset: a per-run field follows
// the server limit, and a server field follows the server's automatic value.
type RunLimits struct {
	CPUs        float64 `json:"cpus,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	Pids        int64   `json:"pids,omitempty"`
}

// ServerRunLimitsParams: a nil field keeps its value.
type ServerRunLimitsParams struct {
	CPUs        *float64 `json:"cpus,omitempty"`
	MemoryBytes *int64   `json:"memory_bytes,omitempty"`
	Pids        *int64   `json:"pids,omitempty"`
}

// ServerRunLimits is the result of MethodServerRunLimits. Default is what a
// field gets when Limits leaves it unset: the server's flag or automatic
// value. Effective is what a run with no override gets, Limits over Default.
type ServerRunLimits struct {
	Limits    RunLimits `json:"limits"`
	Default   RunLimits `json:"default"`
	Effective RunLimits `json:"effective"`
}
