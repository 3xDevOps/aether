package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	MethodDevTerminalList = "dev.terminal.list"
	MethodDevTerminalStart = "dev.terminal.start"
	MethodDevTerminalOutput = "dev.terminal.output"
	MethodDevTerminalScreen = "dev.terminal.screen"
	MethodDevTerminalScreenshot = "dev.terminal.screenshot"
	MethodDevTerminalInput = "dev.terminal.input"
	MethodDevTerminalResize = "dev.terminal.resize"
	MethodDevTerminalWait = "dev.terminal.wait"
	MethodDevTerminalStop = "dev.terminal.stop"
	MethodDevBrowserStatus = "dev.browser.status"
	MethodDevBrowserOpen = "dev.browser.open"
	MethodDevBrowserPages = "dev.browser.pages"
	MethodDevBrowserNavigate = "dev.browser.navigate"
	MethodDevBrowserSnapshot = "dev.browser.snapshot"
	MethodDevBrowserAction = "dev.browser.action"
	MethodDevBrowserScreenshot = "dev.browser.screenshot"
	MethodDevBrowserViewport = "dev.browser.viewport"
	MethodDevBrowserWait = "dev.browser.wait"
	MethodDevBrowserConsole = "dev.browser.console"
	MethodDevBrowserNetwork = "dev.browser.network"
	MethodDevBrowserReset = "dev.browser.reset"
	MethodDevBrowserClose = "dev.browser.close"
	MethodDevControlStatus = "dev.control.status"
	MethodDevControlAcquire = "dev.control.acquire"
	MethodDevControlRelease = "dev.control.release"
	MethodDevArtifactList = "dev.artifact.list"
	MethodDevArtifactGet = "dev.artifact.get"
	MethodDevArtifactDelete = "dev.artifact.delete"

	MaxDevParamsBytes = 48 << 10
	MaxDevResultBytes = 48 << 10
	MaxDevOutputBytes = 8 << 10
	MaxDevInputBytes = 8 << 10
	MaxDevWaitMS = 30000
	MaxDevScreenCells = 256
	MaxDevSnapshotNodes = 128
	MaxDevSnapshotChars = 8 << 10
	MaxDevLogEntries = 100
	MaxDevArtifactPage = 100
	MaxDevTerminalDimension = 500
	MaxDevViewportDimension = 4096
)

// DecodeDevAgentParams refuses a caller-supplied run identity, including an
// empty or null run_id. Only the authenticated socket supplies that identity.
// Human dispatch uses ordinary strict decoding and authorizes DevRunParams.
func DecodeDevAgentParams(raw json.RawMessage, params any) error {
	if len(raw) == 0 { raw = json.RawMessage(`{}`) }
	if len(raw) > MaxDevParamsBytes { return fmt.Errorf("development request exceeds %d bytes", MaxDevParamsBytes) }
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil { return err }
	if fields == nil { return fmt.Errorf("development parameters must be an object") }
	for key := range fields {
		// encoding/json matches exported field names case-insensitively.
		if bytes.EqualFold([]byte(key), []byte("run_id")) { return fmt.Errorf("run_id is supplied by the authenticated run socket") }
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(params); err != nil { return err }
	if err := decoder.Decode(new(any)); err != io.EOF { return fmt.Errorf("development parameters contain trailing data") }
	return nil
}

// MarshalDevResult enforces the run socket's response budget before an
// envelope is added. A broker must truncate/page observations explicitly;
// exceeding this limit is never silently reported as a complete observation.
func MarshalDevResult(result any) (json.RawMessage, error) {
	data, err := json.Marshal(result)
	if err != nil { return nil, err }
	if len(data) > MaxDevResultBytes { return nil, fmt.Errorf("development result exceeds %d bytes; request a smaller observation", MaxDevResultBytes) }
	return data, nil
}

// DevRunParams.RunID is accepted only at authenticated human entry points.
// It is never an agent-selectable target or authentication credential.
type DevRunParams struct { RunID string `json:"run_id,omitempty"` }

type DevCapability struct {
	Available bool `json:"available"`
	Reason string `json:"reason,omitempty"`
}

type DevControlFence struct {
	ControlSessionID string `json:"control_session_id"`
	ControlGeneration uint64 `json:"control_generation"`
}

type DevSurface struct {
	Kind string `json:"kind"` // terminal or browser; never primary harness
	ID string `json:"id"`
	Incarnation string `json:"incarnation"`
}

type DevControlStatusParams struct {
	DevRunParams
	Surface DevSurface `json:"surface"`
}

type DevController struct {
	Kind string `json:"kind"` // member or run_agent
	MemberID string `json:"member_id,omitempty"`
	RunID string `json:"run_id,omitempty"`
	DevControlFence
	Connected bool `json:"connected"`
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type DevControlStatusResult struct {
	Surface DevSurface `json:"surface"`
	Controller *DevController `json:"controller"`
}

type DevControlAcquireParams struct {
	DevControlStatusParams
	ControlSessionID string `json:"control_session_id"`
	ExpectedGeneration uint64 `json:"expected_generation,omitempty"`
	Takeover bool `json:"takeover,omitempty"`
}

type DevControlAcquireResult struct {
	DevControlStatusResult
	Displaced *DevController `json:"displaced,omitempty"`
}

type DevControlReleaseParams struct {
	DevControlStatusParams
	DevControlFence
}

type DevControlReleaseResult struct { Released bool `json:"released"` }

type DevOutputCursor struct {
	Epoch string `json:"epoch"`
	Sequence uint64 `json:"sequence"`
}

type DevTerminalTarget struct {
	DevRunParams
	TerminalID string `json:"terminal_id"`
	Incarnation string `json:"incarnation"`
}

type DevProcessState struct {
	State string `json:"state"` // running, exited, stopped, unavailable
	ExitCode *int `json:"exit_code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type DevTerminal struct {
	TerminalID string `json:"terminal_id"`
	Incarnation string `json:"incarnation"`
	Name string `json:"name"`
	Cols uint `json:"cols"`
	Rows uint `json:"rows"`
	Process DevProcessState `json:"process"`
}

type DevTerminalListParams struct { DevRunParams }
type DevTerminalListResult struct { Terminals []DevTerminal `json:"terminals"` }

type DevTerminalStartParams struct {
	DevRunParams
	Name string `json:"name,omitempty"`
	Command []string `json:"command,omitempty"` // empty starts the account shell; otherwise argv
	Cols uint `json:"cols,omitempty"`
	Rows uint `json:"rows,omitempty"`
}

type DevTerminalStartResult struct { Terminal DevTerminal `json:"terminal"` }

type DevTerminalOutputParams struct {
	DevTerminalTarget
	After *DevOutputCursor `json:"after,omitempty"`
	MaxBytes int `json:"max_bytes,omitempty"`
	Format string `json:"format,omitempty"` // text or raw; raw bytes are bounded base64
}

type DevTerminalOutputResult struct {
	Terminal DevTerminal `json:"terminal"`
	Start DevOutputCursor `json:"start"`
	Next DevOutputCursor `json:"next"`
	Position DevOutputCursor `json:"position"`
	Text string `json:"text,omitempty"`
	Data []byte `json:"data,omitempty"`
	MissingCursor bool `json:"missing_cursor"`
	Truncated bool `json:"truncated"`
	More bool `json:"more"`
}

type DevTerminalScreenParams struct {
	DevTerminalTarget
	RowOffset int `json:"row_offset,omitempty"`
	MaxCells int `json:"max_cells,omitempty"`
}

type DevTerminalCell struct {
	Text string `json:"text"`
	Width int `json:"width"`
	Foreground string `json:"foreground,omitempty"`
	Background string `json:"background,omitempty"`
	Bold bool `json:"bold,omitempty"`
	Dim bool `json:"dim,omitempty"`
	Italic bool `json:"italic,omitempty"`
	Underline bool `json:"underline,omitempty"`
	Blink bool `json:"blink,omitempty"`
	Inverse bool `json:"inverse,omitempty"`
	Hidden bool `json:"hidden,omitempty"`
	Strikethrough bool `json:"strikethrough,omitempty"`
}

type DevTerminalRow struct {
	Text string `json:"text"`
	Wrapped bool `json:"wrapped"`
	Cells []DevTerminalCell `json:"cells"`
}

type DevTerminalCursor struct {
	X int `json:"x"`
	Y int `json:"y"`
	Visible bool `json:"visible"`
}

type DevTerminalScreenResult struct {
	Terminal DevTerminal `json:"terminal"`
	Position DevOutputCursor `json:"position"`
	ScreenRevision uint64 `json:"screen_revision"`
	GeometryRevision uint64 `json:"geometry_revision"`
	Alternate bool `json:"alternate"`
	Cursor DevTerminalCursor `json:"cursor"`
	Text string `json:"text"`
	Lines []DevTerminalRow `json:"lines"`
	RowOffset int `json:"row_offset"`
	NextRow *int `json:"next_row,omitempty"`
	Truncated bool `json:"truncated"`
	UnsupportedGraphics []string `json:"unsupported_graphics,omitempty"`
}

type DevTerminalScreenshotParams struct { DevTerminalTarget }
type DevTerminalScreenshotResult struct { Artifact DevArtifact `json:"artifact"` }

type DevTerminalMouse struct {
	Action string `json:"action"` // press, release, move, wheel
	Button string `json:"button,omitempty"`
	X int `json:"x"`
	Y int `json:"y"`
	Delta int `json:"delta,omitempty"`
}

type DevTerminalInputParams struct {
	DevTerminalTarget
	DevControlFence
	Kind string `json:"kind"` // text, paste, key, mouse
	Text string `json:"text,omitempty"`
	Key string `json:"key,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
	Mouse *DevTerminalMouse `json:"mouse,omitempty"`
}

type DevTerminalInputResult struct { Accepted bool `json:"accepted"` }

type DevTerminalResizeParams struct {
	DevTerminalTarget
	DevControlFence
	Cols uint `json:"cols"`
	Rows uint `json:"rows"`
}

type DevTerminalResizeResult struct {
	Terminal DevTerminal `json:"terminal"`
	ScreenRevision uint64 `json:"screen_revision"`
	GeometryRevision uint64 `json:"geometry_revision"`
}

type DevTerminalWaitParams struct {
	DevTerminalTarget
	AfterOutput *DevOutputCursor `json:"after_output,omitempty"`
	AfterScreenRevision uint64 `json:"after_screen_revision,omitempty"`
	Contains string `json:"contains,omitempty"`
	Exit bool `json:"exit,omitempty"`
	TimeoutMS int `json:"timeout_ms"`
}

type DevTerminalWaitResult struct {
	Terminal DevTerminal `json:"terminal"`
	Position DevOutputCursor `json:"position"`
	ScreenRevision uint64 `json:"screen_revision"`
	GeometryRevision uint64 `json:"geometry_revision"`
	Matched bool `json:"matched"`
	TimedOut bool `json:"timed_out"`
	MissingCursor bool `json:"missing_cursor"`
	Truncated bool `json:"truncated"`
}

type DevTerminalStopParams struct {
	DevTerminalTarget
	DevControlFence
	TimeoutMS int `json:"timeout_ms"`
}

type DevTerminalStopResult struct {
	Terminal DevTerminal `json:"terminal"`
	Stopped bool `json:"stopped"`
	TimedOut bool `json:"timed_out"`
}

// SessionID is the browser incarnation, NOT the controller's session ID.
// PageRevision fences navigation and node replacement; ViewportID fences
// coordinate input. None of these is the control generation.
type DevBrowserTarget struct {
	DevRunParams
	SessionID string `json:"session_id"`
}

type DevBrowserPageTarget struct {
	DevBrowserTarget
	PageID string `json:"page_id"`
	PageRevision uint64 `json:"page_revision"`
}

type DevBrowserPage struct {
	SessionID string `json:"session_id"`
	PageID string `json:"page_id"`
	PageRevision uint64 `json:"page_revision"`
	ViewportID string `json:"viewport_id"`
	URL string `json:"url"`
	Title string `json:"title"`
	Width int `json:"width"`
	Height int `json:"height"`
}

type DevBrowserStatusParams struct { DevRunParams }
type DevBrowserStatusResult struct {
	DevCapability
	Running bool `json:"running"`
	SessionID string `json:"session_id,omitempty"`
	SelectedPageID string `json:"selected_page_id,omitempty"`
}

type DevBrowserOpenParams struct {
	DevRunParams
	DevControlFence
	SessionID string `json:"session_id,omitempty"` // empty only for first launch
	URL string `json:"url"`
	Width int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

type DevBrowserOpenResult struct { Page DevBrowserPage `json:"page"` }
type DevBrowserPagesParams struct { DevBrowserTarget }
type DevBrowserPagesResult struct {
	Pages []DevBrowserPage `json:"pages"`
	SelectedPageID string `json:"selected_page_id,omitempty"`
}

type DevBrowserNavigateParams struct {
	DevBrowserPageTarget
	DevControlFence
	URL string `json:"url,omitempty"`
	Direction string `json:"direction,omitempty"` // url (default), back, forward, reload
	TimeoutMS int `json:"timeout_ms"`
}

type DevBrowserNavigateResult struct { Page DevBrowserPage `json:"page"` }

type DevBrowserSnapshotParams struct {
	DevBrowserPageTarget
	MaxNodes int `json:"max_nodes,omitempty"`
	MaxChars int `json:"max_chars,omitempty"`
}

type DevBrowserNode struct {
	NodeID string `json:"node_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
	Value string `json:"value,omitempty"`
	Disabled bool `json:"disabled,omitempty"`
	Checked *bool `json:"checked,omitempty"`
	Selected *bool `json:"selected,omitempty"`
}

type DevBrowserSnapshotResult struct {
	Page DevBrowserPage `json:"page"`
	Nodes []DevBrowserNode `json:"nodes"`
	Truncated bool `json:"truncated"`
}

type DevBrowserActionParams struct {
	DevBrowserPageTarget
	DevControlFence
	Action string `json:"action"` // click, fill, select_option, key, scroll, text, pointer, touch, select
	NodeID string `json:"node_id,omitempty"`
	ViewportID string `json:"viewport_id,omitempty"`
	Text string `json:"text,omitempty"`
	Key string `json:"key,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
	Values []string `json:"values,omitempty"`
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
	DeltaX float64 `json:"delta_x,omitempty"`
	DeltaY float64 `json:"delta_y,omitempty"`
	Button string `json:"button,omitempty"`
	Phase string `json:"phase,omitempty"` // down, move, up, cancel
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

type DevBrowserActionResult struct { Page DevBrowserPage `json:"page"` }
type DevBrowserScreenshotParams struct {
	DevBrowserPageTarget
	FullPage bool `json:"full_page,omitempty"`
}

type DevBrowserScreenshotResult struct { Artifact DevArtifact `json:"artifact"` }
type DevBrowserViewportParams struct {
	DevBrowserPageTarget
	DevControlFence
	Width int `json:"width"`
	Height int `json:"height"`
}

type DevBrowserViewportResult struct { Page DevBrowserPage `json:"page"` }
type DevBrowserWaitParams struct {
	DevBrowserPageTarget
	Condition string `json:"condition"` // text, visible, hidden, url, load
	NodeID string `json:"node_id,omitempty"`
	Text string `json:"text,omitempty"`
	TimeoutMS int `json:"timeout_ms"`
}

type DevBrowserWaitResult struct {
	Page DevBrowserPage `json:"page"`
	Matched bool `json:"matched"`
	TimedOut bool `json:"timed_out"`
}

type DevBrowserConsoleParams struct {
	DevBrowserPageTarget
	After uint64 `json:"after,omitempty"`
	Limit int `json:"limit,omitempty"`
}

type DevBrowserConsoleEntry struct {
	Sequence uint64 `json:"sequence"`
	Time string `json:"time"`
	Level string `json:"level"`
	Text string `json:"text"`
	URL string `json:"url,omitempty"`
}

type DevBrowserConsoleResult struct {
	Entries []DevBrowserConsoleEntry `json:"entries"`
	Next uint64 `json:"next"`
	MissingCursor bool `json:"missing_cursor"`
	Truncated bool `json:"truncated"`
}

type DevBrowserNetworkParams struct {
	DevBrowserPageTarget
	After uint64 `json:"after,omitempty"`
	Limit int `json:"limit,omitempty"`
}

type DevBrowserNetworkEntry struct {
	Sequence uint64 `json:"sequence"`
	Time string `json:"time"`
	URL string `json:"url"`
	Method string `json:"method"`
	Status int `json:"status,omitempty"`
	Failure string `json:"failure,omitempty"`
}

type DevBrowserNetworkResult struct {
	Entries []DevBrowserNetworkEntry `json:"entries"`
	Next uint64 `json:"next"`
	MissingCursor bool `json:"missing_cursor"`
	Truncated bool `json:"truncated"`
}

type DevBrowserResetParams struct {
	DevBrowserTarget
	DevControlFence
}

type DevBrowserResetResult struct { SessionID string `json:"session_id"` }
type DevBrowserCloseParams struct {
	DevBrowserPageTarget
	DevControlFence
}

type DevBrowserCloseResult struct { Closed bool `json:"closed"` }

// Captures are handles, never image bytes or caller-selected host paths. Path
// is assigned by the artifact store inside the run's read-only capture mount.
// GitHead/Dirty are optional: absence means that boundary was not observed.
type DevArtifact struct {
	ID string `json:"id"`
	Path string `json:"path"`
	Source string `json:"source"`
	RunID string `json:"run_id"`
	Incarnation string `json:"incarnation"`
	TerminalID string `json:"terminal_id,omitempty"`
	PageID string `json:"page_id,omitempty"`
	PageRevision uint64 `json:"page_revision,omitempty"`
	ScreenRevision uint64 `json:"screen_revision,omitempty"`
	GeometryRevision uint64 `json:"geometry_revision,omitempty"`
	ViewportID string `json:"viewport_id,omitempty"`
	URL string `json:"url,omitempty"`
	CapturedAt string `json:"captured_at"`
	ContentType string `json:"content_type"`
	Bytes int64 `json:"bytes"`
	Width int `json:"width"`
	Height int `json:"height"`
	Cols uint `json:"cols,omitempty"`
	Rows uint `json:"rows,omitempty"`
	GitHead string `json:"git_head,omitempty"`
	Dirty *bool `json:"dirty,omitempty"`
	Truncated bool `json:"truncated"`
}

type DevArtifactListParams struct {
	DevRunParams
	After string `json:"after,omitempty"`
	Limit int `json:"limit,omitempty"`
}

type DevArtifactListResult struct {
	Artifacts []DevArtifact `json:"artifacts"`
	Next string `json:"next,omitempty"`
	Truncated bool `json:"truncated"`
}

type DevArtifactGetParams struct {
	DevRunParams
	ArtifactID string `json:"artifact_id"`
}

type DevArtifactGetResult struct { Artifact DevArtifact `json:"artifact"` }
type DevArtifactDeleteParams struct {
	DevRunParams
	ArtifactID string `json:"artifact_id"`
}

type DevArtifactDeleteResult struct { Deleted bool `json:"deleted"` }
