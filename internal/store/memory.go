package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/secretscan"
)

type MemoryKind string

const (
	MemoryDecision   MemoryKind = "decision"
	MemoryConvention MemoryKind = "convention"
	MemoryGotcha     MemoryKind = "gotcha"
	MemoryPreference MemoryKind = "preference"
	MemoryOutcome    MemoryKind = "outcome"
)

// MemoryScope selects whose briefs a pinned note enters: every member's,
// or only the pinning member's. Search sees every note either way.
type MemoryScope string

const (
	MemoryScopeWorkspace MemoryScope = "workspace"
	MemoryScopeMember    MemoryScope = "member"
)

type MemorySource string

const (
	MemoryFromAgent  MemorySource = "agent"
	MemoryFromMember MemorySource = "member"
	MemoryFromReport MemorySource = "report"
	MemoryFromAnswer MemorySource = "answer"
)

// A pin costs its brief line: its title plus MemoryPinLineOverhead, which
// covers "- note-xxxxxxxxxx preference (<name>): " with the name cut to
// MemoryPinNameBytes, and the newline. Pins within their budgets therefore
// always fit in the brief.
const (
	MemoryMaxTitleBytes     = 120
	MemoryMaxFiles          = 20
	MemoryPinWorkspaceBytes = 1 << 10
	MemoryPinMemberBytes    = 512
	MemoryPinNameBytes      = 24
	MemoryPinLineOverhead   = 64
)

// Memory is one team-memory note. AuthorName, PinnedByName and the PR
// fields are read-only joins: the members' display names and the latest
// pull request of the note's run.
type Memory struct {
	ID           string
	WorkspaceID  domain.WorkspaceID
	Kind         MemoryKind
	Scope        MemoryScope
	Source       MemorySource
	SourceRef    string
	Title        string
	Body         string
	Files        []string
	AuthorID     domain.MemberID
	AuthorName   string
	RunID        domain.RunID
	Harness      string
	Commit       string
	PRNumber     int
	PRState      string
	PRURL        string
	SupersededBy string
	InvalidAt    *time.Time
	PinnedBy     domain.MemberID
	PinnedByName string
	PinnedAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Live reports whether the note is neither replaced nor invalidated.
func (m *Memory) Live() bool { return m.InvalidAt == nil && m.SupersededBy == "" }

// MemoryPinBudgetError refuses a pin that would push the pinned notes of
// its scope past their byte budget.
type MemoryPinBudgetError struct {
	Scope MemoryScope
	Used  int
	Limit int
	Need  int
}

func (e *MemoryPinBudgetError) Error() string {
	whose := "pinned workspace notes"
	if e.Scope == MemoryScopeMember {
		whose = "your pinned member notes"
	}
	return fmt.Sprintf("%s use %d of %d bytes; this note needs %d; unpin one first", whose, e.Used, e.Limit, e.Need)
}

const memoryCols = `m.id, m.workspace_id, m.kind, m.scope, m.source, m.source_ref, m.title, m.body, m.files,
	m.author_member_id, COALESCE(author.display_name, ''), m.run_id, m.harness, m.commit_sha,
	COALESCE(pr.number, 0), COALESCE(pr.state, ''), COALESCE(pr.url, ''), m.superseded_by, m.invalid_at,
	m.pinned_by, COALESCE(pinner.display_name, ''), m.pinned_at, m.created_at, m.updated_at`

const memoryJoins = `
	LEFT JOIN members author ON author.id = m.author_member_id
	LEFT JOIN members pinner ON pinner.id = m.pinned_by
	LEFT JOIN run_pull_requests pr ON pr.rowid = (
		SELECT rowid FROM run_pull_requests WHERE run_id = m.run_id ORDER BY created_at DESC, rowid DESC LIMIT 1)`

const memoryLive = `m.invalid_at IS NULL AND m.superseded_by = ''`

func scanMemory(row interface{ Scan(...any) error }) (*Memory, error) {
	var (
		m                Memory
		files            string
		invalid, pinned  *int64
		created, updated int64
	)
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.Kind, &m.Scope, &m.Source, &m.SourceRef, &m.Title, &m.Body, &files,
		&m.AuthorID, &m.AuthorName, &m.RunID, &m.Harness, &m.Commit, &m.PRNumber, &m.PRState, &m.PRURL,
		&m.SupersededBy, &invalid, &m.PinnedBy, &m.PinnedByName, &pinned, &created, &updated); err != nil {
		return nil, err
	}
	if files != "" {
		m.Files = strings.Split(files, "\n")
	}
	m.InvalidAt, m.PinnedAt = decodeTimePtr(invalid), decodeTimePtr(pinned)
	m.CreatedAt, m.UpdatedAt = decodeTime(created), decodeTime(updated)
	return &m, nil
}

type memoryQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readMemory(ctx context.Context, q memoryQuerier, workspace domain.WorkspaceID, id string) (*Memory, error) {
	m, err := scanMemory(q.QueryRowContext(ctx,
		`SELECT `+memoryCols+` FROM memory m`+memoryJoins+` WHERE m.workspace_id = ? AND m.id = ?`, workspace, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: memory note %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read memory note %s: %w", id, err)
	}
	return m, nil
}

// memoryHash ignores case and whitespace, so a note retyped with another
// line wrap is still a duplicate.
func memoryHash(body string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(strings.ToLower(body)), " ")))
	return hex.EncodeToString(sum[:])
}

// memoryTitle is text's first non-empty line, cut to MemoryMaxTitleBytes
// on a rune boundary.
func memoryTitle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if len(line) <= MemoryMaxTitleBytes {
		return line
	}
	cut := MemoryMaxTitleBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut]
}

// InsertMemory stores m and reads it back into m. When a live agent or
// member note already has the same text, or the question already has an
// answer note, it stores nothing, reads that note into m and reports true.
func (d *DB) InsertMemory(ctx context.Context, m *Memory) (bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: insert memory: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	duplicate, err := insertMemory(ctx, tx, m)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: insert memory: commit: %w", err)
	}
	return duplicate, nil
}

// SupersedeMemory stores m as the replacement for the live note old in one
// transaction. vet, when set, marks an agent's replacement: it may refuse
// old as the transaction reads it, and a replacement whose text another
// live note holds is refused, since it would hide old without writing a
// note the run's limit counts. old is invalidated before the insert, so m
// may repeat its text; when m duplicates another live note, old points at
// that note. A pin on old moves to the replacement, pinned in old's scope
// by the author m arrived with, even when the replacement is someone
// else's duplicate, and must fit that scope's budget without old. It
// reports whether m duplicated a live note and whether old's pin moved.
func (d *DB) SupersedeMemory(ctx context.Context, old string, m *Memory, vet func(old *Memory) error) (duplicate, movedPin bool, err error) {
	if m == nil {
		return false, false, errors.New("store: supersede memory: note is required")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("store: supersede memory: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prior, err := readMemory(ctx, tx, m.WorkspaceID, old)
	if err != nil {
		return false, false, err
	}
	if !prior.Live() {
		return false, false, fmt.Errorf("%w: memory note %s is already replaced", ErrConflict, old)
	}
	if vet != nil {
		if err = vet(prior); err != nil {
			return false, false, err
		}
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return false, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory SET invalid_at = ?, pinned_by = '', pinned_at = NULL, updated_at = ? WHERE id = ?`,
		now, now, old); err != nil {
		return false, false, fmt.Errorf("store: supersede memory %s: %w", old, err)
	}
	by := m.AuthorID
	if duplicate, err = insertMemory(ctx, tx, m); err != nil {
		return false, false, err
	}
	if duplicate && vet != nil {
		return false, false, fmt.Errorf("%w: memory note %s already holds this text; ask a member to replace %s with it (aether memory add --supersedes %s)",
			ErrConflict, m.ID, old, old)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory SET superseded_by = ? WHERE id = ?`, m.ID, old); err != nil {
		return false, false, fmt.Errorf("store: supersede memory %s: %w", old, err)
	}
	if prior.PinnedAt != nil {
		if err = pinMemory(ctx, tx, m, by, prior.Scope); err != nil {
			return false, false, fmt.Errorf("move the pin on %s to its replacement: %w", old, err)
		}
		var pinned *Memory
		if pinned, err = readMemory(ctx, tx, m.WorkspaceID, m.ID); err != nil {
			return false, false, err
		}
		*m = *pinned
	}
	if err = tx.Commit(); err != nil {
		return false, false, fmt.Errorf("store: supersede memory: commit: %w", err)
	}
	return duplicate, prior.PinnedAt != nil, nil
}

func insertMemory(ctx context.Context, tx *sql.Tx, m *Memory) (bool, error) {
	if m == nil || m.WorkspaceID == "" || strings.TrimSpace(m.Body) == "" {
		return false, errors.New("store: insert memory: workspace and body are required")
	}
	id, ts, err := prepareCreate("note", m.CreatedAt)
	if err != nil {
		return false, err
	}
	created, err := encodeTime(ts)
	if err != nil {
		return false, fmt.Errorf("store: insert memory: %w", err)
	}
	title, scope, hash := m.Title, m.Scope, memoryHash(m.Body)
	if title == "" {
		title = memoryTitle(m.Body)
	}
	if scope == "" {
		scope = MemoryScopeWorkspace
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO memory (id, workspace_id, kind, scope, source, source_ref, title, body, files,
			author_member_id, run_id, harness, commit_sha, hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		id, m.WorkspaceID, m.Kind, scope, m.Source, m.SourceRef, title, m.Body, strings.Join(m.Files, "\n"),
		m.AuthorID, m.RunID, m.Harness, m.Commit, hash, created, created)
	if err != nil {
		return false, fmt.Errorf("store: insert memory: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: insert memory: %w", err)
	}
	if inserted == 0 {
		existing := `source IN ('agent', 'member') AND workspace_id = ? AND hash = ? AND invalid_at IS NULL`
		args := []any{m.WorkspaceID, hash}
		if m.Source == MemoryFromAnswer {
			existing, args = `source = 'answer' AND source_ref = ?`, []any{m.SourceRef}
		}
		if err = tx.QueryRowContext(ctx, `SELECT id FROM memory WHERE `+existing, args...).Scan(&id); err != nil {
			return false, fmt.Errorf("store: insert memory: read the note it duplicates: %w", err)
		}
	}
	stored, err := readMemory(ctx, tx, m.WorkspaceID, id)
	if err != nil {
		return false, err
	}
	*m = *stored
	return inserted == 0, nil
}

// MemoryQuery selects notes. Words match any word in titles, bodies and
// files, titles weighing most; File keeps notes citing that path. Without
// either the newest notes come first. All includes replaced notes, and
// Replaced keeps only them.
type MemoryQuery struct {
	Workspace domain.WorkspaceID
	Words     string
	Kind      MemoryKind
	Source    MemorySource
	File      string
	All       bool
	Replaced  bool
	Pinned    bool
	Limit     int
}

// SearchMemory ranks matches by bm25 decayed by age: a 30-day-old note
// scores half of a new one.
func (d *DB) SearchMemory(ctx context.Context, q MemoryQuery) ([]*Memory, error) {
	if q.Workspace == "" || q.Limit <= 0 {
		return nil, errors.New("store: search memory: workspace and a positive limit are required")
	}
	where, args := "m.workspace_id = ?", []any{q.Workspace}
	if q.Kind != "" {
		where += " AND m.kind = ?"
		args = append(args, q.Kind)
	}
	if q.Source != "" {
		where += " AND m.source = ?"
		args = append(args, q.Source)
	}
	switch {
	case q.Replaced:
		where += " AND NOT (" + memoryLive + ")"
	case !q.All:
		where += " AND " + memoryLive
	}
	if q.Pinned {
		where += " AND m.pinned_at IS NOT NULL"
	}
	var match []string
	if q.File != "" {
		match = append(match, "files : "+ftsPhrase(q.File))
	}
	var words []string
	for _, word := range strings.Fields(q.Words) {
		words = append(words, ftsPhrase(word))
	}
	if len(words) > 0 {
		match = append(match, "("+strings.Join(words, " OR ")+")")
	}
	query := `SELECT ` + memoryCols + ` FROM memory m` + memoryJoins + ` WHERE ` + where +
		` ORDER BY m.created_at DESC, m.rowid DESC LIMIT ?`
	if len(match) > 0 {
		query = `SELECT ` + memoryCols + ` FROM memory_fts JOIN memory m ON m.rowid = memory_fts.rowid` + memoryJoins +
			` WHERE memory_fts MATCH ? AND ` + where +
			` ORDER BY bm25(memory_fts, 4.0, 1.0, 2.0) / (1 + (? - m.created_at) / 86400e9 / 30), m.created_at DESC LIMIT ?`
		args = append([]any{strings.Join(match, " AND ")}, args...)
		args = append(args, time.Now().UnixNano())
	}
	rows, err := d.db.QueryContext(ctx, query, append(args, q.Limit)...)
	if err != nil {
		return nil, fmt.Errorf("store: search memory: %w", err)
	}
	return collect(rows, scanMemory)
}

// ftsPhrase quotes s as one FTS5 phrase, so no input is read as query
// syntax.
func ftsPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// GetMemory returns the note and every note in its supersede chain other
// than itself, oldest first: the notes it replaced, and those that
// replaced it.
func (d *DB) GetMemory(ctx context.Context, workspace domain.WorkspaceID, id string) (*Memory, []*Memory, error) {
	m, err := readMemory(ctx, d.db, workspace, id)
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.db.QueryContext(ctx, `WITH RECURSIVE
		newer(id) AS (
			SELECT superseded_by FROM memory WHERE workspace_id = ?1 AND id = ?2 AND superseded_by <> ''
			UNION SELECT m.superseded_by FROM memory m JOIN newer n ON m.id = n.id WHERE m.superseded_by <> ''),
		older(id) AS (
			SELECT id FROM memory WHERE workspace_id = ?1 AND superseded_by = ?2
			UNION SELECT m.id FROM memory m JOIN older o ON m.superseded_by = o.id)
		SELECT `+memoryCols+` FROM memory m`+memoryJoins+`
		WHERE m.workspace_id = ?1 AND m.id IN (SELECT id FROM newer UNION SELECT id FROM older)
		ORDER BY m.created_at, m.rowid`, workspace, id)
	if err != nil {
		return nil, nil, fmt.Errorf("store: read memory chain of %s: %w", id, err)
	}
	chain, err := collect(rows, scanMemory)
	if err != nil {
		return nil, nil, err
	}
	return m, chain, nil
}

// PinMemory pins a live note into the briefs its scope selects; an empty
// scope keeps the note's own. Workspace pins share
// MemoryPinWorkspaceBytes of brief lines, and each member's member-scope
// pins MemoryPinMemberBytes.
func (d *DB) PinMemory(ctx context.Context, workspace domain.WorkspaceID, id string, by domain.MemberID, scope MemoryScope) (*Memory, error) {
	if by == "" {
		return nil, errors.New("store: pin memory: member is required")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: pin memory: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := readMemory(ctx, tx, workspace, id)
	if err != nil {
		return nil, err
	}
	if !m.Live() {
		return nil, fmt.Errorf("%w: memory note %s is replaced; pin the note that replaced it", ErrConflict, id)
	}
	if scope == "" {
		scope = m.Scope
	}
	if err = pinMemory(ctx, tx, m, by, scope); err != nil {
		return nil, err
	}
	if m, err = readMemory(ctx, tx, workspace, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: pin memory: commit: %w", err)
	}
	return m, nil
}

// pinMemory pins m by member by in scope, refusing a pin the other live
// pins of that scope leave no room for.
func pinMemory(ctx context.Context, tx *sql.Tx, m *Memory, by domain.MemberID, scope MemoryScope) error {
	budget := &MemoryPinBudgetError{Scope: scope, Limit: MemoryPinWorkspaceBytes, Need: len(m.Title) + MemoryPinLineOverhead}
	pinned, args := `m.scope = 'workspace'`, []any{m.WorkspaceID, m.ID}
	if scope == MemoryScopeMember {
		budget.Limit = MemoryPinMemberBytes
		pinned = `m.scope = 'member' AND m.pinned_by = ?`
		args = append(args, by)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(m.title AS BLOB)) + ?), 0) FROM memory m
		WHERE m.workspace_id = ? AND m.id <> ? AND m.pinned_at IS NOT NULL AND `+memoryLive+` AND `+pinned,
		append([]any{MemoryPinLineOverhead}, args...)...).Scan(&budget.Used); err != nil {
		return fmt.Errorf("store: pin memory: measure pins: %w", err)
	}
	if budget.Used+budget.Need > budget.Limit {
		return budget
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory SET scope = ?, pinned_by = ?, pinned_at = ?, updated_at = ? WHERE id = ?`,
		scope, by, now, now, m.ID); err != nil {
		return fmt.Errorf("store: pin memory %s: %w", m.ID, err)
	}
	return nil
}

func (d *DB) UnpinMemory(ctx context.Context, workspace domain.WorkspaceID, id string) (*Memory, error) {
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE memory SET pinned_by = '', pinned_at = NULL, updated_at = ?
		 WHERE workspace_id = ? AND id = ? AND pinned_at IS NOT NULL`, now, workspace, id); err != nil {
		return nil, fmt.Errorf("store: unpin memory %s: %w", id, err)
	}
	return readMemory(ctx, d.db, workspace, id)
}

func (d *DB) DeleteMemory(ctx context.Context, workspace domain.WorkspaceID, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete memory %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := deleteMemory(ctx, tx, `workspace_id = ? AND id = ?`, workspace, id)
	if err != nil {
		return fmt.Errorf("store: delete memory %s: %w", id, err)
	}
	if len(ids) == 0 {
		return fmt.Errorf("%w: memory note %s", ErrNotFound, id)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: delete memory %s: commit: %w", id, err)
	}
	return nil
}

// DeleteMemoryByRun deletes every note attributed to run, pinned or not,
// and returns their IDs in order.
func (d *DB) DeleteMemoryByRun(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) ([]string, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: delete memory of run %s: begin: %w", run, err)
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := deleteMemory(ctx, tx, `workspace_id = ? AND run_id = ? AND run_id <> ''`, workspace, run)
	if err != nil {
		return nil, fmt.Errorf("store: delete memory of run %s: %w", run, err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: delete memory of run %s: commit: %w", run, err)
	}
	return ids, nil
}

// deleteMemory deletes the notes where selects and returns their IDs in
// order. A note one of them replaced takes its place in the supersede
// chain: the next surviving note replaces it, or with none it is live
// again. OR IGNORE leaves it replaced when a live note already holds its
// text or, for an outcome note, its run has a newer one.
func deleteMemory(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM memory WHERE `+where+` RETURNING id, superseded_by`, args...)
	if err != nil {
		return nil, err
	}
	type deleted struct{ id, replacedBy string }
	notes, err := collect(rows, func(row interface{ Scan(...any) error }) (*deleted, error) {
		var n deleted
		return &n, row.Scan(&n.id, &n.replacedBy)
	})
	if err != nil {
		return nil, err
	}
	replacedBy := make(map[string]string, len(notes))
	ids := make([]string, 0, len(notes))
	for _, n := range notes {
		replacedBy[n.id] = n.replacedBy
		ids = append(ids, n.id)
	}
	slices.Sort(ids)
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		next := replacedBy[id]
		for later, gone := replacedBy[next]; gone; later, gone = replacedBy[next] {
			next = later
		}
		restore, restoreArgs := `UPDATE OR IGNORE memory SET invalid_at = NULL, superseded_by = '', updated_at = ? WHERE superseded_by = ?`, []any{now, id}
		if next != "" {
			restore, restoreArgs = `UPDATE memory SET superseded_by = ?, updated_at = ? WHERE superseded_by = ?`, []any{next, now, id}
		}
		if _, err = tx.ExecContext(ctx, restore, restoreArgs...); err != nil {
			return nil, fmt.Errorf("restore the notes %s replaced: %w", id, err)
		}
	}
	return ids, nil
}

// CountAgentNotesByRun counts every note run's agent wrote, replaced or
// not.
func (d *DB) CountAgentNotesByRun(ctx context.Context, run domain.RunID) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory WHERE run_id = ? AND source = 'agent'`, run).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count agent notes of run %s: %w", run, err)
	}
	return n, nil
}

// MemoryBriefQuery selects a member's brief inputs. Self, the run the
// brief is for, is left out of the live-run count.
type MemoryBriefQuery struct {
	Workspace domain.WorkspaceID
	Member    domain.MemberID
	Self      domain.RunID
	Since     time.Time
	Runs      int
}

// MemoryBrief is what a launch brief is built from. Pinned holds the
// workspace pins, then the member's own; Notes counts live notes and
// AgentNotes the unpinned ones agents wrote.
type MemoryBrief struct {
	Pinned     []*Memory
	Runs       []*MemoryBriefRun
	LiveRuns   int
	Notes      int
	AgentNotes int
}

// MemoryBriefRun is a run that finished since the query's Since. Outcome
// is its standing success or failure report, empty without one. Task is
// the first line of its task, or of its mission's objective for a swarm
// run.
type MemoryBriefRun struct {
	ID         domain.RunID
	MemberName string
	Harness    string
	Status     domain.RunStatus
	Outcome    CoordOutcome
	Branch     string
	PRNumber   int
	PRState    string
	FinishedAt time.Time
	Task       string
}

func (d *DB) ReadMemoryBrief(ctx context.Context, q MemoryBriefQuery) (*MemoryBrief, error) {
	since, err := encodeTime(q.Since)
	if err != nil {
		return nil, fmt.Errorf("store: read memory brief: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: read memory brief: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var brief MemoryBrief
	rows, err := tx.QueryContext(ctx, `SELECT `+memoryCols+` FROM memory m`+memoryJoins+`
		WHERE m.workspace_id = ? AND m.pinned_at IS NOT NULL AND `+memoryLive+`
		  AND (m.scope = 'workspace' OR m.pinned_by = ?)
		ORDER BY m.scope = 'member', m.pinned_at, m.rowid`, q.Workspace, q.Member)
	if err != nil {
		return nil, fmt.Errorf("store: read memory brief: pins: %w", err)
	}
	if brief.Pinned, err = collect(rows, scanMemory); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT r.id, COALESCE(member.display_name, ''), r.harness, r.status,
			COALESCE((SELECT outcome FROM coord_reports WHERE run_id = r.id AND state = 'finalized'
				AND outcome IN ('success', 'failure') AND superseded_at IS NULL ORDER BY created_at DESC LIMIT 1), ''),
			r.branch, COALESCE(pr.number, 0), COALESCE(pr.state, ''), r.finished_at,
			COALESCE((SELECT objective FROM missions WHERE id IN (
				SELECT id FROM missions WHERE current_integrator_run_id = r.id
				UNION SELECT mission_id FROM mission_attempts WHERE run_id = r.id
				UNION SELECT mission_id FROM mission_integrator_replacements WHERE run_id = r.id
				UNION SELECT mission_id FROM mission_create_receipts WHERE initial_run_id = r.id) LIMIT 1), r.task)
		FROM runs r
		LEFT JOIN members member ON member.id = r.member_id
		LEFT JOIN run_pull_requests pr ON pr.rowid = (
			SELECT rowid FROM run_pull_requests WHERE run_id = r.id ORDER BY created_at DESC, rowid DESC LIMIT 1)
		WHERE r.workspace_id = ? AND r.finished_at >= ?
		ORDER BY r.finished_at DESC, r.rowid DESC LIMIT ?`, q.Workspace, since, q.Runs)
	if err != nil {
		return nil, fmt.Errorf("store: read memory brief: runs: %w", err)
	}
	brief.Runs, err = collect(rows, func(row interface{ Scan(...any) error }) (*MemoryBriefRun, error) {
		var r MemoryBriefRun
		var finished int64
		if scanErr := row.Scan(&r.ID, &r.MemberName, &r.Harness, &r.Status, &r.Outcome, &r.Branch,
			&r.PRNumber, &r.PRState, &finished, &r.Task); scanErr != nil {
			return nil, scanErr
		}
		r.FinishedAt = decodeTime(finished)
		r.Task, _, _ = strings.Cut(strings.TrimSpace(r.Task), "\n")
		r.Task = strings.TrimSpace(r.Task)
		return &r, nil
	})
	if err != nil {
		return nil, err
	}
	placeholders, statuses := liveStatusArgs()
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE workspace_id = ? AND id <> ? AND status IN (`+placeholders+`)`,
		append([]any{q.Workspace, q.Self}, statuses...)...).Scan(&brief.LiveRuns); err != nil {
		return nil, fmt.Errorf("store: read memory brief: live runs: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE m.source = 'agent' AND m.pinned_at IS NULL)
		FROM memory m WHERE m.workspace_id = ? AND `+memoryLive, q.Workspace).Scan(&brief.Notes, &brief.AgentNotes); err != nil {
		return nil, fmt.Errorf("store: read memory brief: notes: %w", err)
	}
	return &brief, nil
}

// withholdSecret scans text and the files it cites together, as memory.add
// does. On a hit it returns the rule that caught it as the text and no
// files, so a captured note never stores a secret.
func withholdSecret(text string, files []string) (string, []string) {
	hits := secretscan.Scan("", []byte(strings.Join(append([]string{text}, files...), "\n")))
	if len(hits) == 0 {
		return text, files
	}
	return fmt.Sprintf("(withheld: gitleaks rule %s at %s)", hits[0].Kind, hits[0].Location), nil
}

// latestRunDiff reads the files and HEAD of the run's newest run.diff
// event. It decodes the payload itself because the events package imports
// this one.
func (d *DB) latestRunDiff(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) ([]string, string, error) {
	var payload string
	err := d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE workspace_id = ? AND run_id = ? AND type = 'run.diff' ORDER BY seq DESC LIMIT 1`,
		workspace, run).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the latest diff of run %s: %w", run, err)
	}
	var diff struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Head string `json:"head"`
	}
	if err := json.Unmarshal([]byte(payload), &diff); err != nil {
		return nil, "", fmt.Errorf("decode the latest diff of run %s: %w", run, err)
	}
	var files []string
	for _, f := range diff.Files[:min(len(diff.Files), MemoryMaxFiles)] {
		files = append(files, f.Path)
	}
	return files, diff.Head, nil
}

// reportMemory prepares the outcome note a success or failure report
// writes when it is finalized; a blocked report writes none.
func (d *DB) reportMemory(ctx context.Context, report *CoordReport) (*Memory, error) {
	if report.Outcome != CoordOutcomeSuccess && report.Outcome != CoordOutcomeFailure {
		return nil, nil
	}
	body := report.Summary
	if report.NextAction != "" {
		body += "\n\nNext action: " + report.NextAction
	}
	files, head, err := d.latestRunDiff(ctx, report.WorkspaceID, report.RunID)
	if err != nil {
		return nil, err
	}
	body, files = withholdSecret(body, files)
	return &Memory{
		WorkspaceID: report.WorkspaceID, Kind: MemoryOutcome, Scope: MemoryScopeWorkspace,
		Source: MemoryFromReport, SourceRef: report.ID, Title: memoryTitle(string(report.Outcome) + ": " + memoryTitle(body)),
		Body: body, Files: files, RunID: report.RunID, Commit: head,
	}, nil
}

// upsertReportMemory writes the run's outcome note, attributed to the run's
// owner and harness, or rewrites the live one an earlier report wrote. A
// rewrite clears the note's pin, since no member has read the new text, and
// returns the note's ID when it was pinned.
func upsertReportMemory(ctx context.Context, tx *sql.Tx, m *Memory) (string, error) {
	var unpinned string
	err := tx.QueryRowContext(ctx, `SELECT id FROM memory
		WHERE run_id = ? AND source = 'report' AND invalid_at IS NULL AND pinned_at IS NOT NULL`, m.RunID).Scan(&unpinned)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: record run %s outcome in memory: %w", m.RunID, err)
	}
	id, ts, err := prepareCreate("note", time.Time{})
	if err != nil {
		return "", err
	}
	now, err := encodeTime(ts)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory (id, workspace_id, kind, scope, source, source_ref, title, body, files,
			author_member_id, run_id, harness, commit_sha, hash, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, r.member_id, r.id, r.harness, ?, ?, ?, ? FROM runs r WHERE r.id = ?
		ON CONFLICT (run_id) WHERE source = 'report' AND invalid_at IS NULL DO UPDATE SET
			source_ref = excluded.source_ref, title = excluded.title, body = excluded.body, files = excluded.files,
			author_member_id = excluded.author_member_id, harness = excluded.harness,
			commit_sha = excluded.commit_sha, hash = excluded.hash, pinned_by = '', pinned_at = NULL,
			updated_at = excluded.updated_at`,
		id, m.WorkspaceID, m.Kind, m.Scope, m.Source, m.SourceRef, m.Title, m.Body, strings.Join(m.Files, "\n"),
		m.Commit, memoryHash(m.Body), now, now, m.RunID); err != nil {
		return "", fmt.Errorf("store: record run %s outcome in memory: %w", m.RunID, err)
	}
	return unpinned, nil
}

// answerMemory prepares the decision note an answer to a mission question
// writes, attributed to the answering member and the asking run.
func (d *DB) answerMemory(ctx context.Context, questionID domain.MissionQuestionID, member domain.MemberID, answer string) (*Memory, error) {
	q, err := d.GetMissionQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	body, _ := withholdSecret("Q: "+q.Body+" A: "+answer, nil)
	return &Memory{
		Kind: MemoryDecision, Scope: MemoryScopeWorkspace, Source: MemoryFromAnswer, SourceRef: string(questionID),
		Body: body, AuthorID: member, RunID: q.AskedByRunID,
	}, nil
}
