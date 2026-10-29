package preference

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// genericSecret is the gitleaks generic-api-key sample internal/secretscan
// tests with.
const genericSecret = "QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"

type fixture struct {
	dir   string
	db    *store.DB
	svc   *Service
	alice domain.MemberID
	ws    *domain.Workspace
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir()}
	db, err := storetest.Open(f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	f.db = db
	alice := &domain.Member{DisplayName: "Alice", TailnetLogin: "alice@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(context.Background(), alice); err != nil {
		t.Fatal(err)
	}
	f.ws = &domain.Workspace{Name: "api", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateWorkspace(context.Background(), f.ws); err != nil {
		t.Fatal(err)
	}
	f.alice = alice.ID
	f.svc = New(db, filepath.Join(f.dir, "preference-tombstones"))
	return f
}

func (f *fixture) dbPath() string { return filepath.Join(f.dir, "aether.db") }

func (f *fixture) add(t *testing.T, text string) protocol.Preference {
	t.Helper()
	res, err := f.svc.Add(context.Background(), f.alice, text, "", protocol.PreferenceSourceSettings, "")
	if err != nil {
		t.Fatal(err)
	}
	return res.Preference
}

func code(err error) int {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return 0
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"  Prefer\ttable\n\noutput ":      "Prefer table output",
		"Prefer \u202etable\u200b output": "Prefer table output",
		"Ｐｒｅｆｅｒ ﬁles":                     "Prefer files",
		"Prefer\x07 table output":         "Prefer table output",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddRefusesBadText(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		text, ws string
		code     int
	}{
		{" \u200b ", "", protocol.CodeInvalidParams},
		{strings.Repeat("a", MaxText+1), "", protocol.CodeInvalidParams},
		{"Use tabs", "", protocol.CodeInvalidParams},
		{"Use go fmt", "", protocol.CodeInvalidParams},
		{"Prefer table output", "ws-missing", protocol.CodeInvalidParams},
	} {
		if _, err := f.svc.Add(ctx, f.alice, tc.text, tc.ws, protocol.PreferenceSourceSettings, ""); code(err) != tc.code {
			t.Errorf("Add(%.20q, %q) = %v, want code %d", tc.text, tc.ws, err, tc.code)
		}
	}
	if _, err := f.svc.Add(ctx, f.alice, "Prefer table output", string(f.ws.ID), protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatalf("Add in an existing workspace: %v", err)
	}
}

// A secret is refused by kind, its value never echoed, and nothing reaches
// the database.
func TestSecretIsRefusedAndNotPersisted(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Add(context.Background(), f.alice, "Always use token="+genericSecret, "", protocol.PreferenceSourceSettings, "")
	if code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "gitleaks rule") || strings.Contains(err.Error(), genericSecret) {
		t.Fatalf("Add with a secret = %v; want a denial naming the rule, not the value", err)
	}
	p := f.add(t, "Prefer table output")
	text := "Always use token=" + genericSecret
	if _, err = f.svc.Update(context.Background(), f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Text: &text}, protocol.PreferenceSourceSettings, ""); code(err) != protocol.CodeDenied {
		t.Fatalf("Update with a secret = %v", err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{f.dbPath(), f.dbPath() + "-wal"} {
		b, _ := os.ReadFile(file)
		if bytes.Contains(b, []byte(genericSecret)) {
			t.Fatalf("%s holds the refused secret", filepath.Base(file))
		}
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateReturnsPreviousAndNotesChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.add(t, "Prefer table output")
	text := "Prefer short answers"
	res, err := f.svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Text: &text, WorkspaceID: string(f.ws.ID)}, protocol.PreferenceSourceAssistant, "msg-1")
	if err != nil || res.Previous == nil || res.Previous.Text != "Prefer table output" || res.Preference.Text != text ||
		res.Preference.Source != protocol.PreferenceSourceAssistant || res.Preference.SourceRef != "msg-1" || res.Preference.WorkspaceID != string(f.ws.ID) {
		t.Fatalf("Update = %+v, %v", res, err)
	}
	off := false
	if _, err = f.svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Enabled: &off, ClearWorkspace: true}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	listed, _ := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{})
	all, _ := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{IncludeDisabled: true})
	if len(listed.Preferences) != 0 || len(all.Preferences) != 1 || all.Preferences[0].WorkspaceID != "" || !listed.Enabled {
		t.Fatalf("after disabling: enabled list %+v, all %+v", listed, all)
	}
	if notes := f.svc.TakeChanges(ctx, f.alice); len(notes) != 2 || notes[0] != restarted || notes[1] != "preference "+p.ID+" was changed; do not use the old text" {
		t.Fatalf("changes = %q", notes)
	}
	if notes := f.svc.TakeChanges(ctx, f.alice); len(notes) != 0 {
		t.Fatalf("changes taken twice = %q", notes)
	}
}

func TestListFiltersByWorkspaceAndQuery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.add(t, "Prefer table output")
	other := &domain.Workspace{Name: "web", BaseBranch: domain.DefaultBaseBranch}
	if err := f.db.CreateWorkspace(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []*domain.Workspace{f.ws, other} {
		if _, err := f.svc.Add(ctx, f.alice, "Prefer pnpm in "+ws.Name, string(ws.ID), protocol.PreferenceSourceSettings, ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{WorkspaceID: string(f.ws.ID)})
	if err != nil || len(got.Preferences) != 2 {
		t.Fatalf("list in api = %+v, %v; want api's and the global one", got.Preferences, err)
	}
	got, _ = f.svc.List(ctx, f.alice, protocol.PreferenceListParams{Query: "PNPM"})
	if len(got.Preferences) != 2 {
		t.Fatalf("query = %+v", got.Preferences)
	}
}

// A delete or an edit is re-applied when the database is restored from a
// copy taken before it, as an update rollback does.
func TestRestoreReappliesTombstonesToAnOlderDatabase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gone := f.add(t, "Prefer table output")
	edited := f.add(t, "Prefer verbose logs")
	kept := f.add(t, "Prefer dark mode")
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if _, err = f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: gone.ID}); err != nil {
		t.Fatal(err)
	}
	text := "Prefer quiet logs"
	if _, err = f.svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: edited.ID, Text: &text}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.dbPath(), snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	before, _ := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{})
	if len(before.Preferences) != 3 {
		t.Fatalf("the restored copy has %d preferences, want the 3 it was taken with", len(before.Preferences))
	}
	n, err := f.svc.Restore(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Restore = %d, %v; want 2", n, err)
	}
	after, _ := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{})
	if len(after.Preferences) != 1 || after.Preferences[0].ID != kept.ID {
		t.Fatalf("after Restore: %+v; want only %s", after.Preferences, kept.ID)
	}
	tomb, _ := os.ReadFile(filepath.Join(f.dir, "preference-tombstones"))
	if bytes.Contains(tomb, []byte("Prefer")) {
		t.Fatalf("the tombstone file holds preference text: %q", tomb)
	}
}

// Reads racing a delete see the row or nothing, never an error.
func TestDeleteRacingReads(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for range 20 {
		p := f.add(t, "Prefer table output")
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 4 {
			wg.Go(func() {
				if _, err := f.svc.Get(ctx, f.alice, p.ID); err != nil && code(err) != protocol.CodeNotFound {
					errs <- err
				}
				if _, err := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{}); err != nil {
					errs <- err
				}
			})
		}
		if _, err := f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: p.ID}); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("a read racing the delete failed: %v", err)
		}
	}
}

// A crash in the middle of a tombstone line does not corrupt the next one.
func TestTombstoneAfterAPartialLine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.add(t, "Prefer table output")
	if err := os.WriteFile(filepath.Join(f.dir, "preference-tombstones"), []byte("pref-partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: p.ID}); err != nil {
		t.Fatal(err)
	}
	deleted, _, _, err := f.svc.tombstones.read()
	if err != nil || len(deleted) != 1 || deleted[0] != p.ID {
		t.Fatalf("deleted = %q, %v; want only %s", deleted, err, p.ID)
	}
}

// A cancelled request cannot roll back a change whose tombstone it wrote,
// which a restore would later apply.
func TestCancelledDeleteStillCommits(t *testing.T) {
	f := newFixture(t)
	p := f.add(t, "Prefer table output")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: p.ID}); err != nil {
		t.Fatalf("delete cancelled after its tombstone = %v", err)
	}
	if _, err := f.svc.Get(context.Background(), f.alice, p.ID); code(err) != protocol.CodeNotFound {
		t.Fatalf("after the delete: %v", err)
	}
}

// failingUpdate writes the tombstone of an edit, then fails it, as an
// UPDATE that fails or a crash before the commit does.
type failingUpdate struct{ *store.DB }

func (f failingUpdate) UpdatePreference(ctx context.Context, member domain.MemberID, id string, edit func(*store.Preference) error,
	commit func(old, cur store.Preference) error) (store.Preference, store.Preference, error) {
	return f.DB.UpdatePreference(ctx, member, id, edit, func(old, cur store.Preference) error {
		if err := commit(old, cur); err != nil {
			return err
		}
		return errors.New("disk I/O error")
	})
}

// An edit that never committed deletes nothing at the next start, while one
// that did still deletes an older copy of its row.
func TestRestoreKeepsARowWhoseEditFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.add(t, "Prefer table output")
	text := "Prefer short answers"
	failing := New(failingUpdate{f.db}, filepath.Join(f.dir, "preference-tombstones"))
	if _, err := failing.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Text: &text}, protocol.PreferenceSourceSettings, ""); err == nil {
		t.Fatal("the failing update succeeded")
	}
	if n, err := f.svc.Restore(ctx); err != nil || n != 0 {
		t.Fatalf("Restore after a failed edit = %d, %v; want nothing deleted", n, err)
	}
	if got, err := f.svc.Get(ctx, f.alice, p.ID); err != nil || got.Text != "Prefer table output" {
		t.Fatalf("after Restore: %+v, %v; want the unchanged row", got, err)
	}
}

// failingText fails every text change after writing its tombstone, as
// failingUpdate does, and passes other changes.
type failingText struct{ *store.DB }

func (f failingText) UpdatePreference(ctx context.Context, member domain.MemberID, id string, edit func(*store.Preference) error,
	commit func(old, cur store.Preference) error) (store.Preference, store.Preference, error) {
	return f.DB.UpdatePreference(ctx, member, id, edit, func(old, cur store.Preference) error {
		if err := commit(old, cur); err != nil || cur.Text == old.Text {
			return err
		}
		return errors.New("disk I/O error")
	})
}

// reopen closes the database, writes snapshot over it when set, and opens
// it again with a new service, as a restart does.
func (f *fixture) reopen(t *testing.T, snapshot []byte) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		if err := os.WriteFile(f.dbPath(), snapshot, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	f.db, f.svc = db, New(db, filepath.Join(f.dir, "preference-tombstones"))
}

// A text edit that failed is not taken as committed once a later change,
// such as turning the preference off, makes the row newer, whether that
// change comes in the same process or after a restart.
func TestAFailedEditNeverDeletesTheRowLater(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.add(t, "Prefer table output")
	f.reopen(t, nil)
	snapshot, err := os.ReadFile(f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	svc := New(failingText{f.db}, filepath.Join(f.dir, "preference-tombstones"))
	text, off, on := "Prefer short answers", false, true
	edit := protocol.PreferenceUpdateParams{ID: p.ID, Text: &text}
	if _, err = svc.Update(ctx, f.alice, edit, protocol.PreferenceSourceSettings, ""); err == nil {
		t.Fatal("the failing edit succeeded")
	}
	if _, err = svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Enabled: &off}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, f.alice, edit, protocol.PreferenceSourceSettings, ""); err == nil {
		t.Fatal("the failing edit succeeded")
	}
	f.reopen(t, nil)
	if n, rerr := f.svc.Restore(ctx); rerr != nil || n != 0 {
		t.Fatalf("Restore after the failed edits = %d, %v; want nothing deleted", n, rerr)
	}
	if _, err = f.svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Enabled: &on}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	f.reopen(t, nil)
	if n, rerr := f.svc.Restore(ctx); rerr != nil || n != 0 {
		t.Fatalf("Restore after turning it on = %d, %v; want nothing deleted", n, rerr)
	}
	f.reopen(t, snapshot)
	if n, rerr := f.svc.Restore(ctx); rerr != nil || n != 0 {
		t.Fatalf("Restore of a copy taken before the failed edits = %d, %v; want its unchanged row kept", n, rerr)
	}
}

// An edit whose commit was not recorded, as after a crash right after it
// committed, is found committed from the database and recorded, so a later
// restored copy still loses its old text.
func TestRestoreRecordsAnEditThatCommitted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.add(t, "Prefer table output")
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if err = f.svc.tombstones.edit(p.ID, p.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.db.UpdatePreference(ctx, f.alice, p.ID, func(row *store.Preference) error { row.Text = "Prefer short answers"; return nil },
		func(store.Preference, store.Preference) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if n, rerr := f.svc.Restore(ctx); rerr != nil || n != 0 {
		t.Fatalf("Restore = %d, %v; want the edited row kept", n, rerr)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.dbPath(), snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if n, err := f.svc.Restore(ctx); err != nil || n != 1 {
		t.Fatalf("Restore of the copy taken before the edit = %d, %v; want its old row deleted", n, err)
	}
}

// Deleting a member records their preferences as deleted, so restoring a
// copy of the database taken before does not bring them back.
func TestDeletedMemberStaysDeletedInARestoredDatabase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.add(t, "Prefer table output")
	f.add(t, "Prefer short answers")
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if err = f.svc.DeleteMember(ctx, f.alice); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.dbPath(), snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	if f.db, err = store.Open(f.dbPath()); err != nil {
		t.Fatal(err)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if n, err := f.svc.Restore(ctx); err != nil || n != 2 {
		t.Fatalf("Restore = %d, %v; want both of the removed member's preferences deleted", n, err)
	}
}

// A forget succeeds while a reader holds the write-ahead log, and the log
// loses the text once the reader is done.
func TestForgetWhileAReaderHoldsTheLog(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.retry = 10 * time.Millisecond
	p := f.add(t, "Prefer canary-otter-forgotten output")
	reader, err := sql.Open("sqlite", "file:"+f.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	tx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM member_preferences`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: p.ID}); err != nil {
		t.Fatalf("forget while a reader holds the log = %v; want success", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, _ := os.ReadFile(f.dbPath() + "-wal")
		if !bytes.Contains(b, []byte("canary-otter-forgotten")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the write-ahead log still holds the forgotten text")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The restart note goes only to a member who lets the assistant use
// preferences and keeps at least one.
func TestRestartNoteOnlyForMembersWithPreferences(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if notes := f.svc.TakeChanges(ctx, f.alice); len(notes) != 0 {
		t.Fatalf("notes with no preferences = %q", notes)
	}
	f.add(t, "Prefer table output")
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if err := f.db.PutAssistantSettings(ctx, f.alice, store.AssistantSettings{ShareView: true}); err != nil {
		t.Fatal(err)
	}
	if notes := f.svc.TakeChanges(ctx, f.alice); len(notes) != 0 {
		t.Fatalf("notes with preferences off = %q", notes)
	}
	f.svc = New(f.db, filepath.Join(f.dir, "preference-tombstones"))
	if err := f.db.PutAssistantSettings(ctx, f.alice, store.AssistantSettings{ShareView: true, PreferencesEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if notes := f.svc.TakeChanges(ctx, f.alice); len(notes) != 1 || notes[0] != restarted {
		t.Fatalf("notes with a preference = %q; want the restart note", notes)
	}
}

// A member delete that fails leaves their preferences in place and records
// none of them as deleted.
func TestFailedMemberDeleteKeepsPreferences(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.add(t, "Prefer table output")
	if err := f.svc.DeleteMember(ctx, "mem-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteMember of a missing member = %v", err)
	}
	failing := New(f.db, filepath.Join(f.dir, "no-such-dir", "preference-tombstones"))
	if err := failing.DeleteMember(ctx, f.alice); err == nil {
		t.Fatal("DeleteMember with no tombstone file succeeded")
	}
	if got, _ := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{}); len(got.Preferences) != 1 {
		t.Fatalf("after a failed member delete: %+v", got.Preferences)
	}
	if _, err := f.db.GetMember(ctx, f.alice); err != nil {
		t.Fatalf("the member is gone after a failed delete: %v", err)
	}
}

// lostEdited commits the edit, then makes the tombstone file unwritable.
type lostEdited struct {
	*store.DB
	tombs string
}

func (l lostEdited) UpdatePreference(ctx context.Context, member domain.MemberID, id string, edit func(*store.Preference) error,
	commit func(old, cur store.Preference) error) (store.Preference, store.Preference, error) {
	old, cur, err := l.DB.UpdatePreference(ctx, member, id, edit, commit)
	if err == nil {
		err = errors.Join(os.Remove(l.tombs), os.Mkdir(l.tombs, 0o700))
	}
	return old, cur, err
}

// A committed edit whose record could not be written is reported, not
// passed off as durable.
func TestEditWhoseRecordFailsIsReported(t *testing.T) {
	f := newFixture(t)
	p := f.add(t, "Prefer table output")
	tombs := filepath.Join(f.dir, "preference-tombstones")
	svc := New(lostEdited{f.db, tombs}, tombs)
	text := "Prefer short answers"
	_, err := svc.Update(context.Background(), f.alice, protocol.PreferenceUpdateParams{ID: p.ID, Text: &text}, protocol.PreferenceSourceSettings, "")
	if err == nil || !strings.Contains(err.Error(), "the text changed") {
		t.Fatalf("Update with its record lost = %v; want the failure reported", err)
	}
}

// scriptedCheckpoint fails the first checkpoint and the third, which
// another change makes while the retry worker's own checkpoint, the
// second, is running.
type scriptedCheckpoint struct {
	*store.DB
	mu      sync.Mutex
	calls   int
	running chan struct{}
	release chan struct{}
	fourth  chan struct{}
}

func (s *scriptedCheckpoint) CheckpointWAL(context.Context) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	switch n {
	case 1, 3:
		return errors.New("a reader holds the write-ahead log")
	case 2:
		close(s.running)
		<-s.release
	case 4:
		close(s.fourth)
	}
	return nil
}

// A checkpoint that fails while the retry worker is finishing is retried
// too.
func TestScrubRetriesAFailureDuringItsLastAttempt(t *testing.T) {
	f := newFixture(t)
	fake := &scriptedCheckpoint{DB: f.db, running: make(chan struct{}), release: make(chan struct{}), fourth: make(chan struct{})}
	svc := New(fake, filepath.Join(f.dir, "preference-tombstones"))
	svc.retry = time.Millisecond
	svc.scrub()
	<-fake.running
	svc.scrub()
	close(fake.release)
	select {
	case <-fake.fourth:
	case <-time.After(10 * time.Second):
		t.Fatal("the checkpoint that failed during the worker's last attempt was not retried")
	}
}

// An update or delete that names when the member last saw the preference
// changes nothing once it changed since.
func TestStaleUpdateAndDeleteChangeNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seen := f.add(t, "Prefer table output")
	text := "Prefer short answers"
	res, err := f.svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: seen.ID, Text: &text, ExpectedUpdatedAt: seen.UpdatedAt}, protocol.PreferenceSourceSettings, "")
	if err != nil {
		t.Fatalf("update as seen: %v", err)
	}
	other := "Prefer long answers"
	if _, err = f.svc.Update(ctx, f.alice, protocol.PreferenceUpdateParams{ID: seen.ID, Text: &other, ExpectedUpdatedAt: seen.UpdatedAt}, protocol.PreferenceSourceSettings, ""); code(err) != protocol.CodeConflict ||
		!strings.Contains(err.Error(), "changed since you saw it") {
		t.Fatalf("stale update = %v, want a conflict", err)
	}
	if _, err = f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: seen.ID, ExpectedUpdatedAt: seen.UpdatedAt}); code(err) != protocol.CodeConflict ||
		!strings.Contains(err.Error(), "changed since you saw it") {
		t.Fatalf("stale delete = %v, want a conflict", err)
	}
	got, err := f.svc.Get(ctx, f.alice, seen.ID)
	if err != nil || got.Text != text || !got.UpdatedAt.Equal(res.Preference.UpdatedAt) {
		t.Fatalf("after the stale writes: %+v, %v; want the first update kept", got, err)
	}
	if _, err = f.svc.Delete(ctx, f.alice, protocol.PreferenceDeleteParams{ID: seen.ID, ExpectedUpdatedAt: res.Preference.UpdatedAt}); err != nil {
		t.Fatalf("delete as seen: %v", err)
	}
}

// Concurrent saves of the same text for the same scope keep one row, and
// the others are refused with its id; another scope keeps its own.
func TestConcurrentAddsOfTheSameTextKeepOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = f.svc.Add(ctx, f.alice, "Prefer table  output", "", protocol.PreferenceSourceSettings, "")
		})
	}
	wg.Wait()
	all, err := f.svc.List(ctx, f.alice, protocol.PreferenceListParams{IncludeDisabled: true})
	if err != nil || len(all.Preferences) != 1 {
		t.Fatalf("list = %+v, %v; want one row", all.Preferences, err)
	}
	for _, err := range errs {
		if err != nil && (code(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "already remembered as preference "+all.Preferences[0].ID)) {
			t.Errorf("duplicate add = %v, want a conflict naming %s", err, all.Preferences[0].ID)
		}
	}
	if _, err = f.svc.Add(ctx, f.alice, "Prefer table output", string(f.ws.ID), protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatalf("the same text for one workspace: %v", err)
	}
}
