package protocol

// Attach transport types for the local gateway (internal/localgw). The
// gateway's HTTP/WS surface has no login of its own: it is spawned by
// `aether gui`, which holds the SSH identity and hands the browser a
// single-process bearer token, so the SSH key stays the only identity
// system.

// DashAttachRequest is the header frame of /ws/attach/{run}; the run
// comes from the path. Write access is opt-in - the zero value is the
// read-only mirror the dashboard defaults to - and is refused unless the
// member holds the steer capability on that run. The ack is the shared
// AttachResponse.
type DashAttachRequest struct {
	Write bool `json:"write,omitempty"`
	Cols  uint `json:"cols,omitempty"`
	Rows  uint `json:"rows,omitempty"`
}

// Control frame kinds a client sends on /ws/attach/{run}.
const (
	DashAttachInput  = "input"
	DashAttachResize = "resize"
)

// DashAttachControl is one client control frame on /ws/attach/{run}:
// {"type":"input","data":"ls\r"} or {"type":"resize","cols":120,"rows":40}.
// Terminal output travels the other way as binary frames.
type DashAttachControl struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint   `json:"cols,omitempty"`
	Rows uint   `json:"rows,omitempty"`
}
