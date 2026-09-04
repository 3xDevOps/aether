package protocol

const (
	// MethodFilesTree lists the immediate children of a workspace or run path.
	MethodFilesTree = "files.tree"
	// MethodFilesRead reads one file from a workspace or run checkout.
	MethodFilesRead = "files.read"
	// MethodFilesDiff renders one run file against its recorded base.
	MethodFilesDiff = "files.diff"
)

// FilesTreeParams addresses a workspace tree, or a run checkout when RunID is
// set. Path is relative to the repository root and may be empty for root.
type FilesTreeParams struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id,omitempty"`
	Path        string `json:"path"`
}

// FilesTreeEntry is one immediate file or directory child.
type FilesTreeEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

// FilesTreeResult is the reply to files.tree.
type FilesTreeResult struct {
	Entries []FilesTreeEntry `json:"entries"`
}

// FilesReadParams addresses one file in a workspace or run checkout.
type FilesReadParams struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id,omitempty"`
	Path        string `json:"path"`
}

// FilesReadResult is the reply to files.read. Content is text when Binary is
// false; clients use Binary to display a notice instead of rendering it.
type FilesReadResult struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Size      int64  `json:"size"`
}

// FilesDiffParams addresses one file in a run checkout.
type FilesDiffParams struct {
	RunID string `json:"run_id"`
	Path  string `json:"path"`
}

// FilesDiffResult is the reply to files.diff.
type FilesDiffResult struct {
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}
