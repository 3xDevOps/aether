// Package browser owns one run's private Chromium companion. Authorization and
// controller fencing belong to the caller; no request can choose a run identity.
package browser

import "time"

const (
	MaxRequestBytes = 2 << 20
	MaxImageBytes = 8 << 20
	MaxFrameBytes = 2 << 20
	MaxMetadataBytes = 16 << 10
)

// Request is the narrow companion protocol, not a public gateway request.
// Existing pages require all three target fields, even for read operations.
// Coordinate input additionally requires the viewport identity of its frame.
type Request struct {
	Operation string `json:"operation"`
	SessionID string `json:"session_id,omitempty"`
	PageID string `json:"page_id,omitempty"`
	PageRevision uint64 `json:"page_revision,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	ViewportID string `json:"viewport_id,omitempty"`
	URL string `json:"url,omitempty"`
	Text string `json:"text,omitempty"`
	Key string `json:"key,omitempty"`
	Values []string `json:"values,omitempty"`
	Action string `json:"action,omitempty"`
	Button string `json:"button,omitempty"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	DeltaX float64 `json:"delta_x"`
	DeltaY float64 `json:"delta_y"`
	ClickCount int `json:"click_count,omitempty"`
	TouchID int `json:"touch_id,omitempty"`
	Width int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	TimeoutMS int `json:"timeout_ms,omitempty"`
	Condition string `json:"condition,omitempty"`
	MaxNodes int `json:"max_nodes,omitempty"`
	MaxChars int `json:"max_chars,omitempty"`
	After uint64 `json:"after,omitempty"`
}

type Page struct {
	SessionID string `json:"session_id"`
	PageID string `json:"page_id"`
	PageRevision uint64 `json:"page_revision"`
	ViewportID string `json:"viewport_id"`
	URL string `json:"url"`
	Title string `json:"title"`
	Width int `json:"width"`
	Height int `json:"height"`
}

type Node struct {
	NodeID string `json:"node_id"`
	FrameURL string `json:"frame_url,omitempty"`
	Tag string `json:"tag,omitempty"`
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
	Value string `json:"value,omitempty"`
	Disabled bool `json:"disabled,omitempty"`
	Checked *bool `json:"checked,omitempty"`
	Expanded *string `json:"expanded,omitempty"`
}

type Snapshot struct {
	Nodes []Node `json:"nodes"`
	Truncated bool `json:"truncated"`
}

type Log struct {
	Sequence uint64 `json:"sequence"`
	CapturedAt time.Time `json:"captured_at"`
	Level string `json:"level,omitempty"`
	Text string `json:"text,omitempty"`
	URL string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
	Status int `json:"status,omitempty"`
}

type Result struct {
	SessionID string `json:"session_id,omitempty"`
	SelectedPageID string `json:"selected_page_id,omitempty"`
	Pages []Page `json:"pages,omitempty"`
	Page *Page `json:"page,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Logs []Log `json:"logs,omitempty"`
	LatestSequence uint64 `json:"latest_sequence,omitempty"`
	Matched bool `json:"matched,omitempty"`
	TimedOut bool `json:"timed_out,omitempty"`
}

type Metadata struct {
	Page
	ContentType string `json:"content_type"`
	CapturedAt time.Time `json:"captured_at"`
	Sequence uint64 `json:"sequence,omitempty"`
	ScreenRevision uint64 `json:"screen_revision,omitempty"`
	OutputPosition uint64 `json:"output_position,omitempty"`
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
	ActiveBuffer string `json:"active_buffer,omitempty"`
	OffsetTop float64 `json:"offset_top,omitempty"`
	PageScaleFactor float64 `json:"page_scale_factor,omitempty"`
	ScrollX float64 `json:"scroll_x,omitempty"`
	ScrollY float64 `json:"scroll_y,omitempty"`
}

type Capture struct {
	Bytes []byte
	Metadata Metadata
}

type TerminalSnapshot struct {
	SessionID string `json:"session_id"`
	ScreenRevision uint64 `json:"screen_revision"`
	OutputPosition uint64 `json:"output_position"`
	CapturedAt time.Time `json:"captured_at"`
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	FontSize int `json:"font_size,omitempty"`
	VT string `json:"vt"`
}

type Health struct {
	CreationKey string `json:"creation_key"`
	SessionID string `json:"session_id"`
	ProtocolVersion int `json:"protocol_version"`
}

// Error preserves the companion's actionable failure (including sandbox errors).
type Error struct {
	Code string `json:"code"`
	Message string `json:"message"`
}
func (e *Error) Error() string { return "browser: " + e.Code + ": " + e.Message }
