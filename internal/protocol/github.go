package protocol

const (
	MethodGitHubOAuthStart       = "github.oauth.start"
	MethodGitHubOAuthStatus      = "github.oauth.status"
	MethodGitHubOAuthCancel      = "github.oauth.cancel"
	MethodGitHubRepositoriesList = "github.repositories.list"
)

type GitHubOAuthStatusParams struct {
	SessionID string `json:"session_id,omitempty"`
}

type GitHubOAuthCancelParams struct {
	SessionID string `json:"session_id"`
}

type GitHubOAuthResult struct {
	State           string               `json:"state"`
	SessionID       string               `json:"session_id,omitempty"`
	UserCode        string               `json:"user_code,omitempty"`
	VerificationURL string               `json:"verification_url,omitempty"`
	ExpiresAt       string               `json:"expires_at,omitempty"`
	Login           string               `json:"login,omitempty"`
	Connection      *GitHubConnectResult `json:"connection,omitempty"`
	Error           string               `json:"error,omitempty"`
}

type GitHubRepositoriesListParams struct {
	Page int `json:"page,omitempty"`
}

type GitHubAccount struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type GitHubRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	CanPush       bool   `json:"can_push"`
}

type GitHubRepositoryListResult struct {
	Account      GitHubAccount      `json:"account"`
	Repositories []GitHubRepository `json:"repositories"`
	NextPage     int                `json:"next_page,omitempty"`
}
