package preference

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tombstones is an append-only file of the preferences deleted and of the
// text changes made, one "id deleted" or "id updated_at_ms" line each and
// never any text. It lives beside the database, so restoring an older copy
// of the database alone, such as the snapshot an update rollback restores,
// does not bring a forgotten preference or its old text back.
type tombstones struct {
	path string
	mu   sync.Mutex
}

const deletedMark = "deleted"

func (t *tombstones) deleted(id string) error {
	return t.append(id + " " + deletedMark)
}

func (t *tombstones) edited(id string, at time.Time) error {
	return t.append(id + " " + strconv.FormatInt(at.UnixMilli(), 10))
}

// append writes line and syncs it before the change it records commits. A
// leading newline ends a partial line a crash left, which read skips.
func (t *tombstones) append(line string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.OpenFile(t.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("preference: record the change: %w", err)
	}
	if _, err = f.WriteString("\n" + line + "\n"); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("preference: record the change: %w", err)
	}
	return nil
}

func (t *tombstones) read() (deleted []string, edited map[string]time.Time, err error) {
	f, err := os.Open(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("preference: read %s: %w", t.path, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	edited = make(map[string]time.Time)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id, mark, ok := strings.Cut(sc.Text(), " ")
		if !ok || id == "" {
			// A crash mid-append leaves a partial last line; its change
			// never committed.
			continue
		}
		if mark == deletedMark {
			deleted = append(deleted, id)
			continue
		}
		ms, perr := strconv.ParseInt(mark, 10, 64)
		if perr != nil {
			continue
		}
		if at := time.UnixMilli(ms); at.After(edited[id]) {
			edited[id] = at
		}
	}
	if err = sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("preference: read %s: %w", t.path, err)
	}
	return deleted, edited, nil
}

// Restore re-applies the recorded deletions and text changes to the
// database: a preference recorded as deleted is deleted again, and one
// older than its last recorded text change is deleted, since it holds text
// the member replaced. It returns how many it deleted.
func (s *Service) Restore(ctx context.Context) (int64, error) {
	deleted, edited, err := s.tombstones.read()
	if err != nil {
		return 0, err
	}
	n, err := s.store.ApplyPreferenceTombstones(ctx, deleted, edited)
	if err != nil {
		return n, fmt.Errorf("preference: %w", err)
	}
	return n, nil
}
