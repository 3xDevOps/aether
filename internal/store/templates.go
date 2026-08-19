package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Template is a named, parameterized run definition stored on a session:
// the agent, the task prompt, the launch mode, and the parameter defaults
// substituted into the prompt. Name is unique within its session.
type Template struct {
	ID        string
	SessionID domain.SessionID
	Name      string
	Task      string
	Harness   string
	Mode      domain.LaunchMode
	// Params are the task prompt's parameter defaults, keyed by the name
	// used in its {{placeholders}}.
	Params map[string]string
	// BudgetUSD is an advisory per-run cost hint carried with the
	// template. Nothing enforces it; session budgets do the enforcing.
	BudgetUSD float64
	CreatedAt time.Time
}

// Schedule is a cron rule that fires one template unattended. At most one
// schedule exists per template. MemberID is the member who created it:
// every fire is attributed to them and re-checked against their current
// role, so a demotion stops the schedule.
//
// SessionID and Template are read from the owning template on every read
// and ignored on write.
type Schedule struct {
	ID          string
	TemplateID  string
	SessionID   domain.SessionID
	Template    string
	Cron        string
	MemberID    domain.MemberID
	CreatedAt   time.Time
	LastFiredAt *time.Time
}

// TemplateStore is the task-template and schedule persistence surface.
type TemplateStore interface {
	// SaveTemplate creates or replaces the template with t's session and
	// name, filling in t.ID and t.CreatedAt. A replaced template keeps
	// its ID and creation time.
	SaveTemplate(ctx context.Context, t *Template) error
	GetTemplate(ctx context.Context, session domain.SessionID, name string) (*Template, error)
	ListTemplates(ctx context.Context, session domain.SessionID) ([]*Template, error)
	// DeleteTemplate removes a template and its schedule.
	DeleteTemplate(ctx context.Context, session domain.SessionID, name string) error

	// SaveSchedule creates or replaces the schedule of s.TemplateID,
	// filling in s.ID and s.CreatedAt. Replacing clears the last-fired
	// stamp: a redefined rule starts fresh.
	SaveSchedule(ctx context.Context, s *Schedule) error
	// ListSchedules returns a session's schedules, or every schedule when
	// session is empty.
	ListSchedules(ctx context.Context, session domain.SessionID) ([]*Schedule, error)
	DeleteSchedule(ctx context.Context, templateID string) error
	// MarkScheduleFired records the instant a fire was attempted.
	MarkScheduleFired(ctx context.Context, id string, at time.Time) error
}

const templateCols = `id, session_id, name, task, harness, mode, params, budget_usd, created_at`

func (d *DB) SaveTemplate(ctx context.Context, t *Template) error {
	if t.SessionID == "" || t.Name == "" || t.Task == "" || t.Harness == "" {
		return errors.New("store: save template: session_id, name, task, and harness are required")
	}
	id, ts, err := prepareCreate(t.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: save template: %w", err)
	}
	params, err := json.Marshal(nonNilParams(t.Params))
	if err != nil {
		return fmt.Errorf("store: encode template params: %w", err)
	}
	var (
		gotID      string
		gotCreated int64
	)
	err = d.db.QueryRowContext(ctx,
		`INSERT INTO templates (`+templateCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id, name) DO UPDATE SET
		   task = excluded.task, harness = excluded.harness, mode = excluded.mode,
		   params = excluded.params, budget_usd = excluded.budget_usd
		 RETURNING id, created_at`,
		id, t.SessionID, t.Name, t.Task, t.Harness, t.Mode, string(params), t.BudgetUSD, createdAt,
	).Scan(&gotID, &gotCreated)
	if err != nil {
		return fmt.Errorf("store: save template: %w", mapConstraint(err, ErrNotFound))
	}
	t.ID, t.CreatedAt = gotID, decodeTime(gotCreated)
	return nil
}

func (d *DB) GetTemplate(ctx context.Context, session domain.SessionID, name string) (*Template, error) {
	t, err := scanTemplate(d.db.QueryRowContext(ctx,
		`SELECT `+templateCols+` FROM templates WHERE session_id = ? AND name = ?`, session, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get template: %w", err)
	}
	return t, nil
}

func (d *DB) ListTemplates(ctx context.Context, session domain.SessionID) ([]*Template, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+templateCols+` FROM templates WHERE session_id = ? ORDER BY name`, session)
	if err != nil {
		return nil, fmt.Errorf("store: list templates: %w", err)
	}
	return collect(rows, scanTemplate)
}

func (d *DB) DeleteTemplate(ctx context.Context, session domain.SessionID, name string) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`DELETE FROM templates WHERE session_id = ? AND name = ?`, session, name))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: delete template: %w", mapConstraint(err, ErrInUse))
	}
	return err
}

// Schedules are always read joined to their template, which is where the
// session and template name come from.
const scheduleCols = `s.id, s.template_id, t.session_id, t.name, s.cron, s.member_id, s.created_at, s.last_fired_at`

func (d *DB) SaveSchedule(ctx context.Context, s *Schedule) error {
	if s.TemplateID == "" || s.Cron == "" || s.MemberID == "" {
		return errors.New("store: save schedule: template_id, cron, and member_id are required")
	}
	id, ts, err := prepareCreate(s.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: save schedule: %w", err)
	}
	var (
		gotID      string
		gotCreated int64
	)
	err = d.db.QueryRowContext(ctx,
		`INSERT INTO schedules (id, template_id, cron, member_id, created_at, last_fired_at)
		 VALUES (?, ?, ?, ?, ?, NULL)
		 ON CONFLICT (template_id) DO UPDATE SET
		   cron = excluded.cron, member_id = excluded.member_id, last_fired_at = NULL
		 RETURNING id, created_at`,
		id, s.TemplateID, s.Cron, s.MemberID, createdAt,
	).Scan(&gotID, &gotCreated)
	if err != nil {
		return fmt.Errorf("store: save schedule: %w", mapConstraint(err, ErrNotFound))
	}
	s.ID, s.CreatedAt, s.LastFiredAt = gotID, decodeTime(gotCreated), nil
	return nil
}

func (d *DB) ListSchedules(ctx context.Context, session domain.SessionID) ([]*Schedule, error) {
	query := `SELECT ` + scheduleCols + ` FROM schedules s JOIN templates t ON t.id = s.template_id`
	args := []any{}
	if session != "" {
		query += ` WHERE t.session_id = ?`
		args = append(args, session)
	}
	rows, err := d.db.QueryContext(ctx, query+` ORDER BY t.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	return collect(rows, scanSchedule)
}

func (d *DB) DeleteSchedule(ctx context.Context, templateID string) error {
	return d.execDelete(ctx, "delete schedule",
		`DELETE FROM schedules WHERE template_id = ?`, templateID)
}

func (d *DB) MarkScheduleFired(ctx context.Context, id string, at time.Time) error {
	fired, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: mark schedule fired: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE schedules SET last_fired_at = ? WHERE id = ?`, fired, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: mark schedule fired: %w", err)
	}
	return err
}

func nonNilParams(p map[string]string) map[string]string {
	if p == nil {
		return map[string]string{}
	}
	return p
}

func scanTemplate(row interface{ Scan(...any) error }) (*Template, error) {
	var (
		t         Template
		params    string
		createdAt int64
	)
	if err := row.Scan(&t.ID, &t.SessionID, &t.Name, &t.Task, &t.Harness, &t.Mode,
		&params, &t.BudgetUSD, &createdAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(params), &t.Params); err != nil {
		return nil, fmt.Errorf("store: decode template params: %w", err)
	}
	t.CreatedAt = decodeTime(createdAt)
	return &t, nil
}

func scanSchedule(row interface{ Scan(...any) error }) (*Schedule, error) {
	var (
		s         Schedule
		createdAt int64
		lastFired *int64
	)
	if err := row.Scan(&s.ID, &s.TemplateID, &s.SessionID, &s.Template, &s.Cron,
		&s.MemberID, &createdAt, &lastFired); err != nil {
		return nil, err
	}
	s.CreatedAt = decodeTime(createdAt)
	s.LastFiredAt = decodeTimePtr(lastFired)
	return &s, nil
}
