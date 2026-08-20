package profile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/store"
)

func testService(t *testing.T) (*Service, *store.DB, *domain.Member) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m := &domain.Member{
		DisplayName: "Ada",
		PublicKey:   testPublicKey(t),
		Color:       "#e6194b",
		Role:        domain.RoleCollaborator,
	}
	if err = db.CreateMember(context.Background(), m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	svc, err := New(db, filepath.Join(t.TempDir(), "snapshots"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, db, m
}

func testPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")
}

func testFiles() []File {
	return []File{
		{Path: "settings.json", Mode: 0o644, Content: []byte("{\"model\":\"opus\"}\n")},
		{Path: "commands/review.md", Mode: 0o644, Content: []byte("# review\n")},
	}
}

func TestPutGetDedup(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	a, err := svc.Put(ctx, string(m.ID), "claude", testFiles())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if a.Digest == "" || a.ID == "" {
		t.Fatalf("empty identity: %+v", a)
	}
	b, err := svc.Put(ctx, string(m.ID), "claude", testFiles())
	if err != nil {
		t.Fatalf("Put again: %v", err)
	}
	if a.ID != b.ID || a.Digest != b.Digest {
		t.Fatalf("dedup failed: first %+v second %+v", a, b)
	}
	got, files, err := svc.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("Get id = %s, want %s", got.ID, a.ID)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
}

func TestPutRejects(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	member := string(m.ID)
	cases := []struct {
		name string
		h    string
		file File
	}{
		{"absolute", "claude", File{Path: "/etc/passwd", Mode: 0o644, Content: []byte("x")}},
		{"traversal", "claude", File{Path: "../.ssh/id_rsa", Mode: 0o644, Content: []byte("x")}},
		{"dotdot segment", "claude", File{Path: "foo/../../etc/passwd", Mode: 0o644, Content: []byte("x")}},
		{"backslash", "claude", File{Path: `foo\bar`, Mode: 0o644, Content: []byte("x")}},
		{"empty", "claude", File{Path: "", Mode: 0o644, Content: []byte("x")}},
		{"deny credentials", "claude", File{Path: ".credentials.json", Mode: 0o644, Content: []byte("{}")}},
		{"deny auth.json", "claude", File{Path: "auth.json", Mode: 0o644, Content: []byte("{}")}},
		{"deny pem", "claude", File{Path: "id.pem", Mode: 0o644, Content: []byte("k")}},
		{"deny claude.json", "claude", File{Path: ".claude.json", Mode: 0o644, Content: []byte("{}")}},
		{"symlink mode", "claude", File{Path: "link", Mode: uint32(os.ModeSymlink | 0o644), Content: []byte("x")}},
		{"unix symlink", "claude", File{Path: "link2", Mode: sIFLNK | 0o644, Content: []byte("x")}},
		{"unknown harness", "nope", File{Path: "a", Mode: 0o644, Content: []byte("x")}},
		{"custom no root", "custom", File{Path: "a", Mode: 0o644, Content: []byte("x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Put(ctx, member, tc.h, []File{tc.file})
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("err = %v, want ErrDenied", err)
			}
		})
	}
}

func TestPutSizeCap(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	big := bytes.Repeat([]byte("a"), maxFileBytes+1)
	_, err := svc.Put(ctx, string(m.ID), "claude", []File{{Path: "big.txt", Mode: 0o644, Content: big}})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("file cap: %v", err)
	}
}

func TestPinRun(t *testing.T) {
	svc, db, m := testService(t)
	ctx := context.Background()
	snap, err := svc.Put(ctx, string(m.ID), "claude", testFiles())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	w := &domain.Workspace{Name: "ws", Environment: domain.WorkspaceEnvironment{CustomImage: "alpine"}}
	if err = db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	sess := &domain.Session{WorkspaceID: w.ID, Name: "s", BaseBranch: "main"}
	if err = db.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run := &domain.Run{
		SessionID: sess.ID,
		MemberID:  m.ID,
		Task:      "t",
		Harness:   "claude",
		Mode:      domain.LaunchTUI,
		Status:    domain.RunQueued,
	}
	if err = db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err = svc.PinRun(ctx, run.ID, snap.ID); err != nil {
		t.Fatalf("PinRun: %v", err)
	}
	got, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ProfileSnapshotID != snap.ID {
		t.Fatalf("pinned %q, want %q", got.ProfileSnapshotID, snap.ID)
	}
}

func TestMaterializeWritableIndependent(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	snap, err := svc.Put(ctx, string(m.ID), "claude", testFiles())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	d1 := filepath.Join(t.TempDir(), "a")
	d2 := filepath.Join(t.TempDir(), "b")
	if err = svc.Materialize(ctx, snap.ID, d1); err != nil {
		t.Fatalf("Materialize 1: %v", err)
	}
	if err = svc.Materialize(ctx, snap.ID, d2); err != nil {
		t.Fatalf("Materialize 2: %v", err)
	}
	if err = os.WriteFile(filepath.Join(d1, "settings.json"), []byte("mutated"), 0o644); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	if err = os.WriteFile(filepath.Join(d1, "new-session.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}
	_, files, err := svc.Get(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var settings []byte
	for _, f := range files {
		if f.Path == "settings.json" {
			settings = f.Content
		}
	}
	if string(settings) != "{\"model\":\"opus\"}\n" {
		t.Fatalf("stored content mutated: %q", settings)
	}
	second, err := os.ReadFile(filepath.Join(d2, "settings.json"))
	if err != nil {
		t.Fatalf("read dest2: %v", err)
	}
	if string(second) != "{\"model\":\"opus\"}\n" {
		t.Fatalf("second dest mutated: %q", second)
	}
}

func TestRollbackAndRetention(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	member := string(m.ID)
	var ids []domain.ProfileSnapshotID
	for i := 0; i < 11; i++ {
		files := []File{{Path: "n.txt", Mode: 0o644, Content: []byte{byte(i)}}}
		snap, err := svc.Put(ctx, member, "claude", files)
		if err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
		ids = append(ids, snap.ID)
	}
	list, err := svc.List(ctx, member, "claude")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != retainLatest {
		t.Fatalf("List len = %d, want %d", len(list), retainLatest)
	}
	if _, _, err = svc.Get(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest snapshot still present: %v", err)
	}
	kept := ids[1]
	latest, err := svc.Latest(ctx, member, "claude")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID != ids[10] {
		t.Fatalf("Latest = %s, want %s", latest.ID, ids[10])
	}
	if err = svc.Rollback(ctx, member, "claude", kept); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	head, err := svc.Latest(ctx, member, "claude")
	if err != nil {
		t.Fatalf("Latest after rollback: %v", err)
	}
	if head.ID != kept {
		t.Fatalf("head = %s, want %s", head.ID, kept)
	}
	if _, _, err = svc.Get(ctx, ids[10]); err != nil {
		t.Fatalf("rolled-away snapshot deleted: %v", err)
	}
	after, err := svc.List(ctx, member, "claude")
	if err != nil {
		t.Fatalf("List after rollback: %v", err)
	}
	if len(after) < 2 {
		t.Fatalf("rollback deleted history: %d", len(after))
	}
}

func TestGoldenPut(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("testdata", "put_golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g struct {
		Harness string `json:"harness"`
		Digest  string `json:"digest"`
		Files   []struct {
			Path    string `json:"path"`
			Mode    uint32 `json:"mode"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err = json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var files []File
	for _, f := range g.Files {
		files = append(files, File{Path: f.Path, Mode: f.Mode, Content: []byte(f.Content)})
	}
	snap, err := svc.Put(ctx, string(m.ID), g.Harness, files)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if snap.Digest != g.Digest {
		t.Fatalf("digest = %s, want %s", snap.Digest, g.Digest)
	}
	got, out, err := svc.Get(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Digest != g.Digest {
		t.Fatalf("Get digest = %s, want %s", got.Digest, g.Digest)
	}
	if len(out) != len(files) {
		t.Fatalf("file count %d, want %d", len(out), len(files))
	}
}

func TestLocalRootPrefixedPath(t *testing.T) {
	svc, _, m := testService(t)
	ctx := context.Background()
	files := []File{{Path: ".claude/settings.json", Mode: 0o644, Content: []byte("{}\n")}}
	snap, err := svc.Put(ctx, string(m.ID), "claude", files)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, got, err := svc.Get(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 1 || got[0].Path != "settings.json" {
		t.Fatalf("stored path = %+v, want settings.json", got)
	}
}

func TestLatestEmpty(t *testing.T) {
	svc, _, m := testService(t)
	_, err := svc.Latest(context.Background(), string(m.ID), "claude")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest = %v, want ErrNotFound", err)
	}
}

func TestDeniedNestedCredential(t *testing.T) {
	svc, _, m := testService(t)
	_, err := svc.Put(context.Background(), string(m.ID), "claude", []File{
		{Path: "projects/.credentials.json", Mode: 0o644, Content: []byte("{}")},
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("nested credential: %v", err)
	}
}

func TestCanonicalDigestStable(t *testing.T) {
	p, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("missing claude")
	}
	a := testFiles()
	b := []File{testFiles()[1], testFiles()[0]}
	na, err := validateFiles(p, a)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := validateFiles(p, b)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalDigest(na) != canonicalDigest(nb) {
		t.Fatal("digest depends on input order")
	}
}
