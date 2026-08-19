package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

func scanProfileSnapshot(row interface{ Scan(...any) error }) (*domain.ProfileSnapshot, error) {
	var (
		s         domain.ProfileSnapshot
		createdAt int64
	)
	if err := row.Scan(&s.ID, &s.MemberID, &s.Harness, &s.Digest, &createdAt); err != nil {
		return nil, err
	}
	s.CreatedAt = decodeTime(createdAt)
	return &s, nil
}

const profileSnapshotCols = `id, member_id, harness, digest, created_at`

// SaveProfileSnapshot persists a snapshot and its files. Identical
// member+harness+digest reuses the existing row identity. Blobs are
// content-addressed and shared across snapshots.
func (d *DB) SaveProfileSnapshot(ctx context.Context, s *domain.ProfileSnapshot, files []ProfileFile) error {
	if s == nil {
		return fmt.Errorf("store: save profile snapshot: nil snapshot")
	}
	if s.MemberID == "" || s.Harness == "" || s.Digest == "" {
		return fmt.Errorf("store: save profile snapshot: member, harness, and digest are required")
	}
	existing, err := d.GetProfileSnapshotByDigest(ctx, s.MemberID, s.Harness, s.Digest)
	if err == nil {
		*s = *existing
		if herr := d.SetProfileHead(ctx, s.MemberID, s.Harness, s.ID); herr != nil {
			return herr
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}

	id, ts, err := prepareCreate(s.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: save profile snapshot: %w", err)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: save profile snapshot: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO profile_snapshots (id, member_id, harness, digest, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, s.MemberID, s.Harness, s.Digest, createdAt,
	); err != nil {
		return fmt.Errorf("store: save profile snapshot: %w", mapConstraint(err, ErrNotFound))
	}
	for _, f := range files {
		blob := sha256.Sum256(f.Content)
		digest := hex.EncodeToString(blob[:])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO profile_blobs (digest, content) VALUES (?, ?)
			 ON CONFLICT (digest) DO NOTHING`,
			digest, f.Content,
		); err != nil {
			return fmt.Errorf("store: save profile blob: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO profile_files (snapshot_id, path, mode, blob_digest)
			 VALUES (?, ?, ?, ?)`,
			id, f.Path, f.Mode, digest,
		); err != nil {
			return fmt.Errorf("store: save profile file: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO profile_heads (member_id, harness, snapshot_id) VALUES (?, ?, ?)
		 ON CONFLICT (member_id, harness) DO UPDATE SET snapshot_id = excluded.snapshot_id`,
		s.MemberID, s.Harness, id,
	); err != nil {
		return fmt.Errorf("store: set profile head: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: save profile snapshot: commit: %w", err)
	}
	s.ID, s.CreatedAt = domain.ProfileSnapshotID(id), ts
	return nil
}

func (d *DB) GetProfileSnapshot(ctx context.Context, id domain.ProfileSnapshotID) (*domain.ProfileSnapshot, error) {
	s, err := scanProfileSnapshot(d.db.QueryRowContext(ctx,
		`SELECT `+profileSnapshotCols+` FROM profile_snapshots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get profile snapshot: %w", err)
	}
	return s, nil
}

func (d *DB) GetProfileSnapshotByDigest(ctx context.Context, member domain.MemberID, harness, digest string) (*domain.ProfileSnapshot, error) {
	s, err := scanProfileSnapshot(d.db.QueryRowContext(ctx,
		`SELECT `+profileSnapshotCols+` FROM profile_snapshots
		 WHERE member_id = ? AND harness = ? AND digest = ?`, member, harness, digest))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get profile snapshot by digest: %w", err)
	}
	return s, nil
}

func (d *DB) ListProfileSnapshots(ctx context.Context, member domain.MemberID, harness string) ([]*domain.ProfileSnapshot, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+profileSnapshotCols+` FROM profile_snapshots
		 WHERE member_id = ? AND harness = ?
		 ORDER BY created_at DESC, id DESC`, member, harness)
	if err != nil {
		return nil, fmt.Errorf("store: list profile snapshots: %w", err)
	}
	return collect(rows, scanProfileSnapshot)
}

func (d *DB) GetProfileFiles(ctx context.Context, id domain.ProfileSnapshotID) ([]ProfileFile, error) {
	if _, err := d.GetProfileSnapshot(ctx, id); err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT f.path, f.mode, b.content
		 FROM profile_files f
		 JOIN profile_blobs b ON b.digest = f.blob_digest
		 WHERE f.snapshot_id = ?
		 ORDER BY f.path`, id)
	if err != nil {
		return nil, fmt.Errorf("store: get profile files: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []ProfileFile
	for rows.Next() {
		var f ProfileFile
		if err := rows.Scan(&f.Path, &f.Mode, &f.Content); err != nil {
			return nil, fmt.Errorf("store: scan profile file: %w", err)
		}
		// Copy so later writes cannot alias the driver's buffer.
		f.Content = append([]byte(nil), f.Content...)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate profile files: %w", err)
	}
	if out == nil {
		out = []ProfileFile{}
	}
	return out, nil
}

func (d *DB) SetProfileHead(ctx context.Context, member domain.MemberID, harness string, id domain.ProfileSnapshotID) error {
	if _, err := d.GetProfileSnapshot(ctx, id); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO profile_heads (member_id, harness, snapshot_id) VALUES (?, ?, ?)
		 ON CONFLICT (member_id, harness) DO UPDATE SET snapshot_id = excluded.snapshot_id`,
		member, harness, id,
	); err != nil {
		return fmt.Errorf("store: set profile head: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) GetProfileHead(ctx context.Context, member domain.MemberID, harness string) (*domain.ProfileSnapshot, error) {
	s, err := scanProfileSnapshot(d.db.QueryRowContext(ctx,
		`SELECT s.id, s.member_id, s.harness, s.digest, s.created_at
		 FROM profile_heads h
		 JOIN profile_snapshots s ON s.id = h.snapshot_id
		 WHERE h.member_id = ? AND h.harness = ?`, member, harness))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get profile head: %w", err)
	}
	return s, nil
}

// PruneProfileSnapshots keeps the newest `keep` snapshots for member+harness
// plus the current head (so rollback targets are not dropped if they fall
// outside the window). Unreferenced blobs are deleted afterwards.
func (d *DB) PruneProfileSnapshots(ctx context.Context, member domain.MemberID, harness string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: prune profile snapshots: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM profile_snapshots
		WHERE member_id = ? AND harness = ?
		  AND id NOT IN (
			SELECT id FROM (
				SELECT id FROM profile_snapshots
				WHERE member_id = ? AND harness = ?
				ORDER BY created_at DESC, id DESC
				LIMIT ?
			)
			UNION
			SELECT snapshot_id FROM profile_heads
			WHERE member_id = ? AND harness = ?
			UNION
			SELECT profile_snapshot_id FROM runs
			WHERE profile_snapshot_id != ''
		  )`,
		member, harness, member, harness, keep, member, harness,
	); err != nil {
		return fmt.Errorf("store: prune profile snapshots: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM profile_blobs
		 WHERE digest NOT IN (SELECT DISTINCT blob_digest FROM profile_files)`,
	); err != nil {
		return fmt.Errorf("store: prune profile blobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: prune profile snapshots: commit: %w", err)
	}
	return nil
}

func (d *DB) SetRunProfileSnapshot(ctx context.Context, run domain.RunID, id domain.ProfileSnapshotID) error {
	if id != "" {
		if _, err := d.GetProfileSnapshot(ctx, id); err != nil {
			return err
		}
	}
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET profile_snapshot_id = ? WHERE id = ?`, id, run))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set run profile snapshot: %w", err)
	}
	return err
}
