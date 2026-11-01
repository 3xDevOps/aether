//go:build integration

package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationPreferences drives preferences through the wired server:
// assistant.settings turns them off and marks the conversation temporary,
// a new conversation clears temporary, and a preference forgotten after a
// database copy was taken stays forgotten when that copy is restored and
// the server restarts.
func TestIntegrationPreferences(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := startAssistantServer(t, ctx, nil)

	var kept, gone protocol.PreferenceResult
	if perr := env.call(protocol.MethodPreferenceAdd, protocol.PreferenceAddParams{Text: "Prefer table output"}, &kept); perr != nil {
		t.Fatalf("preference.add: %v", perr)
	}
	if perr := env.call(protocol.MethodPreferenceAdd, protocol.PreferenceAddParams{Text: "Prefer short answers"}, &gone); perr != nil {
		t.Fatalf("preference.add: %v", perr)
	}
	if st := env.status(); !st.PreferencesEnabled || st.Temporary {
		t.Fatalf("status of a new member = %+v, want preferences on and not temporary", st)
	}
	off, on := false, true
	var settings protocol.AssistantSettings
	if perr := env.call(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{PreferencesEnabled: &off}, &settings); perr != nil || settings.PreferencesEnabled {
		t.Fatalf("turning preferences off = %+v, %v", settings, perr)
	}
	var listed protocol.PreferenceListResult
	if perr := env.call(protocol.MethodPreferenceList, protocol.PreferenceListParams{}, &listed); perr != nil || listed.Enabled || len(listed.Preferences) != 2 {
		t.Fatalf("preference.list with preferences off = %+v, %v", listed, perr)
	}
	if perr := env.call(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{Temporary: &on}, &settings); perr != nil || !settings.Temporary {
		t.Fatalf("a temporary conversation = %+v, %v", settings, perr)
	}
	if st := env.status(); st.PreferencesEnabled || !st.Temporary {
		t.Fatalf("status = %+v, want preferences off and temporary", st)
	}
	if perr := env.call(protocol.MethodAssistantReset, struct{}{}, nil); perr != nil {
		t.Fatalf("assistant.reset: %v", perr)
	}
	if st := env.status(); st.Temporary {
		t.Fatalf("status after a new conversation = %+v, want temporary cleared", st)
	}

	env.stop()
	db := filepath.Join(env.data, "aether.db")
	// Rows the closed server left in the WAL belong to the older database too.
	files := []string{db, db + "-wal", db + "-shm"}
	snapshot := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		snapshot[f] = data
	}
	env = startAssistantServer(t, ctx, env)
	if perr := env.call(protocol.MethodPreferenceDelete, protocol.PreferenceIDParams{ID: gone.Preference.ID}, nil); perr != nil {
		t.Fatalf("preference.delete: %v", perr)
	}
	env.stop()
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		if data, ok := snapshot[f]; ok {
			if err := os.WriteFile(f, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	env = startAssistantServer(t, ctx, env)
	defer env.stop()
	listed = protocol.PreferenceListResult{}
	if perr := env.call(protocol.MethodPreferenceList, protocol.PreferenceListParams{}, &listed); perr != nil ||
		len(listed.Preferences) != 1 || listed.Preferences[0].ID != kept.Preference.ID {
		t.Fatalf("after restoring the older database = %+v, %v; want only %s", listed.Preferences, perr, kept.Preference.ID)
	}
}
