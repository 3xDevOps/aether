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
	Write       bool `json:"write,omitempty"`
	Interactive bool `json:"interactive,omitempty"`
	Screen      bool `json:"screen,omitempty"`
	Cols        uint `json:"cols,omitempty"`
	Rows        uint `json:"rows,omitempty"`
	// Follow is AttachRequest.Follow: render at the session's geometry and
	// impose none, so this client is left out of the minimum the PTY is
	// sized to. The ack reports the size to draw at, and a geometry frame
	// reports every later change.
	Follow bool `json:"follow,omitempty"`
	// Resume is AttachRequest.Resume: reattach without the scrollback
	// replay, keeping the screen this client already has.
	Resume bool `json:"resume,omitempty"`
	// Cursor is AttachRequest.Cursor: how much of the output this client
	// already holds, so a resume replays only what it missed.
	Cursor uint64 `json:"cursor,omitempty"`
	// ResumeID identifies the PTY process incarnation that produced Cursor.
	ResumeID string `json:"resume_id,omitempty"`
	// ControlSessionID is stable for one browser terminal tab across
	// reconnects and distinct for two tabs by the same member.
	ControlSessionID  string `json:"control_session_id,omitempty"`
	ControlGeneration uint64 `json:"control_generation,omitempty"`
	Takeover          bool   `json:"takeover,omitempty"`
	ReleaseControl    bool   `json:"release_control,omitempty"`
}

// Control frame kinds on /ws/attach/{run}. Input and resize travel from
// the client; geometry and control acknowledgments travel the other way.
const (
	DashAttachInput        = "input"
	DashAttachResize       = "resize"
	DashAttachControlFrame = "control"
	// DashAttachGeometry reports the shared PTY grid before the output drawn
	// at that size. Every dashboard viewer, including writers, adopts it.
	DashAttachGeometry = "geometry"
)

// DashAttachControl is one control frame on /ws/attach/{run}:
// {"type":"input","data":"ls\r","control_generation":3} or
// {"type":"control","request_id":4,"write":true,"takeover":true}
// from the client, {"type":"geometry","cols":132,"rows":43} from the
// server. Terminal output travels as binary frames.
type DashAttachControl struct {
	Type              string `json:"type"`
	Data              string `json:"data,omitempty"`
	Cols              uint   `json:"cols,omitempty"`
	Rows              uint   `json:"rows,omitempty"`
	RequestID         uint64 `json:"request_id,omitempty"`
	Write             bool   `json:"write,omitempty"`
	Takeover          bool   `json:"takeover,omitempty"`
	ControlGeneration uint64 `json:"control_generation,omitempty"`
	OK                bool   `json:"ok,omitempty"`
	Code              int    `json:"code,omitempty"`
	Error             string `json:"error,omitempty"`
	HasControl        bool   `json:"has_control,omitempty"`
	ControlSessionID  string `json:"control_session_id,omitempty"`
}
