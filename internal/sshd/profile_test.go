package sshd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func withProfiles(t *testing.T) func(*Config) {
	t.Helper()
	return func(c *Config) {
		db, ok := c.Store.(*store.DB)
		if !ok {
			t.Fatal("store is not *store.DB")
		}
		svc, err := profile.New(db)
		if err != nil {
			t.Fatal(err)
		}
		c.Profiles = svc
	}
}

func withProfilesHomes(t *testing.T) func(*Config) {
	t.Helper()
	return func(c *Config) {
		withProfiles(t)(c)
		homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
		if err != nil {
			t.Fatal(err)
		}
		c.Homes = homes
	}
}

func TestProfilePushStatusDeltaAndErrors(t *testing.T) {
	e := newTestEnv(t, withProfiles(t))
	c := controlClient(t, e)

	var push protocol.ProfilePushResult
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files: []protocol.ProfileFile{
			{Path: "settings.json", Mode: 0o644, Content: []byte(`{"model":"opus"}`)},
			{Path: "commands/review.md", Mode: 0o644, Content: []byte("# review\n")},
		},
	}, &push); err != nil {
		t.Fatalf("push: %v", err)
	}
	if push.Snapshot.ID == "" || push.Snapshot.Digest == "" {
		t.Fatalf("snapshot = %+v", push.Snapshot)
	}

	var st protocol.ProfileStatusResult
	if err := c.Call(protocol.MethodProfileStatus, protocol.ProfileStatusParams{Harness: "claude"}, &st); err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Snapshot == nil || st.Snapshot.ID != push.Snapshot.ID {
		t.Fatalf("status snapshot = %+v", st.Snapshot)
	}
	if len(st.Files) != 2 || len(st.Snapshots) != 1 {
		t.Fatalf("files=%d snapshots=%d", len(st.Files), len(st.Snapshots))
	}

	known := map[string]string{}
	for _, f := range st.Files {
		known[f.Path] = f.Digest
	}
	var push2 protocol.ProfilePushResult
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Paths: []protocol.ProfileFile{
			{Path: "settings.json", Mode: 0o644, Digest: known["settings.json"]},
			{Path: "commands/review.md", Mode: 0o644, Digest: known["commands/review.md"]},
			{Path: "skills.md", Mode: 0o644, Digest: blobDigest([]byte("skill\n"))},
		},
		Blobs: []protocol.ProfileBlob{
			{Digest: blobDigest([]byte("skill\n")), Content: []byte("skill\n")},
		},
	}, &push2); err != nil {
		t.Fatalf("delta push: %v", err)
	}
	if push2.Snapshot.Digest == push.Snapshot.Digest {
		t.Fatal("delta push reused digest after adding a file")
	}

	err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: ".credentials.json", Mode: 0o644, Content: []byte("{}")}},
	}, nil)
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeDenied {
		t.Fatalf("denied path: err=%v", err)
	}
}

func TestProfileAllowSecretCreatesTimelineAudit(t *testing.T) {
	e := newTestEnv(t, withProfiles(t))
	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Workspace: e.ws.ID, Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	c := controlClient(t, e)
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness:     "claude",
		WorkspaceID: string(e.ws.ID),
		AllowSecret: []string{"settings.json"},
		Files:       []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: []byte(`{"ok":true}`)}},
	}, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	select {
	case ev := <-sub.Events():
		tl, ok := ev.Payload.(events.TimelinePayload)
		if !ok {
			t.Fatalf("payload %T", ev.Payload)
		}
		if tl.Kind != events.TimelineNote || !strings.Contains(tl.Message, "allow-secret") || !strings.Contains(tl.Message, "settings.json") {
			t.Fatalf("timeline = %+v", tl)
		}
		if ev.ActorID != e.member.ID {
			t.Errorf("actor = %s", ev.ActorID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for timeline audit")
	}
}

func TestProfileRollbackDoesNotMutateRunPin(t *testing.T) {
	e := newTestEnv(t, withProfiles(t))
	c := controlClient(t, e)
	var first, second protocol.ProfilePushResult
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "a.json", Mode: 0o644, Content: []byte(`{"n":1}`)}},
	}, &first); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "a.json", Mode: 0o644, Content: []byte(`{"n":2}`)}},
	}, &second); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetRunProfileSnapshot(context.Background(), e.run.ID, domain.ProfileSnapshotID(second.Snapshot.ID)); err != nil {
		t.Fatal(err)
	}
	var rb protocol.ProfileRollbackResult
	if err := c.Call(protocol.MethodProfileRollback, protocol.ProfileRollbackParams{
		Harness:    "claude",
		SnapshotID: first.Snapshot.ID,
	}, &rb); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rb.Snapshot.ID != first.Snapshot.ID {
		t.Fatalf("head = %s, want %s", rb.Snapshot.ID, first.Snapshot.ID)
	}
	run, err := e.store.GetRun(context.Background(), e.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ProfileSnapshotID != domain.ProfileSnapshotID(second.Snapshot.ID) {
		t.Fatalf("run pin = %s, want unchanged %s", run.ProfileSnapshotID, second.Snapshot.ID)
	}
	var got protocol.RunResult
	if err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(e.run.ID)}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Run.ProfileSnapshotID != second.Snapshot.ID {
		t.Fatalf("run.get pin = %q, want %s", got.Run.ProfileSnapshotID, second.Snapshot.ID)
	}
}

func TestProfilePushAndRollbackMaterializeMemberHome(t *testing.T) {
	e := newTestEnv(t, withProfilesHomes(t))
	c := controlClient(t, e)
	var first, second protocol.ProfilePushResult
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: []byte(`{"n":1}`)}},
	}, &first); err != nil {
		t.Fatalf("first push: %v", err)
	}
	home, err := e.srv.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".claude", "settings.json")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read materialized push %s: %v", target, err)
	}
	if string(got) != `{"n":1}` {
		t.Fatalf("materialized push = %q", got)
	}
	if pushErr := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: []byte(`{"n":2}`)}},
	}, &second); pushErr != nil {
		t.Fatalf("second push: %v", pushErr)
	}
	if rbErr := c.Call(protocol.MethodProfileRollback, protocol.ProfileRollbackParams{
		Harness: "claude", SnapshotID: first.Snapshot.ID,
	}, nil); rbErr != nil {
		t.Fatalf("rollback: %v", rbErr)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("read materialized rollback %s: %v", target, err)
	}
	if string(got) != `{"n":1}` {
		t.Fatalf("materialized rollback = %q", got)
	}
}

func TestProfilePushWithoutHomesSkipsMaterialization(t *testing.T) {
	e := newTestEnv(t, withProfiles(t))
	c := controlClient(t, e)
	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: []byte(`{"ok":true}`)}},
	}, nil); err != nil {
		t.Fatalf("push without homes: %v", err)
	}
}

func TestProfileErrorMapping(t *testing.T) {
	tests := []struct {
		err  error
		code int
	}{
		{profile.ErrDenied, protocol.CodeDenied},
		{profile.ErrNotFound, protocol.CodeNotFound},
		{profile.ErrTooLarge, protocol.CodeInvalidParams},
		{store.ErrNotFound, protocol.CodeNotFound},
	}
	for _, tt := range tests {
		if got := profileError(tt.err); got.Code != tt.code {
			t.Errorf("profileError(%v) = %d, want %d", tt.err, got.Code, tt.code)
		}
	}
}

func TestProfilePushSecretRequiresAllowAndWorkspace(t *testing.T) {
	e := newTestEnv(t, withProfiles(t))
	c := controlClient(t, e)
	secret := []byte("This settings file embeds token=QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy")

	err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: secret}},
	}, nil)
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeDenied {
		t.Fatalf("unapproved secret: err=%v", err)
	}

	err = c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness:     "claude",
		AllowSecret: []string{"settings.json"},
		Files:       []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: secret}},
	}, nil)
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeInvalidParams {
		t.Fatalf("allow without workspace: err=%v", err)
	}

	if err := c.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness:     "claude",
		WorkspaceID: string(e.ws.ID),
		AllowSecret: []string{"settings.json"},
		Files:       []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: secret}},
	}, nil); err != nil {
		t.Fatalf("allow+workspace: %v", err)
	}
}
