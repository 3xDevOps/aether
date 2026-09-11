package protocol

// TerminalRequest carries tab selection and terminal dimensions.
type TerminalRequest struct {
	Tab  string `json:"tab,omitempty"`
	Cols uint   `json:"cols,omitempty"`
	Rows uint   `json:"rows,omitempty"`
}

// TerminalImageParams carries the base64-encoded original image bytes.
// RunID empty targets the caller's environment terminal; when present it
// targets that run after the server checks steering authorization.
type TerminalImageParams struct {
	RunID   string `json:"run_id,omitempty"`
	Content string `json:"content"`
}

// TerminalImageResult is the absolute path to the uploaded image inside the
// target container.
type TerminalImageResult struct {
	Path string `json:"path"`
}

// TerminalResponse is the result of a terminal control operation.
type TerminalResponse struct {
	OK   bool   `json:"ok"`
	Tab  string `json:"tab,omitempty"`
	Cols uint   `json:"cols,omitempty"`
	Rows uint   `json:"rows,omitempty"`
	// Replay is the number of bytes of scrollback replay that follow the ack before live output.
	Replay int    `json:"replay,omitempty"`
	Code   int    `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TerminalStatusResult is the wire form of domain.TerminalStatus.
type TerminalStatusResult struct {
	Running    bool     `json:"running"`
	Image      string   `json:"image,omitempty"`
	SavedImage string   `json:"saved_image,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	Tabs       []string `json:"tabs,omitempty"`
}

// EnvSaveResult is the result of saving a member's environment terminal.
type EnvSaveResult struct {
	Image string `json:"image"`
}
