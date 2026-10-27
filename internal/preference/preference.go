// Package preference keeps each member's private assistant preferences:
// short instructions about how they work that they asked the assistant to
// remember. Only the member can read, change or forget theirs.
package preference

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/secretscan"
	"github.com/3xDevOps/Aether/internal/store"
)

// MaxText is the longest preference, in characters.
const MaxText = 500

type Store interface {
	store.PreferenceStore
	GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error)
	AssistantSettings(ctx context.Context, member domain.MemberID) (store.AssistantSettings, error)
}

type Service struct {
	store      Store
	tombstones *tombstones

	mu      sync.Mutex
	changes map[domain.MemberID][]string
}

// New serves preferences from st. Deletions and text changes are recorded
// in tombstonePath, which Restore reads.
func New(st Store, tombstonePath string) *Service {
	return &Service{store: st, tombstones: &tombstones{path: tombstonePath}, changes: make(map[domain.MemberID][]string)}
}

// Normalize is the form preferences are stored and compared in: NFKC,
// without format or control characters such as bidi overrides, with runs
// of whitespace collapsed to one space.
func Normalize(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && !unicode.IsSpace(r)) {
			return -1
		}
		return r
	}, norm.NFKC.String(text))
	return strings.Join(strings.Fields(text), " ")
}

func invalid(format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func notFound(id string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeNotFound, Message: "preference " + id + " not found"}
}

// clean normalizes text and refuses one that is empty, too long or holds a
// secret. The refusal names the secret's kind, never its value.
func clean(text string) (string, error) {
	text = Normalize(text)
	switch n := len([]rune(text)); {
	case n == 0:
		return "", invalid("text is required")
	case n > MaxText:
		return "", invalid("text is %d characters; the limit is %d", n, MaxText)
	}
	if hits := secretscan.Scan("", []byte(text)); len(hits) > 0 {
		return "", &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"preference refused: it looks like a secret (gitleaks rule %s); preferences never hold credentials", hits[0].Kind)}
	}
	return text, nil
}

func (s *Service) workspace(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	if _, err := s.store.GetWorkspace(ctx, domain.WorkspaceID(id)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return invalid("workspace %s not found", id)
		}
		return fmt.Errorf("preference: %w", err)
	}
	return nil
}

func wire(p store.Preference) protocol.Preference {
	out := protocol.Preference{ID: p.ID, WorkspaceID: p.WorkspaceID, Text: p.Text, Enabled: p.Enabled, Source: p.Source,
		SourceRef: p.SourceRef, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if !p.LastUsedAt.IsZero() {
		out.LastUsedAt = &p.LastUsedAt
	}
	return out
}

// Enabled reports the member's setting that lets the assistant use
// preferences.
func (s *Service) Enabled(ctx context.Context, member domain.MemberID) (bool, error) {
	st, err := s.store.AssistantSettings(ctx, member)
	if err != nil {
		return false, fmt.Errorf("preference: %w", err)
	}
	return st.PreferencesEnabled, nil
}

func (s *Service) List(ctx context.Context, member domain.MemberID, p protocol.PreferenceListParams) (protocol.PreferenceListResult, error) {
	enabled, err := s.Enabled(ctx, member)
	if err != nil {
		return protocol.PreferenceListResult{}, err
	}
	rows, err := s.store.ListPreferences(ctx, member)
	if err != nil {
		return protocol.PreferenceListResult{}, fmt.Errorf("preference: %w", err)
	}
	query := strings.ToLower(Normalize(p.Query))
	out := protocol.PreferenceListResult{Preferences: []protocol.Preference{}, Enabled: enabled}
	for _, row := range rows {
		if (!row.Enabled && !p.IncludeDisabled) || (p.WorkspaceID != "" && row.WorkspaceID != "" && row.WorkspaceID != p.WorkspaceID) ||
			!strings.Contains(strings.ToLower(row.Text), query) {
			continue
		}
		out.Preferences = append(out.Preferences, wire(row))
	}
	return out, nil
}

// Get returns the member's preference id; another member's reads as not
// found.
func (s *Service) Get(ctx context.Context, member domain.MemberID, id string) (protocol.Preference, error) {
	row, err := s.store.GetPreference(ctx, member, id)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Preference{}, notFound(id)
	}
	if err != nil {
		return protocol.Preference{}, fmt.Errorf("preference: %w", err)
	}
	return wire(row), nil
}

// Add saves text for member. source and ref say where it came from; the
// caller sets them from its own path, never from a request.
func (s *Service) Add(ctx context.Context, member domain.MemberID, text, workspaceID, source, ref string) (protocol.PreferenceResult, error) {
	text, err := clean(text)
	if err != nil {
		return protocol.PreferenceResult{}, err
	}
	if err = s.workspace(ctx, workspaceID); err != nil {
		return protocol.PreferenceResult{}, err
	}
	row := store.Preference{MemberID: member, WorkspaceID: workspaceID, Text: text, Enabled: true, Source: source, SourceRef: ref}
	if err = s.store.AddPreference(ctx, &row); errors.Is(err, store.ErrPreferenceCap) {
		return protocol.PreferenceResult{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	} else if err != nil {
		return protocol.PreferenceResult{}, fmt.Errorf("preference: %w", err)
	}
	return protocol.PreferenceResult{Preference: wire(row)}, nil
}

// Update applies p to the member's preference. A text change takes source
// and ref, as Add does.
func (s *Service) Update(ctx context.Context, member domain.MemberID, p protocol.PreferenceUpdateParams, source, ref string) (protocol.PreferenceResult, error) {
	if p.ID == "" {
		return protocol.PreferenceResult{}, invalid("id is required")
	}
	if p.WorkspaceID != "" && p.ClearWorkspace {
		return protocol.PreferenceResult{}, invalid("workspace_id and clear_workspace cannot both be set")
	}
	var text string
	if p.Text != nil {
		var err error
		if text, err = clean(*p.Text); err != nil {
			return protocol.PreferenceResult{}, err
		}
	}
	if err := s.workspace(ctx, p.WorkspaceID); err != nil {
		return protocol.PreferenceResult{}, err
	}
	old, cur, err := s.store.UpdatePreference(ctx, member, p.ID, func(row *store.Preference) error {
		if p.Text != nil && text != row.Text {
			row.Text, row.Source, row.SourceRef = text, source, ref
		}
		if p.WorkspaceID != "" || p.ClearWorkspace {
			row.WorkspaceID = p.WorkspaceID
		}
		if p.Enabled != nil {
			row.Enabled = *p.Enabled
		}
		return nil
	}, func(old, cur store.Preference) error {
		if cur.Text == old.Text {
			return nil
		}
		return s.tombstones.edited(cur.ID, cur.UpdatedAt)
	})
	if errors.Is(err, store.ErrNotFound) {
		return protocol.PreferenceResult{}, notFound(p.ID)
	}
	if cur.ID == "" {
		return protocol.PreferenceResult{}, fmt.Errorf("preference: %w", err)
	}
	if cur.Text != old.Text || cur.WorkspaceID != old.WorkspaceID || cur.Enabled != old.Enabled {
		s.changed(member, cur.ID, "was changed")
	}
	previous := wire(old)
	return protocol.PreferenceResult{Preference: wire(cur), Previous: &previous}, wrap(err)
}

func (s *Service) Delete(ctx context.Context, member domain.MemberID, id string) (protocol.PreferenceDeleteResult, error) {
	if id == "" {
		return protocol.PreferenceDeleteResult{}, invalid("id is required")
	}
	row, err := s.store.DeletePreference(ctx, member, id, func(row store.Preference) error {
		return s.tombstones.deleted(row.ID)
	})
	if errors.Is(err, store.ErrNotFound) {
		return protocol.PreferenceDeleteResult{}, notFound(id)
	}
	if row.ID == "" {
		return protocol.PreferenceDeleteResult{}, fmt.Errorf("preference: %w", err)
	}
	s.changed(member, id, "was forgotten")
	return protocol.PreferenceDeleteResult{Deleted: wire(row)}, wrap(err)
}

// wrap is the error of a change that was made but whose cleanup failed.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("preference: %w", err)
}

// Touch records that the assistant just read the member's preferences ids.
func (s *Service) Touch(ctx context.Context, member domain.MemberID, ids []string) error {
	if err := s.store.TouchPreferences(ctx, member, ids, time.Now()); err != nil {
		return fmt.Errorf("preference: %w", err)
	}
	return nil
}

func (s *Service) changed(member domain.MemberID, id, what string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	note := "preference " + id + " " + what
	if !slices.Contains(s.changes[member], note) {
		s.changes[member] = append(s.changes[member], note)
	}
}

// TakeChanges returns and forgets what changed in the member's preferences
// since the last call, one line per preference.
func (s *Service) TakeChanges(member domain.MemberID) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	notes := s.changes[member]
	delete(s.changes, member)
	return notes
}

// RestoreChanges puts back notes TakeChanges returned that were not
// delivered.
func (s *Service) RestoreChanges(member domain.MemberID, notes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes[member] = append(notes, s.changes[member]...)
}
