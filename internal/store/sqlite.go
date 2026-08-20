package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"

	sqlite "modernc.org/sqlite" // pure-Go sqlite driver
	sqlite3 "modernc.org/sqlite/lib"
)

// DB is the SQLite-backed Store implementation.
type DB struct {
	db *sql.DB
}

var _ Store = (*DB)(nil)

// Open opens (creating if necessary) the SQLite database at path, enables
// foreign keys and WAL, and applies any pending migrations. The file is
// created at 0600 before SQLite sees it, because SQLite copies the main
// database file's mode onto the -wal and -shm sidecars it creates while
// applying the journal_mode pragma.
func Open(path string) (*DB, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create %s: %w", path, err)
	}
	if closeErr := f.Close(); closeErr != nil {
		return nil, fmt.Errorf("store: create %s: %w", path, closeErr)
	}
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close() //nolint:errcheck // migrate error takes precedence
		return nil, err
	}
	return &DB{db: db}, nil
}

// Close closes the underlying database.
func (d *DB) Close() error {
	return d.db.Close()
}

// prepareCreate generates a fresh ID and defaults a zero createdAt to now,
// returning the values to persist.
func prepareCreate(createdAt time.Time) (id string, ts time.Time, err error) {
	id, err = newID()
	if err != nil {
		return "", time.Time{}, err
	}
	ts = createdAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return id, ts, nil
}

// encodeTime converts t to Unix nanoseconds, rejecting instants outside
// the int64 range (roughly years 1678-2262) — notably the zero time.Time —
// instead of silently persisting an overflowed value.
func encodeTime(t time.Time) (int64, error) {
	n := t.UnixNano()
	if !time.Unix(0, n).Equal(t) {
		return 0, fmt.Errorf("time %v is outside the storable range", t)
	}
	return n, nil
}

func decodeTime(n int64) time.Time { return time.Unix(0, n).UTC() }

func encodeTimePtr(t *time.Time) (*int64, error) {
	if t == nil {
		return nil, nil
	}
	n, err := encodeTime(*t)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func decodeTimePtr(n *int64) *time.Time {
	if n == nil {
		return nil
	}
	t := decodeTime(*n)
	return &t
}

// mapConstraint converts SQLite constraint violations into the store's
// sentinel errors: UNIQUE/PRIMARY KEY becomes ErrConflict, FOREIGN KEY
// becomes fkErr (ErrNotFound when a write references a missing parent,
// ErrInUse when a delete is blocked by dependents). Other errors pass
// through unchanged.
func mapConstraint(err, fkErr error) error {
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %v", ErrConflict, err)
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%w: %v", fkErr, err)
		}
	}
	return err
}

// normalizePublicKey canonicalizes an authorized_keys line to
// "type base64", stripping options, comment, and surrounding whitespace,
// so key equality (and the UNIQUE index) follows the physical key.
func normalizePublicKey(s string) (string, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil {
		return "", fmt.Errorf("invalid public key: %w", err)
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pk)), "\n"), nil
}

// execDelete runs a delete statement, mapping FK-blocked deletes to
// ErrInUse and zero affected rows to ErrNotFound.
func (d *DB) execDelete(ctx context.Context, op, query string, id any) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx, query, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: %s: %w", op, mapConstraint(err, ErrInUse))
	}
	return err
}

// notFoundOnZeroRows converts an exec result affecting zero rows into
// ErrNotFound.
func notFoundOnZeroRows(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Workspaces

func normalizeWorkspaceEnvironment(e domain.WorkspaceEnvironment) (domain.WorkspaceEnvironment, error) {
	if e.CustomImage == "" && !e.NeutralImage {
		e.NeutralImage = true
	}
	if !e.Valid() {
		return domain.WorkspaceEnvironment{}, fmt.Errorf("store: invalid workspace environment")
	}
	return e, nil
}

func (d *DB) CreateWorkspace(ctx context.Context, w *domain.Workspace) error {
	id, ts, err := prepareCreate(w.CreatedAt)
	if err != nil {
		return err
	}
	envDef, err := normalizeWorkspaceEnvironment(w.Environment)
	if err != nil {
		return err
	}
	environment, err := json.Marshal(envDef)
	if err != nil {
		return fmt.Errorf("store: encode workspace environment: %w", err)
	}
	variables, err := json.Marshal(envDef.Variables)
	if err != nil {
		return fmt.Errorf("store: encode workspace variables: %w", err)
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create workspace: %w", err)
	}
	image := envDef.CustomImage
	if envDef.NeutralImage {
		image = ""
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, image, env, setup_script, created_at, environment)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, w.Name, image, string(variables), envDef.SetupPolicy.Script, createdAt, string(environment),
	); err != nil {
		return fmt.Errorf("store: create workspace: %w", mapConstraint(err, ErrNotFound))
	}
	w.ID, w.CreatedAt, w.Environment = domain.WorkspaceID(id), ts, envDef
	return nil
}

func scanWorkspace(row interface{ Scan(...any) error }) (*domain.Workspace, error) {
	var (
		w                        domain.Workspace
		legacyImage, legacyEnv   string
		legacySetup, environment string
		createdAt                int64
	)
	if err := row.Scan(&w.ID, &w.Name, &legacyImage, &legacyEnv, &legacySetup, &createdAt, &environment); err != nil {
		return nil, err
	}
	if environment != "" && environment != "{}" {
		if err := json.Unmarshal([]byte(environment), &w.Environment); err != nil {
			return nil, fmt.Errorf("store: decode workspace environment: %w", err)
		}
	} else {
		w.Environment = domain.WorkspaceEnvironment{
			CustomImage:  legacyImage,
			NeutralImage: legacyImage == "",
			SetupPolicy:  domain.SetupPolicy{Script: legacySetup},
		}
		if err := json.Unmarshal([]byte(legacyEnv), &w.Environment.Variables); err != nil {
			return nil, fmt.Errorf("store: decode workspace variables: %w", err)
		}
	}
	if w.Environment.CustomImage == "" && !w.Environment.NeutralImage {
		w.Environment.NeutralImage = true
	}
	w.CreatedAt = decodeTime(createdAt)
	return &w, nil
}

const workspaceCols = `id, name, image, env, setup_script, created_at, environment`

func (d *DB) GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	w, err := scanWorkspace(d.db.QueryRowContext(ctx,
		`SELECT `+workspaceCols+` FROM workspaces WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get workspace: %w", err)
	}
	return w, nil
}

func (d *DB) ListWorkspaces(ctx context.Context) ([]*domain.Workspace, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+workspaceCols+` FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list workspaces: %w", err)
	}
	return collect(rows, scanWorkspace)
}

func (d *DB) UpdateWorkspace(ctx context.Context, w *domain.Workspace) error {
	envDef, err := normalizeWorkspaceEnvironment(w.Environment)
	if err != nil {
		return err
	}
	environment, err := json.Marshal(envDef)
	if err != nil {
		return fmt.Errorf("store: encode workspace environment: %w", err)
	}
	variables, err := json.Marshal(envDef.Variables)
	if err != nil {
		return fmt.Errorf("store: encode workspace variables: %w", err)
	}
	image := envDef.CustomImage
	if envDef.NeutralImage {
		image = ""
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE workspaces SET name = ?, image = ?, env = ?, setup_script = ?, environment = ?
		 WHERE id = ?`,
		w.Name, image, string(variables), envDef.SetupPolicy.Script, string(environment), w.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update workspace: %w", mapConstraint(err, ErrNotFound))
	} else if err == nil {
		w.Environment = envDef
	}
	return err
}

// Tool snapshots

func (d *DB) CreateToolSnapshot(ctx context.Context, s *domain.ToolSnapshot) error {
	id, ts, err := prepareCreate(s.CreatedAt)
	if err != nil {
		return err
	}
	if s.ID != "" {
		id = string(s.ID)
	}
	manifest, err := json.Marshal(s.Manifest)
	if err != nil {
		return fmt.Errorf("store: encode tool manifest: %w", err)
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create tool snapshot: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO tool_snapshots
		(id, workspace_id, member_id, digest, manifest, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, s.WorkspaceID, s.MemberID, s.Digest, string(manifest), createdAt)
	if err != nil {
		return fmt.Errorf("store: create tool snapshot: %w", mapConstraint(err, ErrNotFound))
	}
	s.ID, s.CreatedAt = domain.ToolSnapshotID(id), ts
	return nil
}

func scanToolSnapshot(row interface{ Scan(...any) error }) (*domain.ToolSnapshot, error) {
	var s domain.ToolSnapshot
	var manifest string
	var createdAt int64
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.MemberID, &s.Digest, &manifest, &createdAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(manifest), &s.Manifest); err != nil {
		return nil, fmt.Errorf("store: decode tool manifest: %w", err)
	}
	s.CreatedAt = decodeTime(createdAt)
	return &s, nil
}

const toolSnapshotCols = `id, workspace_id, member_id, digest, manifest, created_at`

func (d *DB) GetToolSnapshot(ctx context.Context, id domain.ToolSnapshotID) (*domain.ToolSnapshot, error) {
	s, err := scanToolSnapshot(d.db.QueryRowContext(ctx,
		`SELECT `+toolSnapshotCols+` FROM tool_snapshots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get tool snapshot: %w", err)
	}
	return s, nil
}

func (d *DB) ListToolSnapshots(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) ([]*domain.ToolSnapshot, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+toolSnapshotCols+`
		FROM tool_snapshots WHERE member_id = ? AND workspace_id = ?
		ORDER BY created_at DESC, id DESC`, member, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: list tool snapshots: %w", err)
	}
	return collect(rows, scanToolSnapshot)
}

func (d *DB) DeleteToolSnapshot(ctx context.Context, id domain.ToolSnapshotID) error {
	var active, pending, live int
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM tool_heads WHERE snapshot_id = ?)`, id).Scan(&active); err != nil {
		return fmt.Errorf("store: check active tool snapshot: %w", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM pending_workspace_shells WHERE snapshot_id = ?)`, id).Scan(&pending); err != nil {
		return fmt.Errorf("store: check pending tool snapshot: %w", err)
	}
	var liveStatuses []string
	for _, status := range domain.AllRunStatuses {
		if !status.Terminal() {
			liveStatuses = append(liveStatuses, "?")
		}
	}
	args := []any{id}
	for _, status := range domain.AllRunStatuses {
		if !status.Terminal() {
			args = append(args, status)
		}
	}
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM runs WHERE tool_snapshot_id = ? AND status IN (`+
		strings.Join(liveStatuses, ", ")+`))`, args...).Scan(&live); err != nil {
		return fmt.Errorf("store: check live tool snapshot runs: %w", err)
	}
	if active != 0 || pending != 0 || live != 0 {
		return fmt.Errorf("store: delete tool snapshot: %w", ErrInUse)
	}
	return d.execDelete(ctx, "delete tool snapshot",
		`DELETE FROM tool_snapshots WHERE id = ?`, id)
}

func (d *DB) SetToolHead(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, id domain.ToolSnapshotID) error {
	if id == "" {
		_, err := d.db.ExecContext(ctx, `DELETE FROM tool_heads
			WHERE member_id = ? AND workspace_id = ?`, member, workspace)
		return err
	}
	var exists int
	if err := d.db.QueryRowContext(ctx, `SELECT 1 FROM tool_snapshots
		WHERE id = ? AND member_id = ? AND workspace_id = ?`, id, member, workspace).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: validate tool head: %w", err)
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO tool_heads
		(member_id, workspace_id, snapshot_id) VALUES (?, ?, ?)
		ON CONFLICT(member_id, workspace_id) DO UPDATE SET snapshot_id = excluded.snapshot_id`,
		member, workspace, id)
	if err != nil {
		return fmt.Errorf("store: set tool head: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) GetToolHead(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) (*domain.ToolSnapshot, error) {
	s, err := scanToolSnapshot(d.db.QueryRowContext(ctx, `SELECT
		s.id, s.workspace_id, s.member_id, s.digest, s.manifest, s.created_at
		FROM tool_snapshots s JOIN tool_heads h ON h.snapshot_id = s.id
		WHERE h.member_id = ? AND h.workspace_id = ?`, member, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get tool head: %w", err)
	}
	return s, nil
}

func (d *DB) SetActiveToolSnapshot(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, id domain.ToolSnapshotID) error {
	return d.SetToolHead(ctx, member, workspace, id)
}

func (d *DB) GetActiveToolSnapshot(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) (*domain.ToolSnapshot, error) {
	return d.GetToolHead(ctx, member, workspace)
}

func (d *DB) PruneToolSnapshots(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, keep int) error {
	if keep < 0 {
		return fmt.Errorf("store: prune tool snapshots: negative keep")
	}
	rows, err := d.ListToolSnapshots(ctx, member, workspace)
	if err != nil {
		return err
	}
	for i, s := range rows {
		if i < keep {
			continue
		}
		if err := d.DeleteToolSnapshot(ctx, s.ID); err != nil && !errors.Is(err, ErrInUse) {
			return err
		}
	}
	return nil
}

// Pending workspace shells

func (d *DB) CreatePendingWorkspaceShell(ctx context.Context, s *PendingWorkspaceShell) error {
	id, ts, err := prepareCreate(s.CreatedAt)
	if err != nil {
		return err
	}
	updated := s.UpdatedAt
	if updated.IsZero() {
		updated = ts
	}
	updatedAt, err := encodeTime(updated)
	if err != nil {
		return fmt.Errorf("store: create pending shell: %w", err)
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create pending shell: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO pending_workspace_shells
		(id, workspace_id, member_id, snapshot_id, staging_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, s.WorkspaceID, s.MemberID, s.SnapshotID,
		s.StagingID, createdAt, updatedAt); err != nil {
		return fmt.Errorf("store: create pending shell: %w", mapConstraint(err, ErrNotFound))
	}
	s.ID, s.CreatedAt, s.UpdatedAt = id, ts, updated
	return nil
}

func scanPendingWorkspaceShell(row interface{ Scan(...any) error }) (*PendingWorkspaceShell, error) {
	var s PendingWorkspaceShell
	var createdAt, updatedAt int64
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.MemberID, &s.SnapshotID,
		&s.StagingID, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt = decodeTime(createdAt), decodeTime(updatedAt)
	return &s, nil
}

const pendingWorkspaceShellCols = `id, workspace_id, member_id, snapshot_id, staging_id, created_at, updated_at`

func (d *DB) GetPendingWorkspaceShell(ctx context.Context, id string) (*PendingWorkspaceShell, error) {
	s, err := scanPendingWorkspaceShell(d.db.QueryRowContext(ctx,
		`SELECT `+pendingWorkspaceShellCols+` FROM pending_workspace_shells WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pending shell: %w", err)
	}
	return s, nil
}

// ListPendingWorkspaceShellsBefore returns a bounded batch of stale sessions.
func (d *DB) ListPendingWorkspaceShellsBefore(ctx context.Context, before time.Time, limit int) ([]*PendingWorkspaceShell, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+pendingWorkspaceShellCols+`
		FROM pending_workspace_shells WHERE updated_at < ? ORDER BY updated_at LIMIT ?`,
		before.UTC().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows, scanPendingWorkspaceShell)
}
func (d *DB) ListPendingWorkspaceShells(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) ([]*PendingWorkspaceShell, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+pendingWorkspaceShellCols+`
		FROM pending_workspace_shells WHERE member_id = ? AND workspace_id = ? ORDER BY id`,
		member, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: list pending shells: %w", err)
	}
	return collect(rows, scanPendingWorkspaceShell)
}

func (d *DB) DeletePendingWorkspaceShell(ctx context.Context, id string) error {
	return d.execDelete(ctx, "delete pending shell",
		`DELETE FROM pending_workspace_shells WHERE id = ?`, id)
}

func (d *DB) DeleteWorkspace(ctx context.Context, id domain.WorkspaceID) error {
	return d.execDelete(ctx, "delete workspace", `DELETE FROM workspaces WHERE id = ?`, id)
}

// Sessions

// validateSession rejects undefined steer_others values before they are
// persisted.
func validateSession(s *domain.Session, op string) error {
	if !domain.ValidSteerOthers(s.SteerOthers) {
		return fmt.Errorf("store: %s session: invalid steer_others %q", op, s.SteerOthers)
	}
	return nil
}

func (d *DB) CreateSession(ctx context.Context, s *domain.Session) error {
	if err := validateSession(s, "create"); err != nil {
		return err
	}
	id, ts, err := prepareCreate(s.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO sessions (id, workspace_id, name, base_branch, steer_others, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, s.WorkspaceID, s.Name, s.BaseBranch, s.SteerOthers, createdAt,
	); err != nil {
		return fmt.Errorf("store: create session: %w", mapConstraint(err, ErrNotFound))
	}
	s.ID, s.CreatedAt = domain.SessionID(id), ts
	return nil
}

func scanSession(row interface{ Scan(...any) error }) (*domain.Session, error) {
	var (
		s         domain.Session
		createdAt int64
	)
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.Name, &s.BaseBranch, &s.SteerOthers, &createdAt); err != nil {
		return nil, err
	}
	s.CreatedAt = decodeTime(createdAt)
	return &s, nil
}

const sessionCols = `id, workspace_id, name, base_branch, steer_others, created_at`

func (d *DB) GetSession(ctx context.Context, id domain.SessionID) (*domain.Session, error) {
	s, err := scanSession(d.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get session: %w", err)
	}
	return s, nil
}

func (d *DB) ListSessions(ctx context.Context) ([]*domain.Session, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM sessions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	return collect(rows, scanSession)
}

func (d *DB) ListSessionsByWorkspace(ctx context.Context, id domain.WorkspaceID) ([]*domain.Session, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE workspace_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions by workspace: %w", err)
	}
	return collect(rows, scanSession)
}

func (d *DB) UpdateSession(ctx context.Context, s *domain.Session) error {
	if err := validateSession(s, "update"); err != nil {
		return err
	}
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE sessions SET workspace_id = ?, name = ?, base_branch = ?, steer_others = ?
		 WHERE id = ?`,
		s.WorkspaceID, s.Name, s.BaseBranch, s.SteerOthers, s.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update session: %w", mapConstraint(err, ErrNotFound))
	}
	return err
}

func (d *DB) DeleteSession(ctx context.Context, id domain.SessionID) error {
	return d.execDelete(ctx, "delete session", `DELETE FROM sessions WHERE id = ?`, id)
}

// Members

func (d *DB) CreateMember(ctx context.Context, m *domain.Member) error {
	if !m.Role.Valid() {
		return fmt.Errorf("store: create member: invalid role %q", m.Role)
	}
	key, err := normalizeMemberKey(m.PublicKey, m.TailnetLogin, "create member")
	if err != nil {
		return err
	}
	id, ts, err := prepareCreate(m.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create member: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO members (id, display_name, public_key, tailnet_login, pending, color, role, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, m.DisplayName, key, m.TailnetLogin, m.Pending, m.Color, m.Role, createdAt,
	); err != nil {
		return fmt.Errorf("store: create member: %w", mapConstraint(err, ErrNotFound))
	}
	m.ID, m.PublicKey, m.CreatedAt = domain.MemberID(id), key, ts
	return nil
}

// normalizeMemberKey canonicalizes the public key when present and
// enforces that at least one identity (key or tailnet login) exists.
func normalizeMemberKey(publicKey, tailnetLogin, op string) (string, error) {
	if publicKey == "" {
		if tailnetLogin == "" {
			return "", fmt.Errorf("store: %s: a public key or a tailnet login is required", op)
		}
		return "", nil
	}
	key, err := normalizePublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("store: %s: %w", op, err)
	}
	return key, nil
}

func scanMember(row interface{ Scan(...any) error }) (*domain.Member, error) {
	var (
		m         domain.Member
		createdAt int64
	)
	if err := row.Scan(&m.ID, &m.DisplayName, &m.PublicKey, &m.TailnetLogin, &m.Pending,
		&m.Color, &m.Role, &createdAt); err != nil {
		return nil, err
	}
	m.CreatedAt = decodeTime(createdAt)
	return &m, nil
}

const memberCols = `id, display_name, public_key, tailnet_login, pending, color, role, created_at`

func (d *DB) GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error) {
	m, err := scanMember(d.db.QueryRowContext(ctx,
		`SELECT `+memberCols+` FROM members WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get member: %w", err)
	}
	return m, nil
}

func (d *DB) GetMemberByPublicKey(ctx context.Context, publicKey string) (*domain.Member, error) {
	key, err := normalizePublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("store: get member by public key: %w", err)
	}
	m, err := scanMember(d.db.QueryRowContext(ctx,
		`SELECT `+memberCols+` FROM members WHERE public_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get member by public key: %w", err)
	}
	return m, nil
}

func (d *DB) GetMemberByTailnetLogin(ctx context.Context, login string) (*domain.Member, error) {
	if login == "" {
		return nil, fmt.Errorf("store: get member by tailnet login: empty login")
	}
	m, err := scanMember(d.db.QueryRowContext(ctx,
		`SELECT `+memberCols+` FROM members WHERE tailnet_login = ?`, login))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get member by tailnet login: %w", err)
	}
	return m, nil
}

func (d *DB) ApproveMember(ctx context.Context, id domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE members SET pending = 0 WHERE id = ?`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: approve member: %w", err)
	}
	return err
}

func (d *DB) ListMembers(ctx context.Context) ([]*domain.Member, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+memberCols+` FROM members ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list members: %w", err)
	}
	return collect(rows, scanMember)
}

func (d *DB) UpdateMember(ctx context.Context, m *domain.Member) error {
	if !m.Role.Valid() {
		return fmt.Errorf("store: update member: invalid role %q", m.Role)
	}
	key, err := normalizeMemberKey(m.PublicKey, m.TailnetLogin, "update member")
	if err != nil {
		return err
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE members SET display_name = ?, public_key = ?, tailnet_login = ?, pending = ?,
		 color = ?, role = ?
		 WHERE id = ?`,
		m.DisplayName, key, m.TailnetLogin, m.Pending, m.Color, m.Role, m.ID))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			err = fmt.Errorf("store: update member: %w", mapConstraint(err, ErrNotFound))
		}
		return err
	}
	m.PublicKey = key
	return nil
}

func (d *DB) DeleteMember(ctx context.Context, id domain.MemberID) error {
	return d.execDelete(ctx, "delete member", `DELETE FROM members WHERE id = ?`, id)
}

// Runs

func validateRun(r *domain.Run, op string) error {
	if !r.Status.Valid() {
		return fmt.Errorf("store: %s run: invalid status %q", op, r.Status)
	}
	if !r.Mode.Valid() {
		return fmt.Errorf("store: %s run: invalid launch mode %q", op, r.Mode)
	}
	return nil
}

func (d *DB) CreateRun(ctx context.Context, r *domain.Run) error {
	if err := validateRun(r, "create"); err != nil {
		return err
	}
	id, ts, err := prepareCreate(r.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create run: %w", err)
	}
	startedAt, err := encodeTimePtr(r.StartedAt)
	if err != nil {
		return fmt.Errorf("store: create run: started at: %w", err)
	}
	finishedAt, err := encodeTimePtr(r.FinishedAt)
	if err != nil {
		return fmt.Errorf("store: create run: finished at: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO runs (id, session_id, member_id, task, harness, mode, status,
		                   branch, worktree, protected, created_at, started_at,
		                   finished_at, profile_snapshot_id, tool_snapshot_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.SessionID, r.MemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Branch, r.Worktree, r.Protected, createdAt, startedAt, finishedAt,
		r.ProfileSnapshotID, r.ToolSnapshotID,
	); err != nil {
		return fmt.Errorf("store: create run: %w", mapConstraint(err, ErrNotFound))
	}
	r.ID, r.CreatedAt = domain.RunID(id), ts
	return nil
}

func scanRun(row interface{ Scan(...any) error }) (*domain.Run, error) {
	var (
		r                     domain.Run
		createdAt             int64
		startedAt, finishedAt *int64
	)
	if err := row.Scan(&r.ID, &r.SessionID, &r.MemberID, &r.Task, &r.Harness,
		&r.Mode, &r.Status, &r.Branch, &r.Worktree, &r.Protected,
		&createdAt, &startedAt, &finishedAt, &r.ProfileSnapshotID, &r.ToolSnapshotID); err != nil {
		return nil, err
	}
	r.CreatedAt = decodeTime(createdAt)
	r.StartedAt = decodeTimePtr(startedAt)
	r.FinishedAt = decodeTimePtr(finishedAt)
	return &r, nil
}

const runCols = `id, session_id, member_id, task, harness, mode, status,
	branch, worktree, protected, created_at, started_at, finished_at, profile_snapshot_id, tool_snapshot_id`

func (d *DB) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	r, err := scanRun(d.db.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get run: %w", err)
	}
	return r, nil
}

func (d *DB) ListRunsBySession(ctx context.Context, id domain.SessionID) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE session_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by session: %w", err)
	}
	return collect(rows, scanRun)
}

func (d *DB) ListRunsByMember(ctx context.Context, id domain.MemberID) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE member_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by member: %w", err)
	}
	return collect(rows, scanRun)
}

// ListActiveRuns returns runs whose status is non-terminal. The status set
// is derived from domain.AllRunStatuses so it cannot drift from the enum.
func (d *DB) ListActiveRuns(ctx context.Context) ([]*domain.Run, error) {
	var (
		placeholders []string
		args         []any
	)
	for _, s := range domain.AllRunStatuses {
		if !s.Terminal() {
			placeholders = append(placeholders, "?")
			args = append(args, s)
		}
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE status IN (`+
			strings.Join(placeholders, ", ")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list active runs: %w", err)
	}
	return collect(rows, scanRun)
}

func (d *DB) UpdateRun(ctx context.Context, r *domain.Run) error {
	if err := validateRun(r, "update"); err != nil {
		return err
	}
	startedAt, err := encodeTimePtr(r.StartedAt)
	if err != nil {
		return fmt.Errorf("store: update run: started at: %w", err)
	}
	finishedAt, err := encodeTimePtr(r.FinishedAt)
	if err != nil {
		return fmt.Errorf("store: update run: finished at: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET session_id = ?, member_id = ?, task = ?, harness = ?,
		     mode = ?, status = ?, branch = ?, worktree = ?, protected = ?,
		     started_at = ?, finished_at = ?, profile_snapshot_id = ?,
		     tool_snapshot_id = ?
		 WHERE id = ?`,
		r.SessionID, r.MemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Branch, r.Worktree, r.Protected, startedAt, finishedAt, r.ProfileSnapshotID,
		r.ToolSnapshotID, r.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update run: %w", mapConstraint(err, ErrNotFound))
	}
	return err
}

func (d *DB) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, startedAt, finishedAt *time.Time) error {
	if !status.Valid() {
		return fmt.Errorf("store: update run status: invalid status %q", status)
	}
	started, err := encodeTimePtr(startedAt)
	if err != nil {
		return fmt.Errorf("store: update run status: started at: %w", err)
	}
	finished, err := encodeTimePtr(finishedAt)
	if err != nil {
		return fmt.Errorf("store: update run status: finished at: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET status = ?,
		     started_at = COALESCE(?, started_at),
		     finished_at = COALESCE(?, finished_at)
		 WHERE id = ?`,
		status, started, finished, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update run status: %w", err)
	}
	return err
}

// TransferRun reassigns a run's owner. Existence of the new owner is
// enforced by the runs.member_id REFERENCES members(id) foreign key
// (foreign_keys pragma is on): a bogus member surfaces as ErrConflict
// via mapConstraint rather than being silently accepted.
func (d *DB) TransferRun(ctx context.Context, id domain.RunID, to domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET member_id = ? WHERE id = ?`, to, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: transfer run: %w", mapConstraint(err, ErrNotFound))
	}
	return err
}

// SetRunToolSnapshot updates only the run's tool snapshot pin. A non-empty
// snapshot must belong to the run's current member and workspace, and the
// ownership check is part of the update so a concurrent handoff cannot be
// overwritten by a stale run object.
func (d *DB) SetRunToolSnapshot(ctx context.Context, id domain.RunID, snapshot domain.ToolSnapshotID) error {
	var (
		res sql.Result
		err error
	)
	if snapshot == "" {
		res, err = d.db.ExecContext(ctx,
			`UPDATE runs SET tool_snapshot_id = '' WHERE id = ?`, id)
	} else {
		res, err = d.db.ExecContext(ctx, `
			UPDATE runs
			SET tool_snapshot_id = ?
			WHERE id = ?
			  AND EXISTS (
				SELECT 1
				FROM tool_snapshots ts
				JOIN sessions s ON s.workspace_id = ts.workspace_id
				WHERE ts.id = ?
				  AND ts.member_id = runs.member_id
				  AND s.id = runs.session_id
			  )`, snapshot, id, snapshot)
	}
	if err != nil {
		return fmt.Errorf("store: set run tool snapshot: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set run tool snapshot: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) SetRunProtected(ctx context.Context, id domain.RunID, protected bool) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET protected = ? WHERE id = ?`, protected, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set run protected: %w", err)
	}
	return err
}

func (d *DB) SetSessionSteerOthers(ctx context.Context, id domain.SessionID, steerOthers string) error {
	if !domain.ValidSteerOthers(steerOthers) {
		return fmt.Errorf("store: set session steer_others: invalid value %q", steerOthers)
	}
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE sessions SET steer_others = ? WHERE id = ?`, steerOthers, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set session steer_others: %w", err)
	}
	return err
}

func (d *DB) DeleteRun(ctx context.Context, id domain.RunID) error {
	return d.execDelete(ctx, "delete run", `DELETE FROM runs WHERE id = ?`, id)
}

// collect drains rows through scan, closing them and surfacing iteration
// errors.
func collect[T any](rows *sql.Rows, scan func(interface{ Scan(...any) error }) (*T, error)) ([]*T, error) {
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []*T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan row: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate rows: %w", err)
	}
	return out, nil
}
