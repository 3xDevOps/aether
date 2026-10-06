package protocol

const (
	MethodConfigRoots  = "config.roots"
	MethodConfigTree   = "config.tree"
	MethodConfigRead   = "config.read"
	MethodConfigWrite  = "config.write"
	MethodConfigImport = "config.import"
)

// ConfigRoot is one harness profile directory in the member's persistent
// home; request paths are relative to it.
type ConfigRoot struct {
	Harness         string   `json:"harness"`
	DisplayName     string   `json:"display_name"`
	Path            string   `json:"path"`
	RuntimeIgnores  []string `json:"runtime_ignores"`
	CredentialNames []string `json:"credential_names"`
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

// ConfigFileReadResult is files.read's result plus a revision token and
// writable flag for optimistic editor saves.
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
