package protocol

// TerminalRequest carries tab selection and terminal dimensions.
type TerminalRequest struct {
	Tab  string `json:"tab,omitempty"`
	Cols uint   `json:"cols,omitempty"`
	Rows uint   `json:"rows,omitempty"`
}

// TerminalResponse is the result of a terminal control operation.
type TerminalResponse struct {
	OK    bool   `json:"ok"`
	Tab   string `json:"tab,omitempty"`
	Cols  uint   `json:"cols,omitempty"`
	Rows  uint   `json:"rows,omitempty"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// TerminalStatusResult is the wire form of domain.TerminalStatus.
type TerminalStatusResult struct {
	Running   bool     `json:"running"`
	Image     string   `json:"image,omitempty"`
	StartedAt string   `json:"started_at,omitempty"`
	Tabs      []string `json:"tabs,omitempty"`
}
