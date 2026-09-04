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
// the int64 range (roughly years 1678-2262) - notably the zero time.Time -
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

// validateWorkspace rejects undefined steer_others values before they are
// persisted.
func validateWorkspace(w *domain.Workspace, op string) error {
	if !domain.ValidSteerOthers(w.SteerOthers) {
		return fmt.Errorf("store: %s workspace: invalid steer_others %q", op, w.SteerOthers)
	}
	return nil
}

func (d *DB) CreateWorkspace(ctx context.Context, w *domain.Workspace) error {
	if err := validateWorkspace(w, "create"); err != nil {
		return err
	}
	id, ts, err := prepareCreate(w.CreatedAt)
	if err != nil {
		return err
	}
	baseBranch := w.BaseBranch
	if baseBranch == "" {
		baseBranch = domain.DefaultBaseBranch
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
		`INSERT INTO workspaces (id, name, image, env, setup_script, created_at, environment,
		                         base_branch, steer_others)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, w.Name, image, string(variables), envDef.SetupPolicy.Script, createdAt, string(environment),
		baseBranch, w.SteerOthers,
	); err != nil {
		return fmt.Errorf("store: create workspace: %w", mapConstraint(err, ErrNotFound))
	}
	w.ID, w.CreatedAt, w.Environment, w.BaseBranch = domain.WorkspaceID(id), ts, envDef, baseBranch
	return nil
}

func scanWorkspace(row interface{ Scan(...any) error }) (*domain.Workspace, error) {
	var (
		w                        domain.Workspace
		legacyImage, legacyEnv   string
		legacySetup, environment string
		createdAt                int64
	)
	if err := row.Scan(&w.ID, &w.Name, &legacyImage, &legacyEnv, &legacySetup, &createdAt,
		&environment, &w.BaseBranch, &w.SteerOthers); err != nil {
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

const workspaceCols = `id, name, image, env, setup_script, created_at, environment, base_branch, steer_others`

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
	if err := validateWorkspace(w, "update"); err != nil {
		return err
	}
	envDef, err := normalizeWorkspaceEnvironment(w.Environment)
	if err != nil {
		return err
	}
	baseBranch := w.BaseBranch
	if baseBranch == "" {
		baseBranch = domain.DefaultBaseBranch
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
		`UPDATE workspaces SET name = ?, image = ?, env = ?, setup_script = ?, environment = ?,
		     base_branch = ?, steer_others = ?
		 WHERE id = ?`,
		w.Name, image, string(variables), envDef.SetupPolicy.Script, string(environment),
		baseBranch, w.SteerOthers, w.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update workspace: %w", mapConstraint(err, ErrNotFound))
	} else if err == nil {
		w.Environment, w.BaseBranch = envDef, baseBranch
	}
	return err
}

func (d *DB) SetWorkspaceSteerOthers(ctx context.Context, id domain.WorkspaceID, steerOthers string) error {
	if !domain.ValidSteerOthers(steerOthers) {
		return fmt.Errorf("store: set workspace steer_others: invalid value %q", steerOthers)
	}
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE workspaces SET steer_others = ? WHERE id = ?`, steerOthers, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set workspace steer_others: %w", err)
	}
	return err
}

// Harness definitions

const harnessDefinitionCols = `member_id, name, definition, created_at, updated_at`

func (d *DB) UpsertHarnessDefinition(ctx context.Context, def *HarnessDefinition) error {
	now := time.Now().UTC()
	created := def.CreatedAt
	if created.IsZero() {
		created = now
	}
	createdAt, err := encodeTime(created)
	if err != nil {
		return fmt.Errorf("store: upsert harness definition: %w", err)
	}
	updatedAt, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("store: upsert harness definition: %w", err)
	}
	var storedCreated int64
	if err := d.db.QueryRowContext(ctx, `INSERT INTO harness_definitions
		(member_id, name, definition, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(member_id, name) DO UPDATE SET
			definition = excluded.definition,
			updated_at = excluded.updated_at
		RETURNING created_at`,
		def.MemberID, def.Name, string(def.Definition), createdAt, updatedAt,
	).Scan(&storedCreated); err != nil {
		return fmt.Errorf("store: upsert harness definition: %w", mapConstraint(err, ErrNotFound))
	}
	def.CreatedAt, def.UpdatedAt = decodeTime(storedCreated), now
	return nil
}

func scanHarnessDefinition(row interface{ Scan(...any) error }) (*HarnessDefinition, error) {
	var def HarnessDefinition
	var blob string
	var createdAt, updatedAt int64
	if err := row.Scan(&def.MemberID, &def.Name, &blob, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	def.Definition = []byte(blob)
	def.CreatedAt, def.UpdatedAt = decodeTime(createdAt), decodeTime(updatedAt)
	return &def, nil
}

func (d *DB) GetHarnessDefinition(ctx context.Context, member domain.MemberID, name string) (*HarnessDefinition, error) {
	def, err := scanHarnessDefinition(d.db.QueryRowContext(ctx,
		`SELECT `+harnessDefinitionCols+` FROM harness_definitions
		WHERE member_id = ? AND name = ?`, member, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get harness definition: %w", err)
	}
	return def, nil
}

func (d *DB) ListHarnessDefinitions(ctx context.Context, member domain.MemberID) ([]*HarnessDefinition, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+harnessDefinitionCols+`
		FROM harness_definitions WHERE member_id = ? ORDER BY name`, member)
	if err != nil {
		return nil, fmt.Errorf("store: list harness definitions: %w", err)
	}
	return collect(rows, scanHarnessDefinition)
}

func (d *DB) DeleteWorkspace(ctx context.Context, id domain.WorkspaceID) error {
	return d.execDelete(ctx, "delete workspace", `DELETE FROM workspaces WHERE id = ?`, id)
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
		`INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status,
		                   reason, branch, worktree, protected, created_at, started_at,
		                   finished_at, profile_snapshot_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.WorkspaceID, r.MemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Reason, r.Branch, r.Worktree, r.Protected, createdAt, startedAt, finishedAt,
		r.ProfileSnapshotID,
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
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.MemberID, &r.Task, &r.Harness,
		&r.Mode, &r.Status, &r.Reason, &r.Branch, &r.Worktree, &r.Protected,
		&createdAt, &startedAt, &finishedAt, &r.ProfileSnapshotID); err != nil {
		return nil, err
	}
	r.CreatedAt = decodeTime(createdAt)
	r.StartedAt = decodeTimePtr(startedAt)
	r.FinishedAt = decodeTimePtr(finishedAt)
	return &r, nil
}

const runCols = `id, workspace_id, member_id, task, harness, mode, status,
	reason, branch, worktree, protected, created_at, started_at, finished_at, profile_snapshot_id`

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

func (d *DB) ListRunsByWorkspace(ctx context.Context, id domain.WorkspaceID) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE workspace_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by workspace: %w", err)
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
		`UPDATE runs SET workspace_id = ?, member_id = ?, task = ?, harness = ?,
		     mode = ?, status = ?, reason = ?, branch = ?, worktree = ?,
		     protected = ?, started_at = ?, finished_at = ?,
		     profile_snapshot_id = ?
		 WHERE id = ?`,
		r.WorkspaceID, r.MemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Reason, r.Branch, r.Worktree, r.Protected, startedAt, finishedAt,
		r.ProfileSnapshotID, r.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update run: %w", mapConstraint(err, ErrNotFound))
	}
	return err
}

func (d *DB) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, startedAt, finishedAt *time.Time) error {
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
		`UPDATE runs SET status = ?, reason = ?,
		     started_at = COALESCE(?, started_at),
		     finished_at = COALESCE(?, finished_at)
		 WHERE id = ?`,
		status, reason, started, finished, id))
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

func (d *DB) SetRunProtected(ctx context.Context, id domain.RunID, protected bool) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET protected = ? WHERE id = ?`, protected, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set run protected: %w", err)
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
