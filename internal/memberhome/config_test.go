package memberhome

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestConfigWriteRevisionAndMemberIsolation(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	memberA := domain.MemberID("member-a")
	memberB := domain.MemberID("member-b")
	first, err := manager.ConfigWrite(ctx, memberA, "claude", ".claude", "settings.json", []byte("one"), "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.Revision == "" || !first.Writable {
		t.Fatalf("create result = %+v", first)
	}
	if _, err = manager.ConfigWrite(ctx, memberA, "claude", ".claude", "settings.json", []byte("stale"), "", nil); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("empty revision on existing file = %v, want conflict", err)
	}
	if _, err = manager.ConfigWrite(ctx, memberA, "claude", ".claude", "settings.json", []byte("stale"), "wrong", nil); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("stale revision = %v, want conflict", err)
	}
	if _, err = manager.ConfigWrite(ctx, memberB, "claude", ".claude", "settings.json", []byte("other"), "", nil); err != nil {
		t.Fatalf("other member create: %v", err)
	}
	got, err := manager.ConfigRead(ctx, memberA, "claude", ".claude", "settings.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Content) != "one" || got.Revision != first.Revision {
		t.Fatalf("member A read = %+v", got)
	}
}

func TestConfigImportExcludesSecretsRuntimeAndCredentials(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.ConfigImport(context.Background(), "member-a", "claude", ".claude", []ConfigFile{
		{Path: "settings.json", Content: []byte(`{"model":"opus"}`), Mode: 0o644},
		{Path: "history.jsonl", Content: []byte("transcript"), Mode: 0o644},
		{Path: ".credentials.json", Content: []byte("token"), Mode: 0o600},
		{Path: "README.md", Content: []byte("token=QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"), Mode: 0o644},
	}, []string{".credentials.json"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Files != 1 || result.Bytes == 0 || len(result.Excluded) != 3 {
		t.Fatalf("result = %+v", result)
	}
	read, err := manager.ConfigRead(context.Background(), "member-a", "claude", ".claude", "settings.json", nil)
	if err != nil || string(read.Content) != `{"model":"opus"}` {
		t.Fatalf("settings = %+v, err=%v", read, err)
	}
	if _, err := manager.ConfigRead(context.Background(), "member-a", "claude", ".claude", "history.jsonl", nil); !errors.Is(err, ErrConfigDenied) {
		t.Fatalf("runtime read = %v, want denied", err)
	}
}

func TestConfigImportExcludesPiAndOMPTransientFilesKeepsConfiguration(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	config := []ConfigFile{
		{Path: "agent/skills/review.md", Content: []byte("# review"), Mode: 0o644},
		{Path: "agent/extensions/review.js", Content: []byte("export {}"), Mode: 0o644},
		{Path: "agent/npm/review/package.json", Content: []byte(`{"name":"review"}`), Mode: 0o644},
	}
	tests := []struct {
		name    string
		member  domain.MemberID
		root    string
		ignored []string
	}{
		{
			name:   "pi",
			member: "member-pi",
			root:   ".pi",
			ignored: []string{
				"agent/sessions/transcript.jsonl",
				"agent/tmp/extension.tgz",
			},
		},
		{
			name:   "omp",
			member: "member-omp",
			root:   ".omp",
			ignored: []string{
				"agent/sessions/transcript.jsonl",
				"agent/terminal-sessions/terminal.log",
				"agent/cache/compiled.js",
				"agent/history.db",
				"agent/history.db-shm",
				"agent/history.db-wal",
				"agent/models.db",
				"natives/tool",
				"cache/index",
				"logs/output.log",
				"run/pid",
				"collab/transcript.jsonl",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := append([]ConfigFile(nil), config...)
			for _, ignored := range tc.ignored {
				files = append(files, ConfigFile{Path: ignored, Content: []byte("runtime"), Mode: 0o644})
			}
			result, err := manager.ConfigImport(context.Background(), tc.member, tc.name, tc.root, files, nil)
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if result.Files != len(config) || result.Bytes != 34 || len(result.Excluded) != len(tc.ignored) {
				t.Fatalf("result = %+v, want %d files, 34 bytes, %d exclusions", result, len(config), len(tc.ignored))
			}
			excluded := make(map[string]bool, len(result.Excluded))
			for _, file := range result.Excluded {
				excluded[file.Path] = true
			}
			for _, ignored := range tc.ignored {
				if !excluded[ignored] {
					t.Errorf("runtime path %q was imported; exclusions = %+v", ignored, result.Excluded)
				}
			}
			for _, want := range config {
				got, readErr := manager.ConfigRead(context.Background(), tc.member, tc.name, tc.root, want.Path, nil)
				if readErr != nil || string(got.Content) != string(want.Content) {
					t.Errorf("configuration %q = %q, err=%v; want %q", want.Path, got.Content, readErr, want.Content)
				}
			}
		})
	}
}

func TestConfigImportPreflightsSymlinkBeforeMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	home, err := manager.Path("member-a")
	if err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(home, ".claude")
	if err = os.MkdirAll(filepath.Join(profileRoot, "unsafe"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeDir := filepath.Join(profileRoot, "unsafe")
	if err = os.Remove(unsafeDir); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Dir(outside), unsafeDir); err != nil {
		t.Fatal(err)
	}
	_, err = manager.ConfigImport(context.Background(), "member-a", "claude", ".claude", []ConfigFile{
		{Path: "good.json", Content: []byte("good"), Mode: 0o644},
		{Path: "unsafe/evil.json", Content: []byte("evil"), Mode: 0o644},
	}, nil)
	if !errors.Is(err, ErrConfigDenied) {
		t.Fatalf("symlink import = %v, want denied", err)
	}
	if _, statErr := os.Stat(filepath.Join(profileRoot, "good.json")); !os.IsNotExist(statErr) {
		t.Fatalf("good file stat = %v, import mutated before rejection", statErr)
	}
}

func TestConfigRejectsHardlinkedDestination(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	home, err := manager.Path("member-a")
	if err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(home, ".claude")
	if err = os.MkdirAll(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "credential")
	if err = os.WriteFile(outside, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(outside, filepath.Join(profileRoot, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ConfigWrite(context.Background(), "member-a", "claude", ".claude", "settings.json", []byte("new"), "", nil); !errors.Is(err, ErrConfigDenied) {
		t.Fatalf("hardlink write = %v, want denied", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "credential" {
		t.Fatalf("hardlink target = %q, err=%v", got, err)
	}
}

func TestConfigImportPreservesBinaryAsReadOnly(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte{0, 1, 2, 0xff}
	result, err := manager.ConfigImport(context.Background(), "member-a", "claude", ".claude", []ConfigFile{{
		Path: "plugin.bin", Content: binary, Mode: 0o600,
	}}, nil)
	if err != nil {
		t.Fatalf("binary import: %v", err)
	}
	if result.Files != 1 || result.Bytes != int64(len(binary)) {
		t.Fatalf("binary import result = %+v", result)
	}
	read, err := manager.ConfigRead(context.Background(), "member-a", "claude", ".claude", "plugin.bin", nil)
	if err != nil {
		t.Fatalf("binary read: %v", err)
	}
	if string(read.Content) != string(binary) || !read.Binary || read.Writable || read.Revision != "" {
		t.Fatalf("binary read = %+v", read)
	}
}

func TestConfigImportPreflightsCandidateCollisions(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ConfigWrite(context.Background(), "member-a", "claude", ".claude", "keep.txt", []byte("old"), "", nil); err != nil {
		t.Fatal(err)
	}
	_, err = manager.ConfigImport(context.Background(), "member-a", "claude", ".claude", []ConfigFile{
		{Path: "new.txt", Content: []byte("new"), Mode: 0o644},
		{Path: "a", Content: []byte("file"), Mode: 0o644},
		{Path: "a/nested.txt", Content: []byte("nested"), Mode: 0o644},
	}, nil)
	if !errors.Is(err, ErrConfigDenied) {
		t.Fatalf("candidate collision = %v, want denied", err)
	}
	if _, statErr := os.Stat(filepath.Join(manager.Root(), "member-a", ".claude", "new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("new file stat = %v, import mutated before rejection", statErr)
	}
	got, readErr := os.ReadFile(filepath.Join(manager.Root(), "member-a", ".claude", "keep.txt"))
	if readErr != nil || string(got) != "old" {
		t.Fatalf("existing file after collision = %q, err=%v", got, readErr)
	}
}

func TestConfigTreeAllowsOmittedRootPath(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ConfigImport(context.Background(), "member-a", "claude", ".claude", []ConfigFile{
		{Path: "settings.json", Content: []byte("{}"), Mode: 0o644},
		{Path: "commands/review.md", Content: []byte("# review"), Mode: 0o644},
	}, nil); err != nil {
		t.Fatalf("import: %v", err)
	}
	entries, err := manager.ConfigTree(context.Background(), "member-a", "claude", ".claude", "", nil)
	if err != nil {
		t.Fatalf("tree with omitted root path: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "commands" || entries[0].Kind != "dir" || entries[1].Name != "settings.json" || entries[1].Kind != "file" {
		t.Fatalf("tree = %+v, want commands directory and settings file", entries)
	}
}
