package protocol

const (
	MethodConfigRoots  = "config.roots"
	MethodConfigTree   = "config.tree"
	MethodConfigRead   = "config.read"
	MethodConfigWrite  = "config.write"
	MethodConfigImport = "config.import"
)

// ConfigRoot identifies one harness profile directory in the authenticated
// member's persistent home. Path is a display/container path such as
// ~/.claude; requests use paths relative to that root.
type ConfigRoot struct {
	Harness        string   `json:"harness"`
	Path           string   `json:"path"`
	RuntimeIgnores []string `json:"runtime_ignores"`
}

type ConfigRootsResult struct {
	Roots []ConfigRoot `json:"roots"`
}

type ConfigTreeParams struct {
	Harness string `json:"harness"`
	Path    string `json:"path"`
}

type ConfigTreeEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

type ConfigTreeResult struct {
	Entries []ConfigTreeEntry `json:"entries"`
}

type ConfigReadParams struct {
	Harness string `json:"harness"`
	Path    string `json:"path"`
}

// ConfigFileReadResult follows files.read's content metadata and adds a
// revision token and writable indicator for optimistic editor saves.
type ConfigFileReadResult struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Size      int64  `json:"size"`
	Revision  string `json:"revision"`
	Writable  bool   `json:"writable"`
}

type ConfigWriteParams struct {
	Harness  string `json:"harness"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type ConfigImportFile struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
	Mode          uint32 `json:"mode"`
}

type ConfigImportParams struct {
	Harness string             `json:"harness"`
	Files   []ConfigImportFile `json:"files"`
}

type ConfigExcluded struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type ConfigImportResult struct {
	Harness       string           `json:"harness"`
	Files         int              `json:"files"`
	Bytes         int64            `json:"bytes"`
	Excluded      []ConfigExcluded `json:"excluded"`
	Error         string           `json:"error,omitempty"`
	ImportedPaths []string         `json:"imported_paths,omitempty"`
}
