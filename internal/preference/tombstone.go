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
// text changes made, and never any text. It lives beside the database, so
// restoring an older copy of the database alone, such as the snapshot an
// update rollback restores, does not bring a forgotten preference or its
// old text back. Its lines:
//
//	<id> deleted           the preference was deleted
//	<id> edit <ms>         a text change of the row last updated at <ms> may commit
//	<id> edited <ms>       that change committed
type tombstones struct {
	path string
	mu   sync.Mutex
}

const (
	deletedMark = "deleted"
	editMark    = "edit"
	editedMark  = "edited"
)

func (t *tombstones) deleted(ids ...string) error {
	lines := make([]string, len(ids))
	for i, id := range ids {
		lines[i] = id + " " + deletedMark
	}
	return t.append(lines...)
}

// edit records, before it commits, a text change of id from the row last
// updated at was.
func (t *tombstones) edit(id string, was time.Time) error {
	return t.append(id + " " + editMark + " " + strconv.FormatInt(was.UnixMilli(), 10))
}

// edited records that the text change edit recorded committed.
func (t *tombstones) edited(id string, was time.Time) error {
	return t.append(id + " " + editedMark + " " + strconv.FormatInt(was.UnixMilli(), 10))
}

// append writes lines and syncs them before the change they record
// commits. A leading newline ends a partial line a crash left, which read
// skips.
func (t *tombstones) append(lines ...string) error {
	if len(lines) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.OpenFile(t.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("preference: record the change: %w", err)
	}
	if _, err = f.WriteString("\n" + strings.Join(lines, "\n") + "\n"); err == nil {
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

type edit struct {
	id  string
	was time.Time
}

func (t *tombstones) read() (deleted []string, edits, done map[edit]bool, err error) {
	f, err := os.Open(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("preference: read %s: %w", t.path, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	edits, done = make(map[edit]bool), make(map[edit]bool)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// A crash mid-append leaves a partial last line; its change never
		// committed, and it parses as none of these.
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) == 2 && fields[1] == deletedMark:
			deleted = append(deleted, fields[0])
		case len(fields) == 3 && (fields[1] == editMark || fields[1] == editedMark):
			ms, perr := strconv.ParseInt(fields[2], 10, 64)
			if perr != nil {
				continue
			}
			e := edit{id: fields[0], was: time.UnixMilli(ms).UTC()}
			if fields[1] == editMark {
				edits[e] = true
			} else {
				done[e] = true
			}
		}
	}
	if err = sc.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("preference: read %s: %w", t.path, err)
	}
	return deleted, edits, done, nil
}

// Restore re-applies the recorded deletions and text changes to the
// database: a preference recorded as deleted is deleted again, and one
// last updated no later than the row a committed text change replaced is
// deleted, since it holds text the member replaced. A change recorded
// before its commit counts as committed only when the database holds a
// later row, so an edit that failed or never committed deletes nothing.
// It returns how many it deleted.
func (s *Service) Restore(ctx context.Context) (int64, error) {
	deleted, edits, done, err := s.tombstones.read()
	if err != nil {
		return 0, err
	}
	var pending []string
	for e := range edits {
		if !done[e] {
			pending = append(pending, e.id)
		}
	}
	if len(pending) > 0 {
		at, terr := s.store.PreferenceTimes(ctx, pending)
		if terr != nil {
			return 0, fmt.Errorf("preference: %w", terr)
		}
		for e := range edits {
			if updated, ok := at[e.id]; ok && !done[e] && updated.After(e.was) {
				if err = s.tombstones.edited(e.id, e.was); err != nil {
					return 0, err
				}
				done[e] = true
			}
		}
	}
	replaced := make(map[string]time.Time)
	for e := range done {
		if e.was.After(replaced[e.id]) {
			replaced[e.id] = e.was
		}
	}
	n, err := s.store.ApplyPreferenceTombstones(ctx, deleted, replaced)
	if err != nil {
		return n, fmt.Errorf("preference: %w", err)
	}
	if n > 0 {
		s.scrub()
	}
	return n, nil
}
