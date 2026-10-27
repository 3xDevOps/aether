package preference

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
		{" ​ ", "", protocol.CodeInvalidParams},
		{strings.Repeat("a", MaxText+1), "", protocol.CodeInvalidParams},
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
	if notes := f.svc.TakeChanges(f.alice); len(notes) != 1 || notes[0] != "preference "+p.ID+" was changed" {
		t.Fatalf("changes = %q", notes)
	}
	if notes := f.svc.TakeChanges(f.alice); len(notes) != 0 {
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
	if _, err = f.svc.Delete(ctx, f.alice, gone.ID); err != nil {
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
		if _, err := f.svc.Delete(ctx, f.alice, p.ID); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("a read racing the delete failed: %v", err)
		}
	}
}
