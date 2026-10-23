// Package memory is team memory: short attributed notes that every run and
// member of a workspace can search and write, and the launch brief built
// from what members pinned. docs/plans/2026-10-19-team-memory.md holds the
// design; docs/security.md the trust tiers.
package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/secretscan"
	"github.com/3xDevOps/Aether/internal/store"
)

// Store is what team memory reads and writes; *store.DB satisfies it.
type Store interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error)
	InsertMemory(ctx context.Context, m *store.Memory) (bool, error)
	SupersedeMemory(ctx context.Context, old string, m *store.Memory, vet func(old *store.Memory) error) (duplicate, movedPin bool, err error)
	SearchMemory(ctx context.Context, q store.MemoryQuery) ([]*store.Memory, error)
	GetMemory(ctx context.Context, workspace domain.WorkspaceID, id string) (*store.Memory, []*store.Memory, error)
	PinMemory(ctx context.Context, workspace domain.WorkspaceID, id string, by domain.MemberID, scope store.MemoryScope) (*store.Memory, error)
	UnpinMemory(ctx context.Context, workspace domain.WorkspaceID, id string) (*store.Memory, error)
	DeleteMemory(ctx context.Context, workspace domain.WorkspaceID, id string) error
	DeleteMemoryByRun(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) ([]string, error)
	CountAgentNotesByRun(ctx context.Context, run domain.RunID) (int, error)
	ReadMemoryBrief(ctx context.Context, q store.MemoryBriefQuery) (*store.MemoryBrief, error)
}

type Config struct {
	Store Store
	// Bus carries memory.change events to the activity feed.
	Bus events.Bus
	// Log holds the run.diff snapshots whose HEAD an agent's note cites.
	Log events.EventLog
}

type Service struct {
	cfg Config
	// agentWrites makes a run's note count and its next insert one step,
	// so concurrent adds from one run cannot pass its limit.
	agentWrites sync.Mutex
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil || cfg.Log == nil {
		return nil, errors.New("memory: config: Store, Bus and Log are required")
	}
	return &Service{cfg: cfg}, nil
}

// frameTag matches the brief's own tag, so no note can open or close it.
var frameTag = regexp.MustCompile(`(?i)</?team-memory`)

// Add stores a member's note, pinning it when p.Pin is set.
func (s *Service) Add(ctx context.Context, member domain.MemberID, p protocol.MemoryAddParams) (protocol.MemoryAddResult, error) {
	return s.add(ctx, member, nil, p)
}

// add stores a note for member, or for run's agent when run is set. The
// checks run in a fixed order so a refusal names the first rule the note
// breaks: push permission, kind and caps, secrets, the brief's tag, the
// per-run limit, then what an agent may replace.
func (s *Service) add(ctx context.Context, member domain.MemberID, run *domain.Run, p protocol.MemoryAddParams) (protocol.MemoryAddResult, error) {
	const method = protocol.MethodMemoryAdd
	workspace := domain.WorkspaceID(p.WorkspaceID)
	if workspace == "" {
		return protocol.MemoryAddResult{}, invalid(method, "workspace is required")
	}
	if err := s.authorize(ctx, member, workspace); err != nil {
		return protocol.MemoryAddResult{}, fail(method, err)
	}
	text := strings.TrimSpace(p.Text)
	if err := checkNote(p.Kind, p.Scope, text, p.Files); err != nil {
		return protocol.MemoryAddResult{}, err
	}
	if run != nil && p.Pin {
		return protocol.MemoryAddResult{}, &protocol.Error{Code: protocol.CodeDenied,
			Message: "memory.add: agents cannot pin notes; ask a member to pin it (aether memory pin ID)"}
	}
	if hits := secretscan.Scan("", []byte(strings.Join(append([]string{text}, p.Files...), "\n"))); len(hits) > 0 {
		return protocol.MemoryAddResult{}, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"memory.add: rejected, gitleaks rule %s at %s; remove the secret and retry", hits[0].Kind, hits[0].Location)}
	}
	if frameTag.MatchString(text) {
		return protocol.MemoryAddResult{}, &protocol.Error{Code: protocol.CodeDenied,
			Message: "memory.add: rejected, a note cannot contain <team-memory; remove it and retry"}
	}
	note := &store.Memory{
		WorkspaceID: workspace, Kind: store.MemoryKind(p.Kind), Scope: store.MemoryScope(p.Scope),
		Source: store.MemoryFromMember, Body: text, Files: p.Files, AuthorID: member,
	}
	if run != nil {
		s.agentWrites.Lock()
		defer s.agentWrites.Unlock()
		written, err := s.cfg.Store.CountAgentNotesByRun(ctx, run.ID)
		if err != nil {
			return protocol.MemoryAddResult{}, fail(method, err)
		}
		if written >= protocol.MemoryMaxAgentNotesPerRun {
			return protocol.MemoryAddResult{}, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
				"memory.add: run %s already wrote %d notes, the most one run may write; ask a member to add more (aether memory add)",
				run.ID, written)}
		}
		head, err := s.head(ctx, run)
		if err != nil {
			return protocol.MemoryAddResult{}, fail(method, err)
		}
		note.Source, note.RunID, note.Harness, note.Commit = store.MemoryFromAgent, run.ID, run.Harness, head
	}
	var (
		duplicate, movedPin bool
		err                 error
	)
	if p.Supersedes != "" {
		var vet func(*store.Memory) error
		if run != nil {
			vet = agentMayReplace
		}
		duplicate, movedPin, err = s.cfg.Store.SupersedeMemory(ctx, p.Supersedes, note, vet)
	} else {
		duplicate, err = s.cfg.Store.InsertMemory(ctx, note)
	}
	if err != nil {
		return protocol.MemoryAddResult{}, fail(method, err)
	}
	var runID domain.RunID
	if run != nil {
		runID = run.ID
	}
	if !duplicate {
		if err = s.changed(ctx, workspace, runID, member, events.MemoryActionAdd, note.ID); err != nil {
			return protocol.MemoryAddResult{}, fail(method, err)
		}
	}
	if p.Supersedes != "" {
		if err = s.changed(ctx, workspace, runID, member, events.MemoryActionSupersede, p.Supersedes); err != nil {
			return protocol.MemoryAddResult{}, fail(method, err)
		}
		if movedPin && !p.Pin {
			if err = s.changed(ctx, workspace, runID, member, events.MemoryActionPin, note.ID); err != nil {
				return protocol.MemoryAddResult{}, fail(method, err)
			}
		}
	}
	item := protocol.MemoryItemFromStore(note)
	if p.Pin {
		pinned, pinErr := s.Pin(ctx, member, protocol.MemoryPinParams{WorkspaceID: string(workspace), ID: note.ID, Scope: p.Scope})
		if pinErr != nil {
			rpcErr := fail(protocol.MethodMemoryPin, pinErr)
			return protocol.MemoryAddResult{}, &protocol.Error{Code: rpcErr.Code,
				Message: fmt.Sprintf("memory.add: %s is stored but not pinned: %s", note.ID, rpcErr.Message)}
		}
		item = pinned.Item
	}
	similar, err := s.similar(ctx, workspace, item.ID, text)
	if err != nil {
		return protocol.MemoryAddResult{}, err
	}
	return protocol.MemoryAddResult{Item: item, Duplicate: duplicate, Similar: similar}, nil
}

// checkNote enforces the note's kind and caps. Outcome notes come only
// from run reports.
func checkNote(kind, scope, text string, files []string) error {
	const method = protocol.MethodMemoryAdd
	switch kind {
	case protocol.MemoryKindDecision, protocol.MemoryKindConvention, protocol.MemoryKindGotcha, protocol.MemoryKindPreference:
	default:
		return invalid(method, fmt.Sprintf("kind must be decision, convention, gotcha or preference; got %q", kind))
	}
	switch scope {
	case "", protocol.MemoryScopeWorkspace, protocol.MemoryScopeMember:
	default:
		return invalid(method, fmt.Sprintf("scope must be workspace or member; got %q", scope))
	}
	if text == "" {
		return invalid(method, "text is required")
	}
	if len(text) > protocol.MemoryMaxBodyBytes {
		return invalid(method, fmt.Sprintf("text is %d bytes; a note holds at most %d", len(text), protocol.MemoryMaxBodyBytes))
	}
	if strings.ContainsRune(text, 0) {
		return invalid(method, "text contains a NUL byte; a note cannot hold one")
	}
	if len(files) > protocol.MemoryMaxFiles {
		return invalid(method, fmt.Sprintf("%d files cited; a note cites at most %d", len(files), protocol.MemoryMaxFiles))
	}
	for i, file := range files {
		if len(file) > protocol.MemoryMaxFileBytes {
			return invalid(method, fmt.Sprintf("file %d is %d bytes; a cited path holds at most %d", i+1, len(file), protocol.MemoryMaxFileBytes))
		}
		if strings.TrimSpace(file) == "" || strings.ContainsAny(file, "\r\n") {
			return invalid(method, fmt.Sprintf("file %q must be one non-empty line", file))
		}
	}
	return nil
}

// agentMayReplace lets an agent replace only another agent's unpinned
// note; anything a member wrote, answered or pinned stays theirs to change.
func agentMayReplace(old *store.Memory) error {
	var standing string
	switch {
	case old.PinnedAt != nil:
		standing = "is pinned by " + name(old.PinnedByName, string(old.PinnedBy))
	case old.Source == store.MemoryFromAgent:
		return nil
	case old.Source == store.MemoryFromReport:
		standing = "is the outcome of run " + string(old.RunID)
	default:
		standing = "was written by " + name(old.AuthorName, string(old.AuthorID))
	}
	return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
		"memory.add: %s %s; ask a member to replace it (aether memory add --supersedes %s)", old.ID, standing, old.ID)}
}

func name(display, id string) string {
	if display != "" {
		return display
	}
	return id
}

// head is the run checkout's HEAD at its latest run.diff snapshot, empty
// before the first one. A run's extra repositories publish run.diff under its
// ID too, so it pages back past theirs.
func (s *Service) head(ctx context.Context, run *domain.Run) (string, error) {
	before, err := s.cfg.Log.LastSeq(ctx)
	if err != nil {
		return "", fmt.Errorf("read the latest diff of run %s: %w", run.ID, err)
	}
	filter := events.Filter{Workspace: run.WorkspaceID, Run: run.ID, Types: []events.Type{events.TypeRunDiff}}
	for before++; ; {
		diffs, err := s.cfg.Log.ReadBefore(ctx, filter, before, headDiffPage)
		if err != nil {
			return "", fmt.Errorf("read the latest diff of run %s: %w", run.ID, err)
		}
		for _, event := range diffs {
			if diff, ok := event.Payload.(events.RunDiffPayload); ok && diff.Repo == "" {
				return diff.Head, nil
			}
		}
		if len(diffs) < headDiffPage {
			return "", nil
		}
		before = diffs[len(diffs)-1].Seq
	}
}

const headDiffPage = 32

// similar is the best live matches for text other than the note itself.
func (s *Service) similar(ctx context.Context, workspace domain.WorkspaceID, self, text string) ([]protocol.MemoryItem, error) {
	hits, err := s.cfg.Store.SearchMemory(ctx, store.MemoryQuery{Workspace: workspace, Words: text, Limit: protocol.MemoryMaxSimilar + 1})
	if err != nil {
		return nil, fail(protocol.MethodMemoryAdd, err)
	}
	var out []protocol.MemoryItem
	for _, hit := range hits {
		if hit.ID != self && len(out) < protocol.MemoryMaxSimilar {
			out = append(out, protocol.MemoryItemFromStore(hit))
		}
	}
	return out, nil
}

// Search lists the workspace's notes matching p, best first. Every member
// may read team memory, so it checks no permission.
func (s *Service) Search(ctx context.Context, p protocol.MemorySearchParams) (protocol.MemorySearchResult, error) {
	const method = protocol.MethodMemorySearch
	if p.WorkspaceID == "" {
		return protocol.MemorySearchResult{}, invalid(method, "workspace is required")
	}
	switch p.Kind {
	case "", protocol.MemoryKindDecision, protocol.MemoryKindConvention, protocol.MemoryKindGotcha,
		protocol.MemoryKindPreference, protocol.MemoryKindOutcome:
	default:
		return protocol.MemorySearchResult{}, invalid(method,
			fmt.Sprintf("kind must be decision, convention, gotcha, preference or outcome; got %q", p.Kind))
	}
	switch p.Source {
	case "", protocol.MemorySourceAgent, protocol.MemorySourceMember, protocol.MemorySourceReport, protocol.MemorySourceAnswer:
	default:
		return protocol.MemorySearchResult{}, invalid(method, fmt.Sprintf("source must be agent, member, report or answer; got %q", p.Source))
	}
	if p.Limit == 0 {
		p.Limit = protocol.MemorySearchLimit
	}
	if p.Limit < 0 || p.Limit > protocol.MemorySearchMaxLimit {
		return protocol.MemorySearchResult{}, invalid(method,
			fmt.Sprintf("limit must be between 1 and %d; got %d", protocol.MemorySearchMaxLimit, p.Limit))
	}
	notes, err := s.cfg.Store.SearchMemory(ctx, store.MemoryQuery{Workspace: domain.WorkspaceID(p.WorkspaceID),
		Words: p.Words, Kind: store.MemoryKind(p.Kind), Source: store.MemorySource(p.Source), File: strings.TrimSpace(p.File),
		All: p.All, Replaced: p.Replaced, Pinned: p.Pinned, Limit: p.Limit})
	if err != nil {
		return protocol.MemorySearchResult{}, fail(method, err)
	}
	return protocol.MemorySearchResult{Items: items(notes)}, nil
}

// Show returns one note and its supersede chain.
func (s *Service) Show(ctx context.Context, p protocol.MemoryShowParams) (protocol.MemoryShowResult, error) {
	const method = protocol.MethodMemoryShow
	if p.WorkspaceID == "" || p.ID == "" {
		return protocol.MemoryShowResult{}, invalid(method, "workspace and note ID are required")
	}
	note, chain, err := s.cfg.Store.GetMemory(ctx, domain.WorkspaceID(p.WorkspaceID), p.ID)
	if err != nil {
		return protocol.MemoryShowResult{}, fail(method, err)
	}
	return protocol.MemoryShowResult{Item: protocol.MemoryItemFromStore(note), Chain: items(chain)}, nil
}

// Pin puts a note into briefs: every member's, or with scope member only
// member's own.
func (s *Service) Pin(ctx context.Context, member domain.MemberID, p protocol.MemoryPinParams) (protocol.MemoryPinResult, error) {
	const method = protocol.MethodMemoryPin
	if p.WorkspaceID == "" || p.ID == "" {
		return protocol.MemoryPinResult{}, invalid(method, "workspace and note ID are required")
	}
	switch p.Scope {
	case "", protocol.MemoryScopeWorkspace, protocol.MemoryScopeMember:
	default:
		return protocol.MemoryPinResult{}, invalid(method, fmt.Sprintf("scope must be workspace or member; got %q", p.Scope))
	}
	workspace := domain.WorkspaceID(p.WorkspaceID)
	if err := s.authorize(ctx, member, workspace); err != nil {
		return protocol.MemoryPinResult{}, fail(method, err)
	}
	note, err := s.cfg.Store.PinMemory(ctx, workspace, p.ID, member, store.MemoryScope(p.Scope))
	if err != nil {
		return protocol.MemoryPinResult{}, fail(method, err)
	}
	if err := s.changed(ctx, workspace, "", member, events.MemoryActionPin, note.ID); err != nil {
		return protocol.MemoryPinResult{}, fail(method, err)
	}
	return protocol.MemoryPinResult{Item: protocol.MemoryItemFromStore(note)}, nil
}

func (s *Service) Unpin(ctx context.Context, member domain.MemberID, p protocol.MemoryUnpinParams) (protocol.MemoryUnpinResult, error) {
	const method = protocol.MethodMemoryUnpin
	if p.WorkspaceID == "" || p.ID == "" {
		return protocol.MemoryUnpinResult{}, invalid(method, "workspace and note ID are required")
	}
	workspace := domain.WorkspaceID(p.WorkspaceID)
	if err := s.authorize(ctx, member, workspace); err != nil {
		return protocol.MemoryUnpinResult{}, fail(method, err)
	}
	note, err := s.cfg.Store.UnpinMemory(ctx, workspace, p.ID)
	if err != nil {
		return protocol.MemoryUnpinResult{}, fail(method, err)
	}
	if err := s.changed(ctx, workspace, "", member, events.MemoryActionUnpin, note.ID); err != nil {
		return protocol.MemoryUnpinResult{}, fail(method, err)
	}
	return protocol.MemoryUnpinResult{Item: protocol.MemoryItemFromStore(note)}, nil
}

// Remove deletes one note, or every note p.RunID wrote, and returns the
// IDs it deleted.
func (s *Service) Remove(ctx context.Context, member domain.MemberID, p protocol.MemoryRmParams) (protocol.MemoryRmResult, error) {
	const method = protocol.MethodMemoryRm
	if p.WorkspaceID == "" || (p.ID == "") == (p.RunID == "") {
		return protocol.MemoryRmResult{}, invalid(method, "workspace and exactly one of a note ID or a run are required")
	}
	workspace := domain.WorkspaceID(p.WorkspaceID)
	if err := s.authorize(ctx, member, workspace); err != nil {
		return protocol.MemoryRmResult{}, fail(method, err)
	}
	ids := []string{p.ID}
	var err error
	if p.RunID != "" {
		ids, err = s.cfg.Store.DeleteMemoryByRun(ctx, workspace, domain.RunID(p.RunID))
	} else {
		err = s.cfg.Store.DeleteMemory(ctx, workspace, p.ID)
	}
	if err != nil {
		return protocol.MemoryRmResult{}, fail(method, err)
	}
	if ids == nil {
		ids = []string{}
	}
	if err := s.changed(ctx, workspace, "", member, events.MemoryActionRm, ids...); err != nil {
		return protocol.MemoryRmResult{}, fail(method, err)
	}
	return protocol.MemoryRmResult{IDs: ids}, nil
}

// authorize re-reads member on every write, so a role change applies to
// the next call.
func (s *Service) authorize(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) error {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return err
	}
	return permissions.Check(permissions.Push, permissions.Actor{ID: m.ID, Role: m.Role}, permissions.Target{Workspace: workspace})
}

// changed records each note's change for the activity feed. run is the
// run whose agent acted, empty for a member.
func (s *Service) changed(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, actor domain.MemberID, action events.MemoryAction, ids ...string) error {
	for _, id := range ids {
		if _, err := s.cfg.Bus.Publish(ctx, events.Event{WorkspaceID: workspace, RunID: run, ActorID: actor,
			Payload: events.MemoryChangePayload{Action: action, NoteID: id, ActorID: actor}}); err != nil {
			return fmt.Errorf("%s %s is done, but its memory.change event was not published: %w", action, id, err)
		}
	}
	return nil
}

func items(notes []*store.Memory) []protocol.MemoryItem {
	out := make([]protocol.MemoryItem, 0, len(notes))
	for _, n := range notes {
		out = append(out, protocol.MemoryItemFromStore(n))
	}
	return out
}

func invalid(method, msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: method + ": " + msg}
}

// fail gives err its wire code and names the method, so the run socket
// and the control channel report it alike.
func fail(method string, err error) *protocol.Error {
	var rpcErr *protocol.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	var budget *store.MemoryPinBudgetError
	code := protocol.CodeInternal
	switch {
	case errors.Is(err, permissions.ErrDenied):
		code = protocol.CodeDenied
	case errors.Is(err, store.ErrNotFound):
		code = protocol.CodeNotFound
	case errors.Is(err, store.ErrConflict), errors.As(err, &budget):
		code = protocol.CodeConflict
	}
	return &protocol.Error{Code: code, Message: method + ": " + err.Error()}
}
