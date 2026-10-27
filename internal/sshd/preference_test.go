package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func rpcCode(err error) int {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return 0
}

// Only the member who saved a preference can read, change or delete it:
// a teammate, a viewer and an admin see none of it and get not found for
// its id, and a connector is refused every preference method whatever its
// scopes.
func TestPreferencesAreOwnerOnly(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) {
		db, ok := c.Store.(*store.DB)
		if !ok {
			t.Fatalf("store is %T, want *store.DB", c.Store)
		}
		c.Services.Preferences = preference.New(db, filepath.Join(t.TempDir(), "preference-tombstones"))
	})
	aliceKey, alice := addMember(t, e, "Alice", domain.RoleCollaborator, false)
	bobKey, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	vicKey, _ := addMember(t, e, "Vic", domain.RoleViewer, false)
	adaKey, _ := addMember(t, e, "Ada", domain.RoleAdmin, false)
	aliceC := controlAs(t, e, aliceKey)

	var added protocol.PreferenceResult
	if err := aliceC.Call(protocol.MethodPreferenceAdd, map[string]any{"text": "Prefer table output", "workspace_id": string(e.ws.ID), "source": "assistant"}, &added); err != nil {
		t.Fatalf("preference.add: %v", err)
	}
	if added.Preference.Source != protocol.PreferenceSourceSettings || added.Preference.SourceRef != "" {
		t.Fatalf("added = %+v; want the server to set the source from the path", added.Preference)
	}
	id := added.Preference.ID
	text := "Prefer short answers"
	for name, key := range map[string]ssh.Signer{"bob": bobKey, "viewer": vicKey, "admin": adaKey} {
		c := controlAs(t, e, key)
		var listed protocol.PreferenceListResult
		if err := c.Call(protocol.MethodPreferenceList, protocol.PreferenceListParams{IncludeDisabled: true}, &listed); err != nil || len(listed.Preferences) != 0 {
			t.Errorf("%s lists %+v, %v; want none of alice's", name, listed.Preferences, err)
		}
		if err := c.Call(protocol.MethodPreferenceUpdate, protocol.PreferenceUpdateParams{ID: id, Text: &text}, nil); rpcCode(err) != protocol.CodeNotFound {
			t.Errorf("%s updates alice's preference: %v, want not found", name, err)
		}
		if err := c.Call(protocol.MethodPreferenceDelete, protocol.PreferenceIDParams{ID: id}, nil); rpcCode(err) != protocol.CodeNotFound {
			t.Errorf("%s deletes alice's preference: %v, want not found", name, err)
		}
	}

	connector := e.srv.ConnectorLocal(ConnectorGrant{Member: alice.ID}, "con-test", "ChatGPT",
		[]string{ConnectorScopeRead, ConnectorScopeWrite, ConnectorScopeControl})
	for _, method := range []string{protocol.MethodPreferenceList, protocol.MethodPreferenceAdd, protocol.MethodPreferenceUpdate, protocol.MethodPreferenceDelete} {
		params, _ := json.Marshal(map[string]string{"id": id, "text": "Prefer tabs"})
		if _, perr := connector.Call(context.Background(), method, params); perr == nil || perr.Code != protocol.CodeDenied {
			t.Errorf("%s from a connector = %v, want denied", method, perr)
		}
		if _, ok := ConnectorMethodScopes(method); ok {
			t.Errorf("%s has connector scopes", method)
		}
	}

	var res protocol.PreferenceResult
	if err := aliceC.Call(protocol.MethodPreferenceUpdate, protocol.PreferenceUpdateParams{ID: id, Text: &text}, &res); err != nil ||
		res.Previous == nil || res.Previous.Text != "Prefer table output" || res.Preference.Text != text {
		t.Fatalf("alice's update = %+v, %v", res, err)
	}
	var del protocol.PreferenceDeleteResult
	if err := aliceC.Call(protocol.MethodPreferenceDelete, protocol.PreferenceIDParams{ID: id}, &del); err != nil || del.Deleted.ID != id {
		t.Fatalf("alice's delete = %+v, %v", del, err)
	}
	var listed protocol.PreferenceListResult
	if err := aliceC.Call(protocol.MethodPreferenceList, protocol.PreferenceListParams{IncludeDisabled: true}, &listed); err != nil || len(listed.Preferences) != 0 || !listed.Enabled {
		t.Fatalf("alice's list after the delete = %+v, %v", listed, err)
	}
}

// Removing a member records their preferences as deleted, so restoring a
// copy of the database taken before the removal does not bring them back.
func TestMemberRemoveRecordsPreferencesDeleted(t *testing.T) {
	t.Parallel()
	tombs := filepath.Join(t.TempDir(), "preference-tombstones")
	e := newTestEnv(t, func(c *Config) {
		db, ok := c.Store.(*store.DB)
		if !ok {
			t.Fatalf("store is %T, want *store.DB", c.Store)
		}
		c.Services.Preferences = preference.New(db, tombs)
	})
	bobKey, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	var added protocol.PreferenceResult
	if err := controlAs(t, e, bobKey).Call(protocol.MethodPreferenceAdd, protocol.PreferenceAddParams{Text: "Prefer table output"}, &added); err != nil {
		t.Fatal(err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(bob.ID)}, nil); err != nil {
		t.Fatalf("member.remove: %v", err)
	}
	b, err := os.ReadFile(tombs)
	if err != nil || !strings.Contains(string(b), added.Preference.ID+" deleted") {
		t.Fatalf("tombstones = %q, %v; want %s recorded as deleted", b, err, added.Preference.ID)
	}
}
