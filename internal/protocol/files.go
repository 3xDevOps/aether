package protocol

const (
	// MethodFilesTree lists the immediate children of a workspace or run path.
	MethodFilesTree = "files.tree"
	// MethodFilesRead reads one file from a workspace or run checkout.
	MethodFilesRead = "files.read"
	// MethodFilesDiff renders one run file against its recorded base.
	MethodFilesDiff = "files.diff"
	// MethodFilesWrite writes one file to a run checkout or workspace base.
	MethodFilesWrite = "files.write"
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
	// Revision is the SHA-256 of the exact whole file when it is a complete,
	// valid text read. It is empty for truncated and binary files.
	Revision string `json:"revision"`
	// Writable reports whether the authenticated member may save this file.
	Writable bool `json:"writable"`
}

// FilesWriteParams addresses one file in a workspace base or run checkout.
// An empty Revision creates a new file; existing files require the revision
// returned by files.read.
type FilesWriteParams struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id,omitempty"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Revision    string `json:"revision"`
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
