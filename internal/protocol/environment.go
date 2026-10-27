package protocol

const (
	// MethodWorkspaceEnvironmentGet reads a workspace's setup script,
	// variables and files. A secret is listed by name and a file by path and
	// size; a secret's value and a file's content are never returned.
	MethodWorkspaceEnvironmentGet = "workspace.environment.get"
	// MethodWorkspaceEnvironmentSet changes them (admin only).
	MethodWorkspaceEnvironmentSet = "workspace.environment.set"
)

const (
	MaxWorkspaceSetupScriptBytes   = 64 << 10
	MaxWorkspaceVariableValueBytes = 64 << 10
	MaxWorkspaceFiles              = 20
)

// WorkspaceVariable is one environment variable of a workspace. Value is
// empty for a secret on read.
type WorkspaceVariable struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

// WorkspaceFile is a file every new run gets in its checkout, at Path
// relative to the repository root.
type WorkspaceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WorkspaceFileInfo is how workspace.environment.get lists a WorkspaceFile.
type WorkspaceFileInfo struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

type WorkspaceEnvironmentGetParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// WorkspaceEnvironmentSetParams: Set adds or replaces variables, Unset
// removes them, and a non-nil SetupScript replaces the script ("" clears
// it). Files adds or replaces files after RemoveFiles removes them. A
// non-nil Egress turns egress control on or off, EgressAllow adds hosts and
// EgressDeny removes them. Everything not named is kept.
type WorkspaceEnvironmentSetParams struct {
	WorkspaceID string              `json:"workspace_id"`
	SetupScript *string             `json:"setup_script,omitempty"`
	Set         []WorkspaceVariable `json:"set,omitempty"`
	Unset       []string            `json:"unset,omitempty"`
	Files       []WorkspaceFile     `json:"files,omitempty"`
	RemoveFiles []string            `json:"remove_files,omitempty"`
	Egress      *bool               `json:"egress,omitempty"`
	EgressAllow []string            `json:"egress_allow,omitempty"`
	EgressDeny  []string            `json:"egress_deny,omitempty"`
}

// WorkspaceEnvironmentResult lists Variables sorted by name, Files sorted by
// path and EgressHosts, egress.Defaults until an admin edits them, sorted.
// Warning is set when workspace.environment.set saved the change and could
// not record its timeline note.
type WorkspaceEnvironmentResult struct {
	WorkspaceID string              `json:"workspace_id"`
	SetupScript string              `json:"setup_script"`
	Variables   []WorkspaceVariable `json:"variables"`
	Files       []WorkspaceFileInfo `json:"files"`
	Egress      bool                `json:"egress"`
	EgressHosts []string            `json:"egress_hosts"`
	Warning     string              `json:"warning,omitempty"`
}

// SetupFailure is the Data of a launch refused by the workspace setup
// script: what the script printed, with secret values masked.
type SetupFailure struct {
	SetupOutput string `json:"setup_output"`
}
