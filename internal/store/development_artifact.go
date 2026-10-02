package store

// DevelopmentArtifact records the original capture observation, not the later
// Git revision retained by its evidence packet. Unknown Git state stays unknown.
type DevelopmentArtifact struct {
	ID               string `json:"id"`
	Path             string `json:"path"`
	Source           string `json:"source"`
	RunID            string `json:"run_id"`
	Incarnation      string `json:"incarnation"`
	TerminalID       string `json:"terminal_id,omitempty"`
	PageID           string `json:"page_id,omitempty"`
	PageRevision     uint64 `json:"page_revision,omitempty"`
	ScreenRevision   uint64 `json:"screen_revision,omitempty"`
	GeometryRevision uint64 `json:"geometry_revision,omitempty"`
	ViewportID       string `json:"viewport_id,omitempty"`
	URL              string `json:"url,omitempty"`
	CapturedAt       string `json:"captured_at"`
	ContentType      string `json:"content_type"`
	Bytes            int64  `json:"bytes"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	Cols             uint   `json:"cols,omitempty"`
	Rows             uint   `json:"rows,omitempty"`
	GitHead          string `json:"git_head,omitempty"`
	Dirty            *bool  `json:"dirty,omitempty"`
	Truncated        bool   `json:"truncated"`
}
