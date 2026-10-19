package protocol

import (
	"time"

	"github.com/3xDevOps/Aether/internal/store"
)

// Team memory methods. See docs/plans/2026-10-19-team-memory.md. The run
// socket serves add, search, show and brief; pin, unpin and rm are for
// members only.
const (
	MethodMemoryAdd    = "memory.add"
	MethodMemorySearch = "memory.search"
	MethodMemoryShow   = "memory.show"
	MethodMemoryBrief  = "memory.brief"
	MethodMemoryPin    = "memory.pin"
	MethodMemoryUnpin  = "memory.unpin"
	MethodMemoryRm     = "memory.rm"
)

// Outcome notes are written by the server only, from a run's report.
const (
	MemoryKindDecision   = "decision"
	MemoryKindConvention = "convention"
	MemoryKindGotcha     = "gotcha"
	MemoryKindPreference = "preference"
	MemoryKindOutcome    = "outcome"

	MemoryScopeWorkspace = "workspace"
	MemoryScopeMember    = "member"

	MemorySourceAgent  = "agent"
	MemorySourceMember = "member"
	MemorySourceReport = "report"
	MemorySourceAnswer = "answer"
)

const (
	MemoryMaxTitleBytes       = store.MemoryMaxTitleBytes
	MemoryMaxBodyBytes        = 1 << 10
	MemoryMaxFiles            = store.MemoryMaxFiles
	MemoryMaxAgentNotesPerRun = 20
	MemoryMaxSimilar          = 3
	MemoryPinWorkspaceBytes   = store.MemoryPinWorkspaceBytes
	MemoryPinMemberBytes      = store.MemoryPinMemberBytes
	MemorySearchLimit         = 10
	MemorySearchMaxLimit      = 100
	MemoryBriefMaxBytes       = 2 << 10
	MemoryBriefMaxRuns        = 5
	MemoryBriefTaskBytes      = 80
	MemoryBriefWindow         = 48 * time.Hour
)

// MemoryItem is one note with its provenance. AuthorID is the member the
// note is attributed to: the run's owner for agent and report notes, the
// answering member for answers. Commit and Files are the run's latest
// snapshot when the note was written; PR fields are the run's latest pull
// request. SupersededBy and InvalidAt are set on a replaced note.
type MemoryItem struct {
	ID           string   `json:"id"`
	WorkspaceID  string   `json:"workspace_id"`
	Kind         string   `json:"kind"`
	Scope        string   `json:"scope"`
	Source       string   `json:"source"`
	SourceRef    string   `json:"source_ref,omitempty"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Files        []string `json:"files,omitempty"`
	AuthorID     string   `json:"author_id,omitempty"`
	AuthorName   string   `json:"author_name,omitempty"`
	RunID        string   `json:"run_id,omitempty"`
	Harness      string   `json:"harness,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	PRNumber     int      `json:"pr_number,omitempty"`
	PRState      string   `json:"pr_state,omitempty"`
	PRURL        string   `json:"pr_url,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
	InvalidAt    *string  `json:"invalid_at,omitempty"`
	PinnedBy     string   `json:"pinned_by,omitempty"`
	PinnedByName string   `json:"pinned_by_name,omitempty"`
	PinnedAt     *string  `json:"pinned_at,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

func MemoryItemFromStore(m *store.Memory) MemoryItem {
	return MemoryItem{
		ID: m.ID, WorkspaceID: string(m.WorkspaceID), Kind: string(m.Kind), Scope: string(m.Scope),
		Source: string(m.Source), SourceRef: m.SourceRef, Title: m.Title, Body: m.Body,
		Files: append([]string(nil), m.Files...), AuthorID: string(m.AuthorID), AuthorName: m.AuthorName,
		RunID: string(m.RunID), Harness: m.Harness, Commit: m.Commit,
		PRNumber: m.PRNumber, PRState: m.PRState, PRURL: m.PRURL,
		SupersededBy: m.SupersededBy, InvalidAt: rfc3339Ptr(m.InvalidAt),
		PinnedBy: string(m.PinnedBy), PinnedByName: m.PinnedByName, PinnedAt: rfc3339Ptr(m.PinnedAt),
		CreatedAt: rfc3339(m.CreatedAt), UpdatedAt: rfc3339(m.UpdatedAt),
	}
}

// MemoryAddParams writes one note. On the run socket the server fills the
// workspace, author, run, harness and commit. Scope member keeps a pin on
// it out of other members' briefs. Pin is for members only.
type MemoryAddParams struct {
	WorkspaceID string   `json:"workspace_id,omitempty"`
	Kind        string   `json:"kind"`
	Text        string   `json:"text"`
	Files       []string `json:"files,omitempty"`
	Supersedes  string   `json:"supersedes,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Pin         bool     `json:"pin,omitempty"`
}

// MemoryAddResult is the stored note, or the live note with the same text
// when Duplicate is set, and up to MemoryMaxSimilar other live notes like it.
type MemoryAddResult struct {
	Item      MemoryItem   `json:"item"`
	Duplicate bool         `json:"duplicate,omitempty"`
	Similar   []MemoryItem `json:"similar,omitempty"`
}

// MemorySearchParams matches Words against titles, bodies and files, best
// first; no words lists the newest notes. File keeps notes citing that path.
// All includes superseded and invalidated notes.
type MemorySearchParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Words       string `json:"words,omitempty"`
	Kind        string `json:"kind,omitempty"`
	File        string `json:"file,omitempty"`
	All         bool   `json:"all,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type MemorySearchResult struct {
	Items []MemoryItem `json:"items"`
}

type MemoryShowParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	ID          string `json:"id"`
}

// MemoryShowResult carries the note and every note it replaced or was
// replaced by, oldest first.
type MemoryShowResult struct {
	Item  MemoryItem   `json:"item"`
	Chain []MemoryItem `json:"chain,omitempty"`
}

// MemoryBriefParams selects whose brief to build; an empty MemberID is the
// caller. The run socket takes no parameters.
type MemoryBriefParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	MemberID    string `json:"member_id,omitempty"`
}

// MemoryBriefResult.Text is empty when there is nothing to brief.
type MemoryBriefResult struct {
	Text string `json:"text"`
}

// MemoryPinParams pins a note into every brief in the workspace, or with
// Scope member into the caller's own briefs only.
type MemoryPinParams struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id"`
	Scope       string `json:"scope,omitempty"`
}

type MemoryPinResult struct {
	Item MemoryItem `json:"item"`
}

type MemoryUnpinParams struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id"`
}

type MemoryUnpinResult struct {
	Item MemoryItem `json:"item"`
}

// MemoryRmParams deletes one note by ID, or every note RunID wrote.
type MemoryRmParams struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
}

type MemoryRmResult struct {
	IDs []string `json:"ids"`
}
