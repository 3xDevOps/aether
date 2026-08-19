package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/3xDevOps/Aether/internal/domain"
)

const eventsSchema = `
CREATE TABLE IF NOT EXISTS events (
	seq        INTEGER PRIMARY KEY,
	id         TEXT NOT NULL,
	ts         INTEGER NOT NULL,
	session_id TEXT NOT NULL,
	run_id     TEXT NOT NULL DEFAULT '',
	actor_id   TEXT NOT NULL DEFAULT '',
	type       TEXT NOT NULL,
	payload    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_session_seq ON events (session_id, seq);
`

// SQLiteLog is the EventLog backed by a SQLite events table
// (modernc.org/sqlite, pure Go). Rows are append-only; seq is the bus
// cursor and primary key.
type SQLiteLog struct {
	db *sql.DB
}

// sqliteURIPath percent-encodes the characters that would otherwise be
// misparsed in the path component of a SQLite file: URI — '?' starts the
// query string (silently truncating the path), '#' the fragment, and '%'
// an escape sequence. SQLite percent-decodes the path when opening.
var sqliteURIPath = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// OpenSQLiteLog opens (creating if needed) the event log database at path
// and ensures the events table exists.
func OpenSQLiteLog(path string) (*SQLiteLog, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", sqliteURIPath.Replace(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("events: open sqlite log: %w", err)
	}
	// SQLite allows one writer; a single connection avoids lock contention
	// between pooled connections under the race detector and in production.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(eventsSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("events: init sqlite log schema: %w", err)
	}
	return &SQLiteLog{db: db}, nil
}

// Append implements EventLog.
func (l *SQLiteLog) Append(ctx context.Context, e Event) error {
	if e.Payload == nil {
		return ErrNoPayload
	}
	body, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("events: encode payload: %w", err)
	}
	_, err = l.db.ExecContext(ctx,
		`INSERT INTO events (seq, id, ts, session_id, run_id, actor_id, type, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Seq, e.ID, e.Time.UnixNano(), string(e.SessionID), string(e.RunID),
		string(e.ActorID), string(e.Type), string(body))
	if err != nil {
		return fmt.Errorf("events: append event %d: %w", e.Seq, err)
	}
	return nil
}

// Read implements EventLog.
func (l *SQLiteLog) Read(ctx context.Context, f Filter, afterSeq, uptoSeq uint64, limit int) ([]Event, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT seq, id, ts, session_id, run_id, actor_id, type, payload
		FROM events WHERE seq > ?`)
	args := []any{afterSeq}
	if uptoSeq > 0 {
		sb.WriteString(" AND seq <= ?")
		args = append(args, uptoSeq)
	}
	if f.Session != "" {
		sb.WriteString(" AND session_id = ?")
		args = append(args, string(f.Session))
	}
	if f.Run != "" {
		sb.WriteString(" AND run_id = ?")
		args = append(args, string(f.Run))
	}
	if len(f.Types) > 0 {
		sb.WriteString(" AND type IN (?" + strings.Repeat(", ?", len(f.Types)-1) + ")")
		for _, t := range f.Types {
			args = append(args, string(t))
		}
	}
	sb.WriteString(" ORDER BY seq LIMIT ?")
	args = append(args, limit)

	rows, err := l.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("events: read log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var (
			e       Event
			ts      int64
			sess    string
			run     string
			actor   string
			typ     string
			payload []byte
		)
		if err := rows.Scan(&e.Seq, &e.ID, &ts, &sess, &run, &actor, &typ, &payload); err != nil {
			return nil, fmt.Errorf("events: scan log row: %w", err)
		}
		e.Time = time.Unix(0, ts).UTC()
		e.SessionID = domain.SessionID(sess)
		e.RunID = domain.RunID(run)
		e.ActorID = domain.MemberID(actor)
		e.Type = Type(typ)
		p, err := DecodePayload(e.Type, payload)
		if err != nil {
			return nil, err
		}
		e.Payload = p
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: read log: %w", err)
	}
	return out, nil
}

// LastSeq implements EventLog.
func (l *SQLiteLog) LastSeq(ctx context.Context) (uint64, error) {
	var last uint64
	err := l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&last)
	if err != nil {
		return 0, fmt.Errorf("events: read last seq: %w", err)
	}
	return last, nil
}

// Close implements EventLog.
func (l *SQLiteLog) Close() error { return l.db.Close() }
