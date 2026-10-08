package disk

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistentBytesAndGlobalHardlinks(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	shared := write("repos/workspace.git/object", 100)
	write("repos/workspace.git/independent-object", 100)
	for _, name := range []string{"repos/workspace.git/linked-object", "checkouts/run/shared", "checkouts/run.diffsnap/shared", "homes/member/shared", "evidence/shared", "profiles/shared", "transcripts/shared", "aether.db-wal"} {
		target := write(name, 0)
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(shared, target); err != nil {
			t.Fatal(err)
		}
	}
	write("homes/member/private", 20)
	write("evidence/packet.transcript", 30)
	write("profiles/profile", 40)
	write("checkouts/run/work", 50)
	write("checkouts/run.diffsnap/object", 60)
	write("aether.db", 70)
	u := components(dir)
	if u.RepoBytes != 200 || u.HomeBytes != 20 || u.EvidenceBytes != 30 || u.OtherBytes != 40 || u.WorktreeBytes != 110 || u.SnapshotBytes != 60 || u.DatabaseBytes != 70 || u.TranscriptBytes != 0 {
		t.Fatalf("persistent/hardlink accounting: %+v", u)
	}
	var attributed uint64
	for _, e := range u.Entries {
		attributed += e.Bytes
	}
	if attributed != 470 || len(u.Warnings) != 0 {
		t.Fatalf("attributed %d; warnings %v", attributed, u.Warnings)
	}
}

func TestMeasureDoesNotFollowSymlinkEscapes(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "credential")
	if err := os.WriteFile(secret, make([]byte, 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "checkouts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"homes": outside, "aether.db": secret, "checkouts/escape": outside, "other-secret": secret} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	u, err := Measure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.HomeBytes+u.DatabaseBytes+u.WorktreeBytes+u.OtherBytes+u.RepoBytes+u.TranscriptBytes+u.EvidenceBytes != 0 {
		t.Fatalf("symlink target bytes were inventoried: %+v", u)
	}
	if len(u.Entries) != 0 || len(u.Warnings) != 0 {
		t.Fatalf("skipped symlinks were inventoried: %+v", u)
	}
	report, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{outside, secret, "credential"} {
		if strings.Contains(string(report), private) {
			t.Fatalf("symlink target identifier leaked: %q", private)
		}
	}
}

func TestMeasureRootAliasDoesNotStealHomeOwnership(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "homes/member"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "homes/member/private"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("homes", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	u, err := Measure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.HomeBytes != 4 || u.OtherBytes != 0 || len(u.Entries) != 1 || u.Entries[0].Kind != "home" || u.Entries[0].Key != "member" {
		t.Fatalf("alias stole real ownership: %+v", u)
	}
}

func TestMeasureAggregatesTranscriptSegments(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "transcripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run.cast", "run.items.jsonl", "run.~first.cast", "run.~second.cast"} {
		if err := os.WriteFile(filepath.Join(dir, "transcripts", name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	u, err := Measure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.TranscriptBytes != 16 || len(u.Entries) != 1 || u.Entries[0].Kind != "transcript" || u.Entries[0].Key != "run" || u.Entries[0].Bytes != 16 {
		t.Fatalf("transcript segments were not aggregated: %+v", u)
	}
}

type unreadableStorage struct{ fs.FS }

func (f unreadableStorage) Open(name string) (fs.File, error) {
	if name == "homes/member" {
		return nil, &fs.PathError{Op: "open", Path: "private-credential-path", Err: fs.ErrPermission}
	}
	return f.FS.Open(name)
}

func TestPartialMeasurementKeepsReadableBytesAndFailedOwner(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "homes/member"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aether.db"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	u := componentTree(unreadableStorage{FS: root.FS()}, newSeen(root))
	if u.DatabaseBytes != 8 || len(u.Warnings) != 1 || strings.Contains(u.Warnings[0], "private-credential-path") {
		t.Fatalf("partial measurement: %+v", u)
	}
	for _, e := range u.Entries {
		if e.Kind == "home" && e.Key == "member" && e.Error != "" && e.Bytes == 0 {
			return
		}
	}
	t.Fatal("failed owner omitted from partial inventory")
}

func TestCachePoolsShareGlobalInodeAccountingWithoutFollowingLinks(t *testing.T) {
	dir := t.TempDir()
	cacheRoot := filepath.Join(dir, "home-caches")
	write := func(name string, size int) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	repo := write("repos/ws.git/object", 31)
	run := write("home-caches/launcher/runs/data/build", 17)
	write("home-caches/launcher/terminal/data/private", 13)
	write("homes/account/credential", 7)
	for target, source := range map[string]string{
		"home-caches/launcher/runs/data/repo":       repo,
		"home-caches/launcher/terminal/data/shared": run,
	} {
		if err := os.Link(source, filepath.Join(dir, target)); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(cacheRoot, "launcher/runs/data/escape")); err != nil {
		t.Fatal(err)
	}
	u := components(dir)
	if u.CacheBytes != 30 || u.RepoBytes != 31 || u.HomeBytes != 7 || len(u.Warnings) != 0 {
		t.Fatalf("cache attribution counted a shared inode or symlink target: %+v", u)
	}
	entries := make(map[string]uint64)
	for _, entry := range u.Entries {
		if entry.Kind == "cache" {
			entries[entry.Key] = entry.Bytes
			if entry.ReclaimableBytes != nil {
				t.Fatal("apparent size is not a physical reclaimability promise")
			}
		}
	}
	if entries["launcher/runs"] != 17 || entries["launcher/terminal"] != 13 {
		t.Fatalf("wrong pool attribution: %v", entries)
	}
	pools, err := MeasureCachePools(cacheRoot)
	if err != nil || len(pools) != 2 || pools["launcher/runs"] != 48 || pools["launcher/terminal"] != 13 {
		t.Fatalf("cache-only inode walk: %v, %v", pools, err)
	}
	if _, err := MeasureCachePools(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing cache root was reported as a complete zero-byte scan")
	}
}
