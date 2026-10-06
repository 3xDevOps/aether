package protocol

import "encoding/json"

const (
	// SubsystemACP streams an enhanced run's session item log.
	SubsystemACP = "aether-acp"

	// ACPWireItemBytes caps one streamed item; a larger item is sent cut
	// down with Truncated set, and run.acp.item returns it whole.
	ACPWireItemBytes = 32 << 10
)

// ACPStreamRequest is the aether-acp header. Write asks for the run's control
// lease with the same fields an AttachRequest uses; without it the stream is
// read-only.
type ACPStreamRequest struct {
	RunID             string `json:"run_id"`
	AfterSeq          int64  `json:"after_seq"`
	Write             bool   `json:"write,omitempty"`
	ControlSessionID  string `json:"control_session_id,omitempty"`
	ControlGeneration uint64 `json:"control_generation,omitempty"`
	Takeover          bool   `json:"takeover,omitempty"`
	ReleaseControl    bool   `json:"release_control,omitempty"`
}

// ACPStreamResponse is the aether-acp ack. Seq is the high-water mark the
// client holds once the Replay frames that follow are applied.
type ACPStreamResponse struct {
	OK                bool            `json:"ok"`
	Seq               int64           `json:"seq"`
	Replay            int             `json:"replay"`
	Epoch             int64           `json:"epoch"`
	Live              bool            `json:"live"`
	State             json.RawMessage `json:"state,omitempty"`
	HasControl        bool            `json:"has_control"`
	ControlSessionID  string          `json:"control_session_id,omitempty"`
	ControlGeneration uint64          `json:"control_generation,omitempty"`
	Code              int             `json:"code,omitempty"`
	Error             string          `json:"error,omitempty"`
}

// ACPFrame is one aether-acp stream line: an item, or a reset after which the
// client drops what it holds and applies the items that follow.
type ACPFrame struct {
	Seq       int64           `json:"seq,omitempty"`
	Item      json.RawMessage `json:"item,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	Reset     bool            `json:"reset,omitempty"`
	Epoch     int64           `json:"epoch,omitempty"`
}
