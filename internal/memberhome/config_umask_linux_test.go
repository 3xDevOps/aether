package memberhome

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestConfigWritePreservesModeWithRestrictiveUmask(t *testing.T) {
	if os.Getenv("AETHER_TEST_CONFIG_UMASK") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigWritePreservesModeWithRestrictiveUmask$")
		cmd.Env = append(os.Environ(), "AETHER_TEST_CONFIG_UMASK=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated umask regression: %v\n%s", err, output)
		}
		return
	}

	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "member-a", ".claude", "check.sh")
	if err = os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte("#!/bin/sh\necho before\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(name, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := manager.ConfigRead(context.Background(), "member-a", "claude", ".claude", "check.sh", nil)
	if err != nil {
		t.Fatal(err)
	}

	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	content := "#!/bin/sh\necho after\n"
	if _, err = manager.ConfigWrite(context.Background(), "member-a", "claude", ".claude", "check.sh", []byte(content), before.Revision, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("saved mode = %04o, want 0755", info.Mode().Perm())
	}
	got, err := os.ReadFile(name)
	if err != nil || string(got) != content {
		t.Fatalf("saved content = %q, err=%v", got, err)
	}

	content = "#!/bin/sh\necho imported\n"
	if _, err = manager.ConfigImport(context.Background(), "member-a", "claude", ".claude", []ConfigFile{
		{Path: "check.sh", Content: []byte(content), Mode: 0o644},
		{Path: "new.json", Content: []byte("{}"), Mode: 0o644},
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name    string
		mode    os.FileMode
		content string
	}{
		{"check.sh", 0o755, content},
		{"new.json", 0o644, "{}"},
	} {
		target := filepath.Join(filepath.Dir(name), file.name)
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != file.mode {
			t.Errorf("imported %s mode = %04o, want %04o", file.name, info.Mode().Perm(), file.mode)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != file.content {
			t.Errorf("imported %s content = %q, err=%v, want %q", file.name, got, err, file.content)
		}
	}
}
