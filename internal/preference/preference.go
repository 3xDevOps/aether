// Package preference keeps each member's private assistant preferences:
// short instructions about how they work that they asked the assistant to
// remember. Only the member can read, change or forget theirs.
package preference

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	retry      time.Duration
	scrubTries int
	scrubMu    sync.Mutex
	// scrubbing is set while a worker retries a checkpoint that failed;
	// scrubPending, while one failed since the worker's last attempt began.
	scrubbing, scrubPending bool

	mu      sync.Mutex
	changes map[domain.MemberID][]string
	// told holds the members whose assistant was told of changes since
	// this process started; changes made before it are not known.
	told map[domain.MemberID]bool
}

// restarted is the first note of each member after a start: changes made
// before it were not kept, and the conversation may still hold their old
// text.
const restarted = "the server restarted, so your preferences may have changed since earlier in this conversation; " +
	"use only what aether_my_preferences returns now"

// New serves preferences from st. Deletions and text changes are recorded
// in tombstonePath, which Restore reads.
func New(st Store, tombstonePath string) *Service {
	return &Service{store: st, tombstones: &tombstones{path: tombstonePath}, retry: 5 * time.Second, scrubTries: 120,
		changes: make(map[domain.MemberID][]string), told: make(map[domain.MemberID]bool)}
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

// stale refuses a change to preference id unless it was last updated at
// expected, the time the member saw; a zero time accepts any.
func stale(id string, expected, updated time.Time) error {
	if expected.IsZero() || updated.Equal(expected) {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeConflict, Message: "preference " + id +
		" changed since you saw it, so nothing was changed; show its current text and ask again"}
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
	case len(Words(text)) < MinWords || n < MinChars:
		return "", invalid("a preference needs at least %d words and %d characters", MinWords, MinChars)
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
	if err = s.store.AddPreference(ctx, &row); errors.Is(err, store.ErrPreferenceExists) {
		return protocol.PreferenceResult{}, &protocol.Error{Code: protocol.CodeConflict, Message: "already remembered as preference " + row.ID}
	} else if errors.Is(err, store.ErrPreferenceCap) {
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
		if err := stale(p.ID, p.ExpectedUpdatedAt, row.UpdatedAt); err != nil {
			return err
		}
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
		if err := s.tombstones.settle(old); err != nil {
			return err
		}
		if cur.Text == old.Text {
			return nil
		}
		return s.tombstones.edit(cur.ID, old.UpdatedAt)
	})
	if errors.Is(err, store.ErrNotFound) {
		return protocol.PreferenceResult{}, notFound(p.ID)
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return protocol.PreferenceResult{}, err
	}
	if err != nil {
		return protocol.PreferenceResult{}, fmt.Errorf("preference: %w", err)
	}
	if cur.Text != old.Text || cur.WorkspaceID != old.WorkspaceID || cur.Enabled != old.Enabled {
		s.changed(member, cur.ID, "was changed")
	}
	if cur.Text != old.Text {
		s.scrub()
		if err = s.tombstones.edited(cur.ID, old.UpdatedAt); err != nil {
			return protocol.PreferenceResult{}, fmt.Errorf("preference %s: the text changed, but until the server restarts "+
				"a restored older database could bring the old text back: %w", cur.ID, err)
		}
	}
	previous := wire(old)
	return protocol.PreferenceResult{Preference: wire(cur), Previous: &previous}, nil
}

// Delete forgets the member's preference p.ID.
func (s *Service) Delete(ctx context.Context, member domain.MemberID, p protocol.PreferenceDeleteParams) (protocol.PreferenceDeleteResult, error) {
	id := p.ID
	if id == "" {
		return protocol.PreferenceDeleteResult{}, invalid("id is required")
	}
	// Once the tombstone is synced the delete must commit: a cancelled
	// caller would leave a tombstone for a delete it was told failed.
	row, err := s.store.DeletePreference(context.WithoutCancel(ctx), member, id, func(row store.Preference) error {
		if err := stale(id, p.ExpectedUpdatedAt, row.UpdatedAt); err != nil {
			return err
		}
		return s.tombstones.deleted(row.ID)
	})
	if errors.Is(err, store.ErrNotFound) {
		return protocol.PreferenceDeleteResult{}, notFound(id)
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return protocol.PreferenceDeleteResult{}, err
	}
	if err != nil {
		return protocol.PreferenceDeleteResult{}, fmt.Errorf("preference: %w", err)
	}
	s.scrub()
	s.changed(member, id, "was forgotten")
	return protocol.PreferenceDeleteResult{Deleted: wire(row)}, nil
}

// DeleteMember deletes the member and their preferences in one
// transaction, recording each preference as deleted before it commits, so
// that no restored copy of the database brings them back.
func (s *Service) DeleteMember(ctx context.Context, member domain.MemberID) error {
	// Once the tombstones are synced the delete must commit, as in Delete.
	err := s.store.DeleteMemberWithPreferences(context.WithoutCancel(ctx), member, func(ids []string) error {
		return s.tombstones.deleted(ids...)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return err
		}
		return fmt.Errorf("preference: %w", err)
	}
	s.scrub()
	return nil
}

// scrub clears deleted or replaced text from the write-ahead log. The
// change is done either way: while a reader holds the log, a single
// worker retries until a checkpoint succeeds after the last one that
// failed, or scrubTries ran out.
func (s *Service) scrub() {
	err := s.store.CheckpointWAL(context.Background())
	if err == nil {
		return
	}
	s.scrubMu.Lock()
	defer s.scrubMu.Unlock()
	s.scrubPending = true
	if s.scrubbing {
		return
	}
	s.scrubbing = true
	slog.Warn("preference: clear the write-ahead log, retrying", "error", err)
	go s.retryScrub()
}

func (s *Service) retryScrub() {
	for try := 1; ; try++ {
		time.Sleep(s.retry)
		s.scrubMu.Lock()
		s.scrubPending = false
		s.scrubMu.Unlock()
		err := s.store.CheckpointWAL(context.Background())
		s.scrubMu.Lock()
		if err != nil {
			s.scrubPending = true
		}
		if !s.scrubPending || try >= s.scrubTries {
			s.scrubbing = false
			s.scrubMu.Unlock()
			if err != nil {
				slog.Warn("preference: clear the write-ahead log; the next forget or edit tries again", "error", err)
			}
			return
		}
		s.scrubMu.Unlock()
	}
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
	note := "preference " + id + " " + what + "; do not use the old text"
	if !slices.Contains(s.changes[member], note) {
		s.changes[member] = append(s.changes[member], note)
	}
}

// TakeChanges returns and forgets what changed in the member's preferences
// since the last call, one line per preference, for the assistant's next
// prompt. The first call since the server started also says it restarted,
// to a member who keeps preferences and lets the assistant use them.
func (s *Service) TakeChanges(ctx context.Context, member domain.MemberID) []string {
	s.mu.Lock()
	notes := s.changes[member]
	delete(s.changes, member)
	first := !s.told[member]
	s.told[member] = true
	s.mu.Unlock()
	if first && s.keeps(ctx, member) {
		notes = append([]string{restarted}, notes...)
	}
	return notes
}

// keeps reports whether member lets the assistant use preferences and has
// one; a failed read counts as yes.
func (s *Service) keeps(ctx context.Context, member domain.MemberID) bool {
	enabled, err := s.Enabled(ctx, member)
	if err != nil {
		return true
	}
	if !enabled {
		return false
	}
	rows, err := s.store.ListPreferences(ctx, member)
	return err != nil || len(rows) > 0
}

// RestoreChanges puts back notes TakeChanges returned that were not
// delivered.
func (s *Service) RestoreChanges(member domain.MemberID, notes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(notes) > 0 && notes[0] == restarted {
		s.told[member], notes = false, notes[1:]
	}
	s.changes[member] = append(slices.Clone(notes), s.changes[member]...)
}
