package protocol

// The home.* methods address the authenticated member's own persistent home.
// Paths are slash-separated and relative to it; "" or "." is the home itself
// where a directory is expected.
const (
	MethodHomeTree     = "home.tree"
	MethodHomeRead     = "home.read"
	MethodHomeWrite    = "home.write"
	MethodHomeMkdir    = "home.mkdir"
	MethodHomeRename   = "home.rename"
	MethodHomeRemove   = "home.remove"
	MethodHomeUpload   = "home.upload"
	MethodHomeDownload = "home.download"
)

// HomePathParams names one path for home.tree, home.read, home.mkdir,
// home.remove and home.download.
type HomePathParams struct {
	Path string `json:"path"`
}

// HomeTreeEntry is one immediate child. Kind is "dir", "file" or "link", a
// symlink that is never followed. Locked marks a credential file, which is
// listed but cannot be read, written or downloaded.
type HomeTreeEntry struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	Locked bool   `json:"locked"`
}

type HomeTreeResult struct {
	Entries []HomeTreeEntry `json:"entries"`
}

// HomeWriteParams saves complete UTF-8 text. An empty Revision creates the
// file and refuses an existing one.
type HomeWriteParams struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type HomeRenameParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// HomeUploadParams writes one file of arbitrary bytes, creating missing
// parent directories. An existing destination needs Overwrite.
type HomeUploadParams struct {
	Path      string `json:"path"`
	Content   []byte `json:"content_base64"`
	Overwrite bool   `json:"overwrite"`
}

type HomeDownloadResult struct {
	Content []byte `json:"content_base64"`
	Size    int64  `json:"size"`
}
