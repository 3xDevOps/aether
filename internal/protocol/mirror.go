package protocol

import "github.com/3xDevOps/Aether/internal/domain"

// WorkspaceMirrorParams addresses a workspace mirror.
type WorkspaceMirrorParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// WorkspaceMirrorConfigureParams configures a workspace's upstream mirror.
type WorkspaceMirrorConfigureParams struct {
	WorkspaceID string `json:"workspace_id"`
	SourceURL   string `json:"source_url"`
	Branch      string `json:"branch"`
	Auth        string `json:"auth"`
	KnownHosts  string `json:"known_hosts,omitempty"`
}

// WorkspaceMirrorAdoptParams promotes the candidate observed for a specific
// mirror generation to the accepted base.
type WorkspaceMirrorAdoptParams struct {
	WorkspaceID string `json:"workspace_id"`
	Generation  int64  `json:"generation"`
}

// WorkspaceMirrorResult is the public state of a workspace mirror. It never
// contains private key bytes or host paths. A false Enabled value represents a
// local-only workspace with no configured mirror.
type WorkspaceMirrorResult struct {
	Enabled        bool    `json:"enabled"`
	SourceURL      string  `json:"source_url,omitempty"`
	SourceIdentity string  `json:"source_identity,omitempty"`
	Branch         string  `json:"branch,omitempty"`
	Auth           string  `json:"auth,omitempty"`
	Generation     int64   `json:"generation,omitempty"`
	Status         string  `json:"status,omitempty"`
	ObservedCommit string  `json:"observed_commit,omitempty"`
	AcceptedCommit string  `json:"accepted_commit,omitempty"`
	KeyFingerprint string  `json:"key_fingerprint,omitempty"`
	LastError      string  `json:"last_error,omitempty"`
	CreatedAt      string  `json:"created_at,omitempty"`
	UpdatedAt      string  `json:"updated_at,omitempty"`
	LastAttemptAt  *string `json:"last_attempt_at,omitempty"`
	LastSuccessAt  *string `json:"last_success_at,omitempty"`
	PublicKey      string  `json:"public_key,omitempty"`
	Warning        string  `json:"warning,omitempty"`
}

// WorkspaceMirrorResultFromDomain converts persisted mirror state to its
// public wire form. The caller controls Enabled because a successful disable
// returns the prior state for its warning and audit details while leaving the
// workspace local-only.
func WorkspaceMirrorResultFromDomain(m domain.WorkspaceMirror, enabled bool, publicKey, warning string) WorkspaceMirrorResult {
	out := WorkspaceMirrorResult{
		Enabled:        enabled,
		SourceURL:      m.SourceURL,
		SourceIdentity: m.SourceIdentity,
		Branch:         m.Branch,
		Auth:           string(m.Auth),
		Generation:     m.Generation,
		Status:         string(m.Status),
		ObservedCommit: m.ObservedCommit,
		AcceptedCommit: m.AcceptedCommit,
		KeyFingerprint: m.KeyFingerprint,
		LastError:      m.LastError,
		PublicKey:      publicKey,
		Warning:        warning,
	}
	if !m.CreatedAt.IsZero() {
		out.CreatedAt = rfc3339(m.CreatedAt)
	}
	if !m.UpdatedAt.IsZero() {
		out.UpdatedAt = rfc3339(m.UpdatedAt)
	}
	out.LastAttemptAt = rfc3339ValuePtr(m.LastAttemptAt)
	out.LastSuccessAt = rfc3339ValuePtr(m.LastSuccessAt)
	return out
}
