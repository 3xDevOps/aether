package protocol

import "time"

// The preference methods act on the caller's own preferences only. No
// connector may call them.
const (
	MethodPreferenceList   = "preference.list"
	MethodPreferenceAdd    = "preference.add"
	MethodPreferenceUpdate = "preference.update"
	MethodPreferenceDelete = "preference.delete"

	// MethodAssistantPreferences, MethodAssistantRemember and
	// MethodAssistantForget are served only on the assistant tool socket.
	MethodAssistantPreferences = "assistant.preferences"
	MethodAssistantRemember    = "assistant.remember"
	MethodAssistantForget      = "assistant.forget"
	// MethodAssistantPreferenceShow reads one preference for the card of
	// MethodAssistantForget. The tool socket refuses it.
	MethodAssistantPreferenceShow = "assistant.preference.show"
)

// Where a preference came from; the server sets it from the path that saved
// it.
const (
	PreferenceSourceAssistant = "assistant"
	PreferenceSourceSettings  = "settings"
)

// Preference is one thing a member asked the assistant to remember about
// how they work. No workspace means it applies in all of them.
type Preference struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Text        string `json:"text"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
	// SourceRef is the id of the assistant transcript message the text was
	// quoted from.
	SourceRef  string     `json:"source_ref,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type PreferenceListParams struct {
	// WorkspaceID limits the list to that workspace's preferences and those
	// for all workspaces.
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Query keeps the preferences whose text contains it, ignoring case.
	Query           string `json:"query,omitempty"`
	IncludeDisabled bool   `json:"include_disabled,omitempty"`
}

type PreferenceListResult struct {
	Preferences []Preference `json:"preferences"`
	// Enabled is the member's setting that lets the assistant read and save
	// preferences.
	Enabled bool `json:"enabled"`
}

type PreferenceAddParams struct {
	Text        string `json:"text"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// PreferenceUpdateParams changes each field that is set. ClearWorkspace
// makes the preference apply in all workspaces.
type PreferenceUpdateParams struct {
	ID             string  `json:"id"`
	Text           *string `json:"text,omitempty"`
	WorkspaceID    string  `json:"workspace_id,omitempty"`
	ClearWorkspace bool    `json:"clear_workspace,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
}

type PreferenceIDParams struct {
	ID string `json:"id"`
}

// PreferenceResult is the saved preference and, after an update, the row
// before it, which undoes the change.
type PreferenceResult struct {
	Preference Preference  `json:"preference"`
	Previous   *Preference `json:"previous,omitempty"`
}

type PreferenceDeleteResult struct {
	Deleted Preference `json:"deleted"`
}

// AssistantPreferencesParams is the assistant's read of its member's
// enabled preferences that apply to the conversation, filtered by Query.
type AssistantPreferencesParams struct {
	Query string `json:"query,omitempty"`
}

type AssistantPreference struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Scope is "all" or "workspace".
	Scope     string    `json:"scope"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AssistantPreferencesResult struct {
	Preferences []AssistantPreference `json:"preferences"`
}

// AssistantRememberParams saves text, which must quote the member's latest
// message. Replaces names a preference the text updates.
type AssistantRememberParams struct {
	Text            string `json:"text"`
	WorkspaceScoped bool   `json:"workspace_scoped,omitempty"`
	Replaces        string `json:"replaces,omitempty"`
}
