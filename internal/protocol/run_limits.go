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

// ServerRunLimits is the result of MethodServerRunLimits. Effective is what
// a run with no override gets: the server limits with the automatic values
// filling the unset ones.
type ServerRunLimits struct {
	Limits    RunLimits `json:"limits"`
	Effective RunLimits `json:"effective"`
}
